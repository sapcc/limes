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
	"github.com/sapcc/go-bits/logg"
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
	// used to generate LiquidClients without LiquidConnections
	LiquidClientFactory func(db.ServiceType) (LiquidClient, error)
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
		err = mailConfig.Templates.TransferredCommitments.Compile()
		if err != nil {
			errs.Addf("could not parse transfer mail template: %w", err)
		}
	}

	if !fillLiquidConnections {
		return c, errs
	}

	// fill LiquidConnection map
	for serviceType, l := range config.Liquids {
		connection := MakeLiquidConnection(l, serviceType, config.AvailabilityZones, l.RateLimits, timeNow, dbm)
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
func (c *Cluster) Connect(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, liquidClientFactory func(db.ServiceType) (LiquidClient, error)) (errs errext.ErrorSet) {
	// save factory for possible later use
	c.LiquidClientFactory = liquidClientFactory

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
		client, err := liquidClientFactory(serviceType)
		if err != nil {
			errs.Addf("failed to initialize service %s: %w", serviceType, util.UnpackError(err))
			continue
		}
		err = conn.Init(ctx, client)
		if err != nil {
			errs.Addf("failed to initialize service %s: %w", serviceType, util.UnpackError(err))
		}
	}

	// delete all orphaned services
	// respective resources, az_resources and rates are handled by delete-cascade
	_, err = c.DB.Exec(`DELETE FROM services WHERE type != ALL($1)`, pq.Array(serviceTypes))
	if err != nil {
		errs.Addf("failed orphaned services cleanup: %w", err)
	}

	return errs
}

// AllServiceInfos returns a map of all ServiceInfos for all services in this cluster.
// Its output is the basis to use the convenience methods below to get certain properties of the services
// configuration. In order to be usable efficiently, it is recommended to call this method only once per API,
// so that the database fallback is only done once.
func (c *Cluster) AllServiceInfos() (map[db.ServiceType]liquid.ServiceInfo, error) {
	if len(c.LiquidConnections) == 0 {
		return readServiceInfoFromDB(c.DB, None[db.ServiceType]())
	}
	result := make(map[db.ServiceType]liquid.ServiceInfo, len(c.LiquidConnections))
	for serviceType, connection := range c.LiquidConnections {
		if connection != nil { // defense in depth (nil values should never be stored in the map anyway)
			result[serviceType] = connection.ServiceInfo()
		}
	}
	return result, nil
}

// InfoForService returns the ServiceInfo for a given service. If the service does not
// exist, None[liquid.ServiceInfo] is returned. It should be used instead of Cluster.AllServiceInfos
// when only one service is needed, to avoid the overhead of loading all services.
func (c *Cluster) InfoForService(serviceType db.ServiceType) (Option[liquid.ServiceInfo], error) {
	if len(c.LiquidConnections) == 0 {
		serviceInfos, err := readServiceInfoFromDB(c.DB, Some(serviceType))
		if err != nil {
			return None[liquid.ServiceInfo](), err
		}
		return Some(serviceInfos[serviceType]), nil
	}
	connection := c.LiquidConnections[serviceType]
	if connection == nil {
		return None[liquid.ServiceInfo](), nil
	}
	return Some(connection.ServiceInfo()), nil
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

// BehaviorForResourceLocation is a shorthand for BehaviorForResource using an AZResourceLocation.
func (c *Cluster) BehaviorForResourceLocation(loc AZResourceLocation) ResourceBehavior {
	return c.BehaviorForResource(loc.ServiceType, loc.ResourceName)
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

////////////////////////////////////////////////////////////////////////////////
// Utility functions for working with ServiceInfos and Resources

// HasService checks whether the given service is enabled in this cluster.
func HasService(serviceInfos map[db.ServiceType]liquid.ServiceInfo, serviceType db.ServiceType) bool {
	_, exists := serviceInfos[serviceType]
	return exists
}

// InfoForService returns the ServiceInfo for the given service type.
// If the service does not exist, an empty ServiceInfo is returned.
func InfoForService(serviceInfos map[db.ServiceType]liquid.ServiceInfo, serviceType db.ServiceType) liquid.ServiceInfo {
	serviceInfo, exists := serviceInfos[serviceType]
	if !exists {
		return liquid.ServiceInfo{}
	}
	return serviceInfo
}

// HasResource checks whether the given service is enabled in this cluster and
// whether it advertises the given resource.
func HasResource(serviceInfos map[db.ServiceType]liquid.ServiceInfo, serviceType db.ServiceType, resourceName liquid.ResourceName) bool {
	serviceInfo, exists := serviceInfos[serviceType]
	if !exists {
		return false
	}
	_, exists = serviceInfo.Resources[resourceName]
	return exists
}

// InfoForResource returns the ResourceInfo for a given service and resource. If the service
// does not exist, an empty ResourceInfo (with .Unit == UnitNone and .Category == "") is returned.
func InfoForResource(serviceInfo liquid.ServiceInfo, resourceName liquid.ResourceName) liquid.ResourceInfo {
	resInfo, exists := serviceInfo.Resources[resourceName]
	if !exists {
		return liquid.ResourceInfo{Unit: limes.UnitNone}
	}
	return resInfo
}

// HasUsageForRate checks whether the given service is enabled in this cluster and
// whether it scrapes usage for the given rate.
func HasUsageForRate(serviceInfos map[db.ServiceType]liquid.ServiceInfo, serviceType db.ServiceType, rateName liquid.RateName) bool {
	serviceInfo, exists := serviceInfos[serviceType]
	if !exists {
		return false
	}
	rateInfo, exists := serviceInfo.Rates[rateName]
	return exists && rateInfo.HasUsage
}

// InfoForRate finds the connection for the given serviceType and finds within that
// connection the RateInfo for the given rateName. If the service or rate does not
// exist, an empty RateInfo (with .Unit == UnitNone) is returned. Note that this
// only returns non-empty RateInfos for rates where a usage is reported. There
// may be rates that only have a limit, as defined in the ClusterConfiguration.
func InfoForRate(serviceInfos map[db.ServiceType]liquid.ServiceInfo, serviceType db.ServiceType, rateName liquid.RateName) liquid.RateInfo {
	serviceInfo, exists := serviceInfos[serviceType]
	if !exists {
		return liquid.RateInfo{Unit: limes.UnitNone}
	}
	rateInfo, exists := serviceInfo.Rates[rateName]
	if !exists {
		return liquid.RateInfo{Unit: limes.UnitNone}
	}
	return rateInfo
}

// RatesForService returns a list of all rates for the given service type.
// If the service does not exist, an empty list is returned.
// If an error occurs during db lookup, the error is returned.
func RatesForService(serviceInfos map[db.ServiceType]liquid.ServiceInfo, serviceType db.ServiceType) map[liquid.RateName]liquid.RateInfo {
	serviceInfo, exists := serviceInfos[serviceType]
	if !exists {
		return make(map[liquid.RateName]liquid.RateInfo)
	}
	return serviceInfo.Rates
}

////////////////////////////////////////////////////////////////////////////////
// Utility functions for working with ServiceInfo and DB

// SaveServiceInfoToDB ensures consistency of tables services, resources, az_resources
// and rates with the given serviceInfo. It is called whenever the LiquidVersion changes during Scrape
// or ScrapeCapacity or on Init from the collect-task. It does not have the LiquidConnection as receiverType,
// so that it can be reused from the testSetup to create DB entries.
func SaveServiceInfoToDB(serviceType db.ServiceType, serviceInfo liquid.ServiceInfo, availabilityZones []limes.AvailabilityZone, rateLimits ServiceRateLimitConfiguration, timeNow time.Time, dbm *gorp.DbMap) (srv db.Service, err error) {
	// do the whole consistency check for one connection in a transaction to avoid inconsistent DB state
	tx, err := dbm.Begin()
	if err != nil {
		return srv, err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// collect existing service and the wanted service
	var dbServices []db.Service
	_, err = tx.Select(&dbServices, `SELECT * FROM services WHERE type = $1`, serviceType)
	if err != nil {
		return srv, fmt.Errorf("cannot inspect existing service %s: %w", serviceType, err)
	}
	var wantedServices = []db.ServiceType{serviceType}

	// do update for service (as set update, for convenience)
	cmf, err := util.RenderMapToJSON("capacity_metric_families", serviceInfo.CapacityMetricFamilies)
	if err != nil {
		return srv, fmt.Errorf("cannot serialize CapacityMetricFamilies for %s: %w", serviceType, err)
	}
	umf, err := util.RenderMapToJSON("usage_metric_families", serviceInfo.UsageMetricFamilies)
	if err != nil {
		return srv, fmt.Errorf("cannot serialize UsageMetricFamilies for %s: %w", serviceType, err)
	}
	serviceUpdate := db.SetUpdate[db.Service, db.ServiceType]{
		ExistingRecords: dbServices,
		WantedKeys:      wantedServices,
		KeyForRecord: func(service db.Service) db.ServiceType {
			return service.Type
		},
		Create: func(serviceType db.ServiceType) (db.Service, error) {
			logg.Info("SaveServiceInfoToDB: creating Service %s with LiquidVersion = %d", serviceType, serviceInfo.Version)
			return db.Service{
				NextScrapeAt:                    timeNow,
				Type:                            serviceType,
				LiquidVersion:                   serviceInfo.Version,
				CapacityMetricFamiliesJSON:      cmf,
				UsageMetricFamiliesJSON:         umf,
				UsageReportNeedsProjectMetadata: serviceInfo.UsageReportNeedsProjectMetadata,
				QuotaUpdateNeedsProjectMetadata: serviceInfo.QuotaUpdateNeedsProjectMetadata,
			}, nil
		},
		Update: func(service *db.Service) (err error) {
			if service.LiquidVersion != serviceInfo.Version {
				logg.Info("SaveServiceInfoToDB: updating Service %s from LiquidVersion = %d to %d", service.Type, service.LiquidVersion, serviceInfo.Version)
			}
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
		return srv, fmt.Errorf("update services failed for %s: %w", serviceType, err)
	}
	srv = dbServices[0]

	// collect existing resources and the wanted resources
	var dbResources []db.Resource
	_, err = tx.Select(&dbResources, `SELECT * FROM resources WHERE service_id = $1`, srv.ID)
	if err != nil {
		return srv, fmt.Errorf("cannot inspect existing resources for %s: %w", serviceType, err)
	}
	wantedResources := slices.Sorted(maps.Keys(serviceInfo.Resources))

	// do update for resources
	resourceUpdate := db.SetUpdate[db.Resource, liquid.ResourceName]{
		ExistingRecords: dbResources,
		WantedKeys:      wantedResources,
		KeyForRecord: func(resource db.Resource) liquid.ResourceName {
			return resource.Name
		},
		Create: func(resourceName liquid.ResourceName) (db.Resource, error) {
			logg.Info("SaveServiceInfoToDB: creating Resource %s/%s with LiquidVersion = %d", serviceType, resourceName, serviceInfo.Version)
			return db.Resource{
				ServiceID:           srv.ID,
				Name:                resourceName,
				Path:                fmt.Sprintf("%s/%s", serviceType, resourceName),
				LiquidVersion:       serviceInfo.Version,
				Unit:                serviceInfo.Resources[resourceName].Unit,
				Topology:            serviceInfo.Resources[resourceName].Topology,
				HasCapacity:         serviceInfo.Resources[resourceName].HasCapacity,
				NeedsResourceDemand: serviceInfo.Resources[resourceName].NeedsResourceDemand,
				HasQuota:            serviceInfo.Resources[resourceName].HasQuota,
				AttributesJSON:      string(serviceInfo.Resources[resourceName].Attributes),
				HandlesCommitments:  serviceInfo.Resources[resourceName].HandlesCommitments,
			}, nil
		},
		Update: func(res *db.Resource) (err error) {
			if res.LiquidVersion != serviceInfo.Version {
				logg.Info("SaveServiceInfoToDB: updating Resource %s/%s from LiquidVersion = %d to %d", serviceType, res.Name, res.LiquidVersion, serviceInfo.Version)
			}
			res.LiquidVersion = serviceInfo.Version
			res.Unit = serviceInfo.Resources[res.Name].Unit
			res.Topology = serviceInfo.Resources[res.Name].Topology
			res.HasCapacity = serviceInfo.Resources[res.Name].HasCapacity
			res.NeedsResourceDemand = serviceInfo.Resources[res.Name].NeedsResourceDemand
			res.HasQuota = serviceInfo.Resources[res.Name].HasQuota
			res.AttributesJSON = string(serviceInfo.Resources[res.Name].Attributes)
			res.HandlesCommitments = serviceInfo.Resources[res.Name].HandlesCommitments
			return nil
		},
	}
	dbResources, err = resourceUpdate.Execute(tx)
	if err != nil {
		return srv, err
	}

	// collect existing az_resources
	var dbAZResources []db.AZResource
	_, err = tx.Select(&dbAZResources, `SELECT azr.* FROM az_resources azr JOIN resources r ON azr.resource_id = r.id WHERE r.service_id = $1`, srv.ID)
	if err != nil {
		return srv, fmt.Errorf("cannot inspect existing AZ resources for %s: %w", serviceType, err)
	}
	dbAZResourcesByResourceID := make(map[db.ResourceID][]db.AZResource)
	for _, azRes := range dbAZResources {
		dbAZResourcesByResourceID[azRes.ResourceID] = append(dbAZResourcesByResourceID[azRes.ResourceID], azRes)
	}
	// for az_resources, we need to do one SetUpdate per resource, so that we can limit the keys to just the AZs of this resource
	for _, res := range dbResources {
		// depending on the topology, we can construct the various necessary AZs
		var wantedKeys []limes.AvailabilityZone
		switch res.Topology {
		case liquid.FlatTopology:
			wantedKeys = []limes.AvailabilityZone{limes.AvailabilityZoneAny, liquid.AvailabilityZoneTotal}
		case liquid.AZAwareTopology:
			wantedKeys = []limes.AvailabilityZone{limes.AvailabilityZoneAny, liquid.AvailabilityZoneTotal, limes.AvailabilityZoneUnknown}
		default:
			wantedKeys = []limes.AvailabilityZone{liquid.AvailabilityZoneTotal, limes.AvailabilityZoneUnknown}
		}
		if res.Topology != liquid.FlatTopology {
			wantedKeys = append(wantedKeys, availabilityZones...)
			slices.Sort(wantedKeys)
		}
		setUpdate := db.SetUpdate[db.AZResource, liquid.AvailabilityZone]{
			ExistingRecords: dbAZResourcesByResourceID[res.ID],
			WantedKeys:      wantedKeys,
			KeyForRecord: func(azRes db.AZResource) liquid.AvailabilityZone {
				return azRes.AvailabilityZone
			},
			Create: func(az liquid.AvailabilityZone) (db.AZResource, error) {
				return db.AZResource{
					ResourceID:       res.ID,
					AvailabilityZone: az,
					Path:             fmt.Sprintf("%s/%s", res.Path, az),
				}, nil
			},
			Update: func(azRes *db.AZResource) error {
				// we don't know more than the existence of the AZ, so we don't update anything
				return nil
			},
		}
		_, err = setUpdate.Execute(tx)
		if err != nil {
			return srv, err
		}
	}

	// collect existing rates and the wanted rates
	var dbRates []db.Rate
	_, err = tx.Select(&dbRates, `SELECT * FROM rates WHERE service_id = $1`, srv.ID)
	if err != nil {
		return srv, fmt.Errorf("cannot inspect existing rates for %s: %w", serviceType, err)
	}
	wantedRates := slices.Collect(maps.Keys(serviceInfo.Rates))
	// extend the list of wanted rates with the rates which are configured (they may not be in the serviceInfo.Rates)
	for _, rateLimit := range rateLimits.Global {
		wantedRates = append(wantedRates, rateLimit.Name)
	}
	for _, rateLimit := range rateLimits.ProjectDefault {
		wantedRates = append(wantedRates, rateLimit.Name)
	}
	slices.Sort(wantedRates)
	wantedRates = slices.Compact(wantedRates)

	// do update for resources
	rateUpdate := db.SetUpdate[db.Rate, liquid.RateName]{
		ExistingRecords: dbRates,
		WantedKeys:      wantedRates,
		KeyForRecord: func(rate db.Rate) liquid.RateName {
			return rate.Name
		},
		Create: func(rateName liquid.RateName) (db.Rate, error) {
			return db.Rate{
				ServiceID: dbServices[0].ID,
				Name:      rateName,
			}, nil
		},
		Update: func(rate *db.Rate) (err error) {
			rate.LiquidVersion = serviceInfo.Version
			if rateInfo, exists := serviceInfo.Rates[rate.Name]; exists {
				rate.Unit = rateInfo.Unit
				rate.Topology = rateInfo.Topology
				rate.HasUsage = rateInfo.HasUsage
			} else {
				// fallbacks for rates that are only declared in the config and not announced by the liquid
				rate.Unit = liquid.UnitNone
				rate.Topology = liquid.FlatTopology
				rate.HasUsage = false
			}
			return nil
		},
	}
	_, err = rateUpdate.Execute(tx)
	if err != nil {
		return srv, err
	}

	return srv, tx.Commit()
}

// readServiceInfoFromDB reads the complete ServiceInfo from the database a) as fallback in case the Liquid
// is not reachable on startup or b) as single source for tasks outside of collect to obtain a complete
// ServiceInfo. For b) the call to readServiceInfoFromDB is done from the Cluster, all access which does not
// have a handle to a LiquidConnection (all non-collect-tasks) should utilize the Cluster methods. The
// properties of the ServiceInfo are read individually per entity instead of an SQL-join, so that possible
// inconsistencies of the database can be reported more precisely.
func readServiceInfoFromDB(dbm *gorp.DbMap, serviceTypeOpt Option[db.ServiceType]) (map[db.ServiceType]liquid.ServiceInfo, error) {
	serviceType, applyFilter := serviceTypeOpt.Unpack()
	var (
		dbServices      []db.Service
		err             error
		serviceInfos    = make(map[db.ServiceType]liquid.ServiceInfo)
		serviceTypeByID = make(map[db.ServiceID]db.ServiceType)
	)

	if applyFilter {
		_, err = dbm.Select(&dbServices, `SELECT * FROM services WHERE type = $1`, serviceType)
	} else {
		_, err = dbm.Select(&dbServices, `SELECT * FROM services`)
	}
	if err != nil {
		return serviceInfos, fmt.Errorf("cannot inspect existing service %s: %w", serviceType, err)
	}
	// more than one is not possible due to the key/unique constraint, when filter is given
	if len(dbServices) == 0 && applyFilter {
		return serviceInfos, fmt.Errorf("no service found for %s", serviceType)
	}

	for _, dbService := range dbServices {
		capacityMetricFamilies, err := util.JSONToAny[map[liquid.MetricName]liquid.MetricFamilyInfo](dbService.CapacityMetricFamiliesJSON, "capacity_metric_families")
		if err != nil {
			return serviceInfos, fmt.Errorf("cannot deserialize capacityMetricFamiliesJSON for %s: %w", serviceType, err)
		}
		usageMetricFamilies, err := util.JSONToAny[map[liquid.MetricName]liquid.MetricFamilyInfo](dbService.UsageMetricFamiliesJSON, "usage_metric_families")
		if err != nil {
			return serviceInfos, fmt.Errorf("cannot deserialize usageMetricsFamiliesJSON for %s: %w", serviceType, err)
		}
		serviceInfos[dbService.Type] = liquid.ServiceInfo{
			Version:                         dbService.LiquidVersion,
			Resources:                       make(map[liquid.ResourceName]liquid.ResourceInfo),
			Rates:                           make(map[liquid.RateName]liquid.RateInfo),
			CapacityMetricFamilies:          capacityMetricFamilies,
			UsageMetricFamilies:             usageMetricFamilies,
			UsageReportNeedsProjectMetadata: dbService.UsageReportNeedsProjectMetadata,
			QuotaUpdateNeedsProjectMetadata: dbService.QuotaUpdateNeedsProjectMetadata,
		}
		serviceTypeByID[dbService.ID] = dbService.Type
	}

	var dbResources []db.Resource
	if applyFilter {
		_, err = dbm.Select(&dbResources, `SELECT * FROM resources WHERE service_id = $1`, dbServices[0].ID)
	} else {
		_, err = dbm.Select(&dbResources, `SELECT * FROM resources`)
	}
	if err != nil {
		return serviceInfos, fmt.Errorf("cannot inspect existing resources for %s: %w", serviceType, err)
	}

	var dbRates []db.Rate
	if applyFilter {
		_, err = dbm.Select(&dbRates, `SELECT * FROM rates WHERE service_id = $1`, dbServices[0].ID)
	} else {
		_, err = dbm.Select(&dbRates, `SELECT * FROM rates`)
	}
	if err != nil {
		return serviceInfos, fmt.Errorf("cannot inspect existing rates for %s: %w", serviceType, err)
	}

	for _, dbResource := range dbResources {
		dbServiceType := serviceTypeByID[dbResource.ServiceID]
		dbServiceVersion := serviceInfos[dbServiceType].Version
		if dbResource.LiquidVersion != dbServiceVersion {
			return serviceInfos, fmt.Errorf("resource %s has a different LiquidVersion %d than the service %s with LiquidVersion %d", dbResource.Name, dbResource.LiquidVersion, dbServiceType, dbServiceVersion)
		}
		serviceInfos[dbServiceType].Resources[dbResource.Name] = liquid.ResourceInfo{
			Unit:                dbResource.Unit,
			Topology:            dbResource.Topology,
			HasCapacity:         dbResource.HasCapacity,
			NeedsResourceDemand: dbResource.NeedsResourceDemand,
			HasQuota:            dbResource.HasQuota,
			Attributes:          []byte(dbResource.AttributesJSON),
			HandlesCommitments:  dbResource.HandlesCommitments,
		}
	}
	for _, dbRate := range dbRates {
		dbServiceType := serviceTypeByID[dbRate.ServiceID]
		dbServiceVersion := serviceInfos[dbServiceType].Version
		if dbRate.LiquidVersion != dbServiceVersion {
			return serviceInfos, fmt.Errorf("resource %s has a different LiquidVersion %d than the service %s with LiquidVersion %d", dbRate.Name, dbRate.LiquidVersion, dbServiceType, dbServiceVersion)
		}
		serviceInfos[dbServiceType].Rates[dbRate.Name] = liquid.RateInfo{
			Unit:     dbRate.Unit,
			Topology: dbRate.Topology,
			HasUsage: dbRate.HasUsage,
		}
	}

	return serviceInfos, nil
}
