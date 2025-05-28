// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/lib/pq"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// Cluster contains all configuration and runtime information for the target
// cluster. It will behave differently with regards to the LiquidConnections
// depending on the configuration of the fillLiquidConnections parameter on
// NewCluster. When LiquidConnections are not filled, the cluster is in
// read-only mode, which will cause all operations involving LiquidConnections
// to fallback to database operations.
type Cluster struct {
	Config            ClusterConfiguration
	DiscoveryPlugin   DiscoveryPlugin
	LiquidConnections map[db.ServiceType]*LiquidConnection
	// reference of the DB is necessary to delete leftover LiquidConnections
	DB *gorp.DbMap
}

// NewCluster creates a new Cluster instance also initializes the LiquidConnections - if configured.
// Errors will be logged when the requested DiscoveryPlugin cannot be found.
func NewCluster(config ClusterConfiguration, timeNow func() time.Time, dbm *gorp.DbMap, fillLiquidConnections bool) (c *Cluster, errs errext.ErrorSet) {
	c = &Cluster{
		Config:            config,
		LiquidConnections: make(map[db.ServiceType]*LiquidConnection),
		DB:                dbm,
	}

	// instantiate discovery plugin
	var err error
	c.DiscoveryPlugin, err = NewDiscoveryPlugin(config.Discovery)
	if err != nil {
		errs.Addf("setup for discovery method %s failed: %w", config.Discovery.Method, err)
	}

	// Create mail templates
	if mailConfig, ok := c.Config.MailNotifications.Unpack(); ok {
		err := mailConfig.Templates.ConfirmedCommitments.Compile()
		if err != nil {
			errs.Addf("could not parse confirmation mail template: %w", err)
		}
		err = mailConfig.Templates.ExpiringCommitments.Compile()
		if err != nil {
			errs.Addf("could not parse expiration mail template: %w", err)
		}
	}

	if !fillLiquidConnections {
		return c, errs
	}

	// fill LiquidConnection map
	for serviceType, l := range config.Liquids {
		connection := MakeLiquidConnection(l, serviceType, timeNow, dbm)
		c.LiquidConnections[serviceType] = &connection
	}
	return c, errs
}

// Connect calls Init() on the DiscoveryPlugin and LiquidConnections.
//
// It also loads the QuotaOverrides for this cluster, if configured.
// We also validate if Config.ResourceBehavior[].ScalesWith refers to existing resources.
//
// We cannot do any of this earlier because we only know all resources after
// calling Init() on all LiquidConnections.
func (c *Cluster) Connect(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (errs errext.ErrorSet) {
	// initialize discovery plugin
	err := c.DiscoveryPlugin.Init(ctx, provider, eo)
	if err != nil {
		errs.Addf("failed to initialize discovery method: %w", util.UnpackError(err))
	}

	if len(c.LiquidConnections) == 0 {
		return errs
	}

	// initialize liquid connections
	serviceTypes := slices.Sorted(maps.Keys(c.LiquidConnections))
	for _, serviceType := range serviceTypes {
		conn := c.LiquidConnections[serviceType]
		err := conn.Init(ctx, provider, eo)
		if err != nil {
			errs.Addf("failed to initialize service %s: %w", serviceType, util.UnpackError(err))
		}
	}

	// delete all orphaned cluster_services
	// respective cluster_resources, cluster_az_resources and cluster_rates are handled by delete-cascade
	_, err = c.DB.Exec(`DELETE FROM cluster_services WHERE type != ALL($1)`, pq.Array(serviceTypes))
	if err != nil {
		errs.Addf("failed orphaned cluster_services cleanup: %w", err)
	}

	return errs
}

// ServiceTypesInAlphabeticalOrder can be used when service types need to be
// iterated over in a stable order (mostly to ensure deterministic behavior in unit tests).
func (c *Cluster) ServiceTypesInAlphabeticalOrder() []db.ServiceType {
	if len(c.LiquidConnections) == 0 {
		return serviceTypesInAlphabeticalOrderDB(c.DB)
	}
	result := make([]db.ServiceType, 0, len(c.LiquidConnections))
	for serviceType, connection := range c.LiquidConnections {
		if connection != nil { // defense in depth (nil values should never be stored in the map anyway)
			result = append(result, serviceType)
		}
	}
	slices.Sort(result)
	return result
}

// HasService checks whether the given service is enabled in this cluster.
func (c *Cluster) HasService(serviceType db.ServiceType) bool {
	if len(c.LiquidConnections) == 0 {
		return hasServiceDB(c.DB, serviceType)
	}
	return c.LiquidConnections[serviceType] != nil
}

// InfoForService returns the ServiceInfo for a given service. If the service does not
// exist, an empty ServiceInfo is returned.
func (c *Cluster) InfoForService(serviceType db.ServiceType) liquid.ServiceInfo {
	if len(c.LiquidConnections) == 0 {
		return infoForServiceDB(c.DB, serviceType)
	}
	connection := c.LiquidConnections[serviceType]
	if connection == nil {
		return liquid.ServiceInfo{}
	}
	return connection.ServiceInfo()
}

// HasResource checks whether the given service is enabled in this cluster and
// whether it advertises the given resource.
func (c *Cluster) HasResource(serviceType db.ServiceType, resourceName liquid.ResourceName) bool {
	if len(c.LiquidConnections) == 0 {
		return hasResourceDB(c.DB, serviceType, resourceName)
	}
	connection := c.LiquidConnections[serviceType]
	if connection == nil {
		return false
	}
	_, exists := connection.ServiceInfo().Resources[resourceName]
	return exists
}

// InfoForResource returns the ResourceInfo for a given service and resource. If the service or
// resource does not exist, an empty ResourceInfo (with .Unit == UnitNone and
// .Category == "") is returned.
func (c *Cluster) InfoForResource(serviceType db.ServiceType, resourceName liquid.ResourceName) liquid.ResourceInfo {
	if len(c.LiquidConnections) == 0 {
		return infoForResourceDB(c.DB, serviceType, resourceName)
	}
	connection := c.LiquidConnections[serviceType]
	if connection == nil {
		return liquid.ResourceInfo{Unit: limes.UnitNone}
	}
	resInfo, exists := connection.ServiceInfo().Resources[resourceName]
	if !exists {
		return liquid.ResourceInfo{Unit: limes.UnitNone}
	}
	return resInfo
}

// ResourcesForService returns a list of all resources for the given service type.
// If the service does not exist, an empty list is returned.
func (c *Cluster) ResourcesForService(serviceType db.ServiceType) map[liquid.ResourceName]liquid.ResourceInfo {
	if len(c.LiquidConnections) == 0 {
		return resourcesForServiceDB(c.DB, serviceType)
	}
	connection := c.LiquidConnections[serviceType]
	if connection == nil {
		return make(map[liquid.ResourceName]liquid.ResourceInfo)
	}
	return connection.ServiceInfo().Resources
}

// This is used to reach ConfigSets stored inside type ServiceConfiguration.
func (c *Cluster) configForService(serviceType db.ServiceType) LiquidConfiguration {
	for st, l := range c.Config.Liquids {
		if st == serviceType {
			return l
		}
	}
	return LiquidConfiguration{}
}

// CommitmentBehaviorForResource returns the CommitmentBehavior for the given resource in the given service.
func (c *Cluster) CommitmentBehaviorForResource(serviceType db.ServiceType, resourceName liquid.ResourceName) CommitmentBehavior {
	return c.configForService(serviceType).CommitmentBehaviorPerResource.Pick(resourceName).UnwrapOr(CommitmentBehavior{})
}

// BehaviorForResource returns the ResourceBehavior for the given resource in the given service.
func (c *Cluster) BehaviorForResource(serviceType db.ServiceType, resourceName liquid.ResourceName) ResourceBehavior {
	// default behavior
	result := ResourceBehavior{
		IdentityInV1API: ResourceRef{
			// NOTE: This is the only place where these particular cross-type casts are allowed.
			ServiceType: limes.ServiceType(serviceType),
			Name:        limesresources.ResourceName(resourceName),
		},
	}

	// check for specific behavior
	fullName := string(serviceType) + "/" + string(resourceName)
	for _, behavior := range c.Config.ResourceBehaviors {
		if behavior.FullResourceNameRx.MatchString(fullName) {
			result.Merge(behavior, fullName)
		}
	}

	return result
}

// BehaviorForRate returns the RateBehavior for the given rate in
// the given scope.
func (c *Cluster) BehaviorForRate(serviceType db.ServiceType, rateName liquid.RateName) RateBehavior {
	// default behavior
	result := RateBehavior{
		IdentityInV1API: RateRef{
			// NOTE: This is the only place where these particular cross-type casts are allowed.
			ServiceType: limes.ServiceType(serviceType),
			Name:        limesrates.RateName(rateName),
		},
	}

	// check for specific behavior
	fullName := string(serviceType) + "/" + string(rateName)
	for _, behavior := range c.Config.RateBehaviors {
		if behavior.FullRateNameRx.MatchString(fullName) {
			result.Merge(behavior, fullName)
		}
	}

	return result
}

// QuotaDistributionConfigForResource returns the QuotaDistributionConfiguration
// for the given resource.
func (c *Cluster) QuotaDistributionConfigForResource(serviceType db.ServiceType, resourceName liquid.ResourceName) QuotaDistributionConfiguration {
	// check for specific behavior
	fullName := string(serviceType) + "/" + string(resourceName)
	for _, dmCfg := range c.Config.QuotaDistributionConfigs {
		if dmCfg.FullResourceNameRx.MatchString(fullName) {
			return dmCfg
		}
	}

	// default behavior: do not give out any quota except for existing usage or with explicit quota override
	return QuotaDistributionConfiguration{
		Model: limesresources.AutogrowQuotaDistribution,
		Autogrow: Some(AutogrowQuotaDistributionConfiguration{
			AllowQuotaOvercommitUntilAllocatedPercent: 0,
			ProjectBaseQuota:         0,
			GrowthMultiplier:         1.0,
			GrowthMinimum:            0,
			UsageDataRetentionPeriod: util.MarshalableTimeDuration(1 * time.Second),
		}),
	}
}

// HasUsageForRate checks whether the given service is enabled in this cluster and
// whether it scrapes usage for the given rate.
func (c *Cluster) HasUsageForRate(serviceType db.ServiceType, rateName liquid.RateName) bool {
	if len(c.LiquidConnections) == 0 {
		return HasUsageForRateDB(c.DB, serviceType, rateName)
	}
	connection := c.LiquidConnections[serviceType]
	if connection == nil {
		return false
	}
	rateInfo, exists := connection.ServiceInfo().Rates[rateName]
	return exists && rateInfo.HasUsage
}

// InfoForRate finds the connection for the given serviceType and finds within that
// connection the RateInfo for the given rateName. If the service or rate does not
// exist, an empty RateInfo (with .Unit == UnitNone) is returned. Note that this
// only returns non-empty RateInfos for rates where a usage is reported. There
// may be rates that only have a limit, as defined in the ClusterConfiguration.
func (c *Cluster) InfoForRate(serviceType db.ServiceType, rateName liquid.RateName) liquid.RateInfo {
	if len(c.LiquidConnections) == 0 {
		return InfoForRateDB(c.DB, serviceType, rateName)
	}
	connection := c.LiquidConnections[serviceType]
	if connection == nil {
		return liquid.RateInfo{Unit: limes.UnitNone}
	}
	info, exists := connection.ServiceInfo().Rates[rateName]
	if exists {
		return info
	}
	return liquid.RateInfo{Unit: limes.UnitNone}
}

// RatesForService returns a list of all rates for the given service type.
// If the service does not exist, an empty list is returned.
func (c *Cluster) RatesForService(serviceType db.ServiceType) map[liquid.RateName]liquid.RateInfo {
	if len(c.LiquidConnections) == 0 {
		return ratesForServiceDB(c.DB, serviceType)
	}
	connection := c.LiquidConnections[serviceType]
	if connection == nil {
		return make(map[liquid.RateName]liquid.RateInfo)
	}
	return connection.ServiceInfo().Rates
}

// serviceTypesInAlphabeticalOrderDB is a helper function for the respective cluster method to query this data from the DB.
func serviceTypesInAlphabeticalOrderDB(dbm *gorp.DbMap) (result []db.ServiceType) {
	_, err := dbm.Select(&result, `SELECT type FROM cluster_services ORDER BY type`)
	if err != nil {
		return result
	}
	return result
}

// hasServiceDB is a helper function for the respective cluster method to query this data from the DB.
func hasServiceDB(dbm *gorp.DbMap, serviceType db.ServiceType) bool {
	var dbServiceType db.ServiceType
	err := dbm.SelectOne(&dbServiceType, `SELECT type FROM cluster_services WHERE type = $1`, serviceType)
	return err == nil
}

// infoForServiceDB is a helper function for the respective cluster method to query this data from the DB.
func infoForServiceDB(dbm *gorp.DbMap, serviceType db.ServiceType) liquid.ServiceInfo {
	serviceInfo, err := readServiceInfoFromDB(serviceType, dbm)
	if err != nil {
		return liquid.ServiceInfo{}
	}
	return serviceInfo
}

// hasResourceDB is a helper function for the respective cluster method to query this data from the DB.
func hasResourceDB(dbm *gorp.DbMap, serviceType db.ServiceType, resourceName liquid.ResourceName) bool {
	name, err := dbm.SelectStr(`
		SELECT cr.name FROM cluster_resources as cr
		JOIN cluster_services as cs on cr.service_id = cs.id
		WHERE cr.name = $1 AND cs.type = $2`, resourceName, serviceType)
	if err != nil || name == "" {
		return false
	}
	return true
}

// infoForResourceDB is a helper function for the respective cluster method to query this data from the DB.
func infoForResourceDB(dbm *gorp.DbMap, serviceType db.ServiceType, resourceName liquid.ResourceName) liquid.ResourceInfo {
	var info db.ClusterResource
	err := dbm.SelectOne(&info, `
		SELECT cr.* FROM cluster_resources as cr
		JOIN cluster_services as cs on cr.service_id = cs.id
		WHERE cr.name = $1 AND cs.type = $2`, resourceName, serviceType)
	if err != nil {
		return liquid.ResourceInfo{Unit: limes.UnitNone}
	}
	return liquid.ResourceInfo{
		Unit:                info.Unit,
		Topology:            info.Topology,
		HasCapacity:         info.HasCapacity,
		NeedsResourceDemand: info.NeedsResourceDemand,
		HasQuota:            info.HasQuota,
		Attributes:          []byte(info.AttributesJSON),
	}
}

// resourcesForServiceDB is a helper function for the respective cluster method to query this data from the DB.
func resourcesForServiceDB(dbm *gorp.DbMap, serviceType db.ServiceType) map[liquid.ResourceName]liquid.ResourceInfo {
	result := make(map[liquid.ResourceName]liquid.ResourceInfo)
	var dbResources []db.ClusterResource
	_, err := dbm.Select(&dbResources, `
		SELECT cr.* FROM cluster_resources as cr
		JOIN cluster_services as cs on cr.service_id = cs.id
		WHERE cs.type = $1`, serviceType)
	if err != nil {
		return result
	}
	for _, dbResource := range dbResources {
		result[dbResource.Name] = liquid.ResourceInfo{
			Unit:                dbResource.Unit,
			Topology:            dbResource.Topology,
			HasCapacity:         dbResource.HasCapacity,
			NeedsResourceDemand: dbResource.NeedsResourceDemand,
			HasQuota:            dbResource.HasQuota,
			Attributes:          []byte(dbResource.AttributesJSON),
		}
	}
	return result
}

// HasUsageForRateDB is a helper function for the respective cluster method to query this data from the DB.
func HasUsageForRateDB(dbm *gorp.DbMap, serviceType db.ServiceType, rateName liquid.RateName) bool {
	info := InfoForRateDB(dbm, serviceType, rateName)
	return info.HasUsage
}

// InfoForRateDB is a helper function for the respective cluster method to query this data from the DB.
func InfoForRateDB(dbm *gorp.DbMap, serviceType db.ServiceType, rateName liquid.RateName) liquid.RateInfo {
	var info db.ClusterRate
	err := dbm.SelectOne(&info, `
		SELECT cr.* FROM cluster_rates as cr
		JOIN cluster_services as cs on cr.service_id = cs.id
		WHERE cr.name = $1 AND cs.type = $2`, rateName, serviceType)
	if err != nil {
		return liquid.RateInfo{Unit: limes.UnitNone}
	}
	return liquid.RateInfo{
		Unit:     info.Unit,
		Topology: info.Topology,
		HasUsage: info.HasUsage,
	}
}

// resourcesForServiceDB is a helper function for the respective cluster method to query this data from the DB.
func ratesForServiceDB(dbm *gorp.DbMap, serviceType db.ServiceType) map[liquid.RateName]liquid.RateInfo {
	result := make(map[liquid.RateName]liquid.RateInfo)
	var dbRates []db.ClusterRate
	_, err := dbm.Select(&dbRates, `
		SELECT cr.* FROM cluster_rates as cr
		JOIN cluster_services as cs on cr.service_id = cs.id
		WHERE cs.type = $1`, serviceType)
	if err != nil {
		return result
	}
	for _, dbRate := range dbRates {
		result[dbRate.Name] = liquid.RateInfo{
			Unit:     dbRate.Unit,
			Topology: dbRate.Topology,
			HasUsage: dbRate.HasUsage,
		}
	}
	return result
}

// SaveServiceInfoToDB ensures consistency of tables cluster_services, cluster_resources and cluster_rates
// with the given serviceInfo. It is called whenever the LiquidVersion changes during Scrape or ScrapeCapacity
// or on Init from the collect-task. It does not have the LiquidConnection as receiverType, so that it can be
// reused from the testSetup to create DB entries.
func SaveServiceInfoToDB(timeNow func() time.Time, serviceType db.ServiceType, serviceInfo liquid.ServiceInfo, dbm *gorp.DbMap) (err error) {
	// do the whole consistency check for one connection in a transaction to avoid inconsistent DB state
	tx, err := dbm.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// collect existing cluster_service and the wanted cluster_service
	var dbServices []db.ClusterService
	_, err = tx.Select(&dbServices, `SELECT * FROM cluster_services WHERE type = $1`, serviceType)
	if err != nil {
		return fmt.Errorf("cannot inspect existing cluster_service %s: %w", serviceType, err)
	}
	var wantedServices = []db.ServiceType{serviceType}

	// do update for cluster_service (as set update, for convenience)
	cmf, err := util.RenderMapToJSON("capacity_metric_families", serviceInfo.CapacityMetricFamilies)
	if err != nil {
		return fmt.Errorf("cannot serialize CapacityMetricFamilies for %s: %w", serviceType, err)
	}
	umf, err := util.RenderMapToJSON("usage_metric_families", serviceInfo.UsageMetricFamilies)
	if err != nil {
		return fmt.Errorf("cannot serialize UsageMetricFamilies for %s: %w", serviceType, err)
	}
	serviceUpdate := db.SetUpdate[db.ClusterService, db.ServiceType]{
		ExistingRecords: dbServices,
		WantedKeys:      wantedServices,
		KeyForRecord: func(service db.ClusterService) db.ServiceType {
			return service.Type
		},
		Create: func(serviceType db.ServiceType) (db.ClusterService, error) {
			return db.ClusterService{
				NextScrapeAt:                    timeNow(),
				Type:                            serviceType,
				LiquidVersion:                   serviceInfo.Version,
				CapacityMetricFamiliesJSON:      cmf,
				UsageMetricFamiliesJSON:         umf,
				UsageReportNeedsProjectMetadata: serviceInfo.UsageReportNeedsProjectMetadata,
				QuotaUpdateNeedsProjectMetadata: serviceInfo.QuotaUpdateNeedsProjectMetadata,
			}, nil
		},
		Update: func(service *db.ClusterService) (err error) {
			service.LiquidVersion = serviceInfo.Version
			service.CapacityMetricFamiliesJSON = cmf
			service.UsageMetricFamiliesJSON = umf
			service.UsageReportNeedsProjectMetadata = serviceInfo.UsageReportNeedsProjectMetadata
			service.QuotaUpdateNeedsProjectMetadata = serviceInfo.QuotaUpdateNeedsProjectMetadata
			return nil
		},
	}
	dbServices, err = serviceUpdate.Execute(tx)
	if err != nil {
		return fmt.Errorf("update cluster_services failed for %s: %w", serviceType, err)
	}

	// collect existing cluster_resources and the wanted cluster_resources
	var dbResources []db.ClusterResource
	_, err = tx.Select(&dbResources, `SELECT * FROM cluster_resources WHERE service_id = $1`, dbServices[0].ID)
	if err != nil {
		return fmt.Errorf("cannot inspect existing cluster resources for %s: %w", serviceType, err)
	}
	wantedResources := slices.Sorted(maps.Keys(serviceInfo.Resources))

	// do update for cluster_resources
	resourceUpdate := db.SetUpdate[db.ClusterResource, liquid.ResourceName]{
		ExistingRecords: dbResources,
		WantedKeys:      wantedResources,
		KeyForRecord: func(resource db.ClusterResource) liquid.ResourceName {
			return resource.Name
		},
		Create: func(resourceName liquid.ResourceName) (db.ClusterResource, error) {
			return db.ClusterResource{
				ServiceID:           dbServices[0].ID,
				Name:                resourceName,
				LiquidVersion:       serviceInfo.Version,
				Unit:                serviceInfo.Resources[resourceName].Unit,
				Topology:            serviceInfo.Resources[resourceName].Topology,
				HasCapacity:         serviceInfo.Resources[resourceName].HasCapacity,
				NeedsResourceDemand: serviceInfo.Resources[resourceName].NeedsResourceDemand,
				HasQuota:            serviceInfo.Resources[resourceName].HasQuota,
				AttributesJSON:      string(serviceInfo.Resources[resourceName].Attributes),
			}, nil
		},
		Update: func(res *db.ClusterResource) (err error) {
			res.LiquidVersion = serviceInfo.Version
			res.Unit = serviceInfo.Resources[res.Name].Unit
			res.Topology = serviceInfo.Resources[res.Name].Topology
			res.HasCapacity = serviceInfo.Resources[res.Name].HasCapacity
			res.NeedsResourceDemand = serviceInfo.Resources[res.Name].NeedsResourceDemand
			res.HasQuota = serviceInfo.Resources[res.Name].HasQuota
			res.AttributesJSON = string(serviceInfo.Resources[res.Name].Attributes)
			return nil
		},
	}
	_, err = resourceUpdate.Execute(tx)
	if err != nil {
		return err
	}

	// collect existing cluster_rates and the wanted cluster_rates
	var dbRates []db.ClusterRate
	_, err = tx.Select(&dbRates, `SELECT * FROM cluster_rates WHERE service_id = $1`, dbServices[0].ID)
	if err != nil {
		return fmt.Errorf("cannot inspect existing cluster rates for %s: %w", serviceType, err)
	}
	wantedRates := slices.Sorted(maps.Keys(serviceInfo.Rates))

	// do update for cluster_resources
	rateUpdate := db.SetUpdate[db.ClusterRate, liquid.RateName]{
		ExistingRecords: dbRates,
		WantedKeys:      wantedRates,
		KeyForRecord: func(rate db.ClusterRate) liquid.RateName {
			return rate.Name
		},
		Create: func(rateName liquid.RateName) (db.ClusterRate, error) {
			return db.ClusterRate{
				ServiceID:     dbServices[0].ID,
				Name:          rateName,
				LiquidVersion: serviceInfo.Version,
				Unit:          serviceInfo.Rates[rateName].Unit,
				Topology:      serviceInfo.Rates[rateName].Topology,
				HasUsage:      serviceInfo.Rates[rateName].HasUsage,
			}, nil
		},
		Update: func(rate *db.ClusterRate) (err error) {
			rate.LiquidVersion = serviceInfo.Version
			rate.Unit = serviceInfo.Rates[rate.Name].Unit
			rate.Topology = serviceInfo.Rates[rate.Name].Topology
			rate.HasUsage = serviceInfo.Rates[rate.Name].HasUsage
			return nil
		},
	}
	_, err = rateUpdate.Execute(tx)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// readServiceInfoFromDB reads the complete ServiceInfo from the database a) as fallback in case the Liquid
// is not reachable on startup or b) as single source for tasks outside of collect to obtain a complete
// ServiceInfo. For b) the call to readServiceInfoFromDB is done from the Cluster, all access which does not
// have a handle to a LiquidConnection (all non-collect-tasks) should utilize the Cluster methods. The
// properties of the ServiceInfo are read individually per entity instead of an SQL-join, so that possible
// inconsistencies of the database can be reported more precisely.
func readServiceInfoFromDB(serviceType db.ServiceType, dbm *gorp.DbMap) (serviceInfo liquid.ServiceInfo, err error) {
	var dbServices []db.ClusterService
	_, err = dbm.Select(&dbServices, `SELECT * FROM cluster_services WHERE type = $1`, serviceType)
	if err != nil {
		return liquid.ServiceInfo{}, fmt.Errorf("cannot inspect existing cluster_service %s: %w", serviceType, err)
	}
	// more than one is not possible due to the key/unique constraint
	if len(dbServices) == 0 {
		return liquid.ServiceInfo{}, fmt.Errorf("no cluster_service found for %s", serviceType)
	}

	var dbResources []db.ClusterResource
	_, err = dbm.Select(&dbResources, `SELECT * FROM cluster_resources WHERE service_id = $1`, dbServices[0].ID)
	if err != nil {
		return liquid.ServiceInfo{}, fmt.Errorf("cannot inspect existing cluster resources for %s: %w", serviceType, err)
	}

	var dbRates []db.ClusterRate
	_, err = dbm.Select(&dbRates, `SELECT * FROM cluster_rates WHERE service_id = $1`, dbServices[0].ID)
	if err != nil {
		return liquid.ServiceInfo{}, fmt.Errorf("cannot inspect existing cluster rates for %s: %w", serviceType, err)
	}

	// construct LiquidServiceInfo
	var resources = make(map[liquid.ResourceName]liquid.ResourceInfo, len(dbResources))
	for _, dbResource := range dbResources {
		if dbResource.LiquidVersion != dbServices[0].LiquidVersion {
			return liquid.ServiceInfo{}, fmt.Errorf("cluster_resource %s has a different LiquidVersion %d than the cluster_service %s with LiquidVersion %d", dbResource.Name, dbResource.LiquidVersion, serviceType, dbServices[0].LiquidVersion)
		}
		resources[dbResource.Name] = liquid.ResourceInfo{
			Unit:                dbResource.Unit,
			Topology:            dbResource.Topology,
			HasCapacity:         dbResource.HasCapacity,
			NeedsResourceDemand: dbResource.NeedsResourceDemand,
			HasQuota:            dbResource.HasQuota,
			Attributes:          []byte(dbResource.AttributesJSON),
		}
	}
	var rates = make(map[liquid.RateName]liquid.RateInfo, len(dbRates))
	for _, dbRate := range dbRates {
		if dbRate.LiquidVersion != dbServices[0].LiquidVersion {
			return liquid.ServiceInfo{}, fmt.Errorf("cluster_rate %s has a different LiquidVersion %d than the cluster_service %s with LiquidVersion %d", dbRate.Name, dbRate.LiquidVersion, serviceType, dbServices[0].LiquidVersion)
		}
		rates[dbRate.Name] = liquid.RateInfo{
			Unit:     dbRate.Unit,
			Topology: dbRate.Topology,
			HasUsage: dbRate.HasUsage,
		}
	}
	capacityMetricFamilies, err := util.JSONToAny[map[liquid.MetricName]liquid.MetricFamilyInfo](dbServices[0].CapacityMetricFamiliesJSON, "capacity_metric_families")
	if err != nil {
		return liquid.ServiceInfo{}, fmt.Errorf("cannot deserialize capacityMetricFamiliesJSON for %s: %w", serviceType, err)
	}
	usageMetricFamilies, err := util.JSONToAny[map[liquid.MetricName]liquid.MetricFamilyInfo](dbServices[0].UsageMetricFamiliesJSON, "usage_metric_families")
	if err != nil {
		return liquid.ServiceInfo{}, fmt.Errorf("cannot deserialize usageMetricsFamiliesJSON for %s: %w", serviceType, err)
	}
	serviceInfo = liquid.ServiceInfo{
		Version:                         dbServices[0].LiquidVersion,
		Resources:                       resources,
		Rates:                           rates,
		CapacityMetricFamilies:          capacityMetricFamilies,
		UsageMetricFamilies:             usageMetricFamilies,
		UsageReportNeedsProjectMetadata: dbServices[0].UsageReportNeedsProjectMetadata,
		QuotaUpdateNeedsProjectMetadata: dbServices[0].QuotaUpdateNeedsProjectMetadata,
	}

	return serviceInfo, nil
}
