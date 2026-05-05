// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/lib/pq"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/gophercloudext"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"
	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/gg/options"

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
	Config          ClusterConfiguration
	DiscoveryPlugin DiscoveryPlugin
	// LiquidConnections are only filled for the collector-task.
	LiquidConnections map[db.ServiceType]*LiquidConnection
	// The ServiceInfoCache should be used to access all database entities which are
	// filled from the ServiceInfo. The LiquidConnections get a handle to this in the
	// collector so that they can trigger an immediate update to the data without waiting
	// for the notification mechanism.
	SIC *ServiceInfoCache
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
// We cannot do any of this earlier because we only know all resources after calling Init() on all LiquidConnections.
//
// The ServiceInfoCache is assembled here, because we need to use the Config in the test setup before Connect.
// A dbURL needs to be provided when c.LiquidConnections is empty, to ensure ServiceInfo stays up to date.
func (c *Cluster) Connect(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, liquidClientFactory func(db.ServiceType) (LiquidClient, error), dbURL Option[url.URL]) (errs errext.ErrorSet) {
	// save factory for possible later use
	c.LiquidClientFactory = liquidClientFactory

	// initialize discovery plugin
	err := c.DiscoveryPlugin.Init(ctx, provider, eo)
	if err != nil {
		errs.Addf("failed to initialize discovery method: %w", gophercloudext.UnpackError(err))
	}

	// initialize SIC
	c.SIC, err = NewServiceInfoCache(c.DB, dbURL)
	if err != nil {
		errs.Addf("could not create service info cache: %w", err)
		return errs
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
			errs.Addf("failed to initialize service %s: %w", serviceType, gophercloudext.UnpackError(err))
			continue
		}
		err = conn.Init(ctx, client, c.SIC)
		if errors.Is(err, ErrLeftoverCommitment) {
			// we just log this error here and ignore it, so that the startup does not fail
			// this will produce errors on every scrape subsequently (as if the collector was
			// already running) and those will trigger alerts subsequently.
			logg.Error(`failed to initialize service %s: %v`, serviceType, err)
			continue
		}
		if err != nil {
			errs.Addf("failed to initialize service %s: %w", serviceType, gophercloudext.UnpackError(err))
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

// CommitmentBehaviorForResource returns the CommitmentBehavior for the given resource in the given service.
func (c *Cluster) CommitmentBehaviorForResource(serviceType db.ServiceType, resourceName liquid.ResourceName) CommitmentBehavior {
	return c.Config.Liquids[serviceType].CommitmentBehaviorPerResource.Pick(resourceName).UnwrapOr(CommitmentBehavior{})
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
// Utility functions for working with ServiceInfo and DB

// ErrLeftoverCommitment is a custom error to define when a leftover commitment
// prevents deletion of a service, resource or az_resource.
var ErrLeftoverCommitment = errors.New("ErrLeftoverCommitment")

var deleteFuncCheckQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
			SELECT count(*)
			FROM project_commitments pc
			JOIN az_resources azr
			ON pc.az_resource_id = azr.id 
			WHERE path LIKE $1
			AND status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}, {{util.CommitmentStatusDeleted}})`))

func generateDeleteFunc[T any](dbm *gorp.DbMap, getAZResourcePathPattern func(o T) string) func(T) error {
	return func(o T) error {
		count, err := dbm.SelectInt(deleteFuncCheckQuery, getAZResourcePathPattern(o))
		if err != nil {
			return fmt.Errorf("cannot get project commitments count: %w", err)
		}
		if count > 0 {
			return ErrLeftoverCommitment
		}
		return nil
	}
}

// SaveServiceInfoToDB ensures consistency of tables services, resources, az_resources
// and rates with the given serviceInfo. It is called whenever the LiquidVersion changes during Scrape
// or ScrapeCapacity or on Init from the collect-task. It does not have the LiquidConnection as receiverType,
// so that it can be reused from the testSetup to create DB entries.
func SaveServiceInfoToDB(serviceType db.ServiceType, serviceInfo liquid.ServiceInfo, availabilityZones []limes.AvailabilityZone, rateLimits ServiceRateLimitConfiguration, timeNow time.Time, dbm *gorp.DbMap) (err error) {
	// do the whole consistency check for one connection in a transaction to avoid inconsistent DB state
	tx, err := dbm.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// collect existing service and the wanted service
	var dbServices []db.Service
	_, err = tx.Select(&dbServices, `SELECT * FROM services WHERE type = $1`, serviceType)
	if err != nil {
		return fmt.Errorf("cannot inspect existing service %s: %w", serviceType, err)
	}
	var wantedServices = []db.ServiceType{serviceType}

	// do update for service (as set update, for convenience)
	cmf, err := util.RenderMapToJSON("capacity_metric_families", serviceInfo.CapacityMetricFamilies)
	if err != nil {
		return fmt.Errorf("cannot serialize CapacityMetricFamilies for %s: %w", serviceType, err)
	}
	umf, err := util.RenderMapToJSON("usage_metric_families", serviceInfo.UsageMetricFamilies)
	if err != nil {
		return fmt.Errorf("cannot serialize UsageMetricFamilies for %s: %w", serviceType, err)
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
				NextScrapeAt:                           timeNow,
				Type:                                   serviceType,
				LiquidVersion:                          serviceInfo.Version,
				DisplayName:                            serviceInfo.DisplayName,
				CapacityMetricFamiliesJSON:             cmf,
				UsageMetricFamiliesJSON:                umf,
				UsageReportNeedsProjectMetadata:        serviceInfo.UsageReportNeedsProjectMetadata,
				QuotaUpdateNeedsProjectMetadata:        serviceInfo.QuotaUpdateNeedsProjectMetadata,
				CommitmentHandlingNeedsProjectMetadata: serviceInfo.CommitmentHandlingNeedsProjectMetadata,
			}, nil
		},
		Update: func(service *db.Service) (err error) {
			if service.LiquidVersion != serviceInfo.Version {
				logg.Info("SaveServiceInfoToDB: updating Service %s from LiquidVersion = %d to %d", service.Type, service.LiquidVersion, serviceInfo.Version)
			}
			service.LiquidVersion = serviceInfo.Version
			service.DisplayName = serviceInfo.DisplayName
			service.CapacityMetricFamiliesJSON = cmf
			service.UsageMetricFamiliesJSON = umf
			service.UsageReportNeedsProjectMetadata = serviceInfo.UsageReportNeedsProjectMetadata
			service.QuotaUpdateNeedsProjectMetadata = serviceInfo.QuotaUpdateNeedsProjectMetadata
			service.CommitmentHandlingNeedsProjectMetadata = serviceInfo.CommitmentHandlingNeedsProjectMetadata
			return nil
		},
		PreDelete: Some(generateDeleteFunc[db.Service](dbm, func(_ db.Service) string { return string(serviceType) + "/%/%" })),
	}
	dbServices, err = serviceUpdate.Execute(tx)
	if err != nil {
		return fmt.Errorf("update services failed for %s: %w", serviceType, err)
	}
	srv := dbServices[0]

	// The categories don't have a reference to the service, so we just add all categories which are new
	// and delete the one's which are unused after the resources for this service were reconciled.
	categoryByName, err := db.BuildIndexOfDBResult(tx, func(category db.Category) liquid.CategoryName { return category.Name }, `SELECT * from categories`)
	for name, categoryInfo := range serviceInfo.Categories {
		if _, exists := categoryByName[name]; !exists {
			newCategory := db.Category{Name: name, DisplayName: categoryInfo.DisplayName}
			err = tx.Insert(&newCategory)
			if err != nil {
				return fmt.Errorf("cannot insert category %s for %s: %w", name, serviceType, err)
			}
			categoryByName[name] = newCategory
		}
	}

	// collect existing resources and the wanted resources
	var dbResources []db.Resource
	_, err = tx.Select(&dbResources, `SELECT * FROM resources WHERE service_id = $1`, srv.ID)
	if err != nil {
		return fmt.Errorf("cannot inspect existing resources for %s: %w", serviceType, err)
	}
	wantedResources := slices.Sorted(maps.Keys(serviceInfo.Resources))

	// for unit changes, we need to have some special handling, else we will interpret
	// the old values from the database with the new unit!

	type unitChange struct {
		oldUnit limes.Unit
		newUnit limes.Unit
	}
	unitChangesByResourceID := make(map[db.ResourceID]unitChange)

	// do update for resources
	resourceUpdate := db.SetUpdate[db.Resource, liquid.ResourceName]{
		ExistingRecords: dbResources,
		WantedKeys:      wantedResources,
		KeyForRecord: func(resource db.Resource) liquid.ResourceName {
			return resource.Name
		},
		Create: func(resourceName liquid.ResourceName) (db.Resource, error) {
			logg.Info("SaveServiceInfoToDB: creating Resource %s/%s with LiquidVersion = %d", serviceType, resourceName, serviceInfo.Version)
			categoryID := options.Map(serviceInfo.Resources[resourceName].Category,
				func(cn liquid.CategoryName) db.CategoryID { return categoryByName[cn].ID })
			return db.Resource{
				ServiceID:           srv.ID,
				Name:                resourceName,
				DisplayName:         serviceInfo.Resources[resourceName].DisplayName,
				CategoryID:          categoryID,
				Path:                db.ResourcePath{ServiceType: serviceType, ResourceName: resourceName},
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
			if res.Unit != serviceInfo.Resources[res.Name].Unit {
				unitChangesByResourceID[res.ID] = unitChange{
					oldUnit: res.Unit,
					newUnit: serviceInfo.Resources[res.Name].Unit,
				}
			}
			res.DisplayName = serviceInfo.Resources[res.Name].DisplayName
			res.CategoryID = options.Map(serviceInfo.Resources[res.Name].Category,
				func(cn liquid.CategoryName) db.CategoryID { return categoryByName[cn].ID })
			res.Unit = serviceInfo.Resources[res.Name].Unit
			res.Topology = serviceInfo.Resources[res.Name].Topology
			res.HasCapacity = serviceInfo.Resources[res.Name].HasCapacity
			res.NeedsResourceDemand = serviceInfo.Resources[res.Name].NeedsResourceDemand
			res.HasQuota = serviceInfo.Resources[res.Name].HasQuota
			res.AttributesJSON = string(serviceInfo.Resources[res.Name].Attributes)
			res.HandlesCommitments = serviceInfo.Resources[res.Name].HandlesCommitments
			return nil
		},
		PreDelete: Some(generateDeleteFunc[db.Resource](dbm, func(r db.Resource) string { return r.Path.String() + "/%" })),
	}
	dbResources, err = resourceUpdate.Execute(tx)
	if err != nil {
		return err
	}

	// remove unused categories (categories which are not referenced by any resource anymore)
	_, err = tx.Exec(`DELETE FROM categories WHERE id NOT IN (SELECT DISTINCT category_id FROM resources)`)
	if err != nil {
		return fmt.Errorf("cannot delete unused categories for %s: %w", serviceType, err)
	}

	// do resource unit updates if applicable
	for resID, units := range unitChangesByResourceID {
		oldBaseUnit, oldFactor := units.oldUnit.Base()
		newBaseUnit, newFactor := units.newUnit.Base()
		if oldBaseUnit != newBaseUnit {
			// the mitigation for this failing is probably to delete the resources from the database and read them fresh?
			return fmt.Errorf("cannot change unit of resource with id %d from %q to %q, because the base units differ", resID, units.oldUnit, units.newUnit)
		}

		// For all values which change with the next scrape or from config, we assume rounding is okay.
		// For commitments, our strategy cannot be rounding, because this has billing impact.
		// Therefore, we block it - any operation where we would have to round prevents the unit change.
		// We use integer modulo arithmetic to avoid floating-point precision issues.
		nonConvertibleEntries, err := tx.SelectInt(sqlext.SimplifyWhitespace(`SELECT COUNT(*)
			FROM project_commitments pc
			JOIN az_resources azr ON pc.az_resource_id = azr.id
			WHERE (pc.amount::NUMERIC * $1) % $2 != 0 AND azr.resource_id = $3`), oldFactor, newFactor, resID)
		if err != nil {
			return fmt.Errorf("error while retrieving non-convertible project_commitments with resource_id %d when changing unit from %q to %q: %w", resID, units.oldUnit, units.newUnit, err)
		}
		if nonConvertibleEntries > 0 {
			return fmt.Errorf("there are %d commitments with rounding issues when updating unit on resource_id %d from %q to %q", nonConvertibleEntries, resID, units.oldUnit, units.newUnit)
		}
		_, err = tx.Exec(sqlext.SimplifyWhitespace(`UPDATE project_commitments pc
			SET amount = pc.amount * $1 / $2
			FROM az_resources azr
			WHERE pc.az_resource_id = azr.id AND azr.resource_id = $3`), oldFactor, newFactor, resID)
		if err != nil {
			return fmt.Errorf("error while updating project_commitments with resource_id %d when changing unit from %q to %q: %w", resID, units.oldUnit, units.newUnit, err)
		}

		_, err = tx.Exec(sqlext.SimplifyWhitespace(`UPDATE az_resources
			SET raw_capacity = ROUND(raw_capacity * $1 / $2),
			usage = ROUND(usage * $1 / $2),
			last_nonzero_raw_capacity = ROUND(last_nonzero_raw_capacity * $1 / $2)
			WHERE resource_id = $3`), oldFactor, newFactor, resID)
		if err != nil {
			return fmt.Errorf("error while updating az_resources with resource_id %d when changing unit from %q to %q: %w", resID, units.oldUnit, units.newUnit, err)
		}
		_, err = tx.Exec(sqlext.SimplifyWhitespace(`UPDATE project_resources
			SET max_quota_from_outside_admin = ROUND(max_quota_from_outside_admin * $1 / $2),
			override_quota_from_config = ROUND(override_quota_from_config * $1 / $2)
			WHERE resource_id = $3`), oldFactor, newFactor, resID)
		if err != nil {
			return fmt.Errorf("error while updating project_resources with resource_id %d when changing unit from %q to %q: %w", resID, units.oldUnit, units.newUnit, err)
		}
		_, err = tx.Exec(sqlext.SimplifyWhitespace(`UPDATE project_az_resources pazr
			SET quota = ROUND(pazr.quota * $1 / $2),
			usage = ROUND(pazr.usage * $1 / $2),
			physical_usage = ROUND(pazr.physical_usage * $1 / $2),
			backend_quota = ROUND(pazr.backend_quota * $1 / $2)
			FROM az_resources azr
			WHERE pazr.az_resource_id = azr.id AND azr.resource_id = $3`), oldFactor, newFactor, resID)
		if err != nil {
			return fmt.Errorf("error while updating project_az_resources with resource_id %d when changing unit from %q to %q: %w", resID, units.oldUnit, units.newUnit, err)
		}
	}

	// collect existing az_resources
	var dbAZResources []db.AZResource
	_, err = tx.Select(&dbAZResources, `SELECT azr.* FROM az_resources azr JOIN resources r ON azr.resource_id = r.id WHERE r.service_id = $1`, srv.ID)
	if err != nil {
		return fmt.Errorf("cannot inspect existing AZ resources for %s: %w", serviceType, err)
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
					Path: db.AZResourcePath{
						ServiceType:      res.Path.ServiceType,
						ResourceName:     res.Path.ResourceName,
						AvailabilityZone: az,
					},
				}, nil
			},
			Update: func(azRes *db.AZResource) error {
				// we don't know more than the existence of the AZ, so we don't update anything
				return nil
			},
			PreDelete: Some(generateDeleteFunc[db.AZResource](dbm, func(azr db.AZResource) string { return azr.Path.String() })),
		}
		_, err = setUpdate.Execute(tx)
		if err != nil {
			return err
		}
	}

	// collect existing rates and the wanted rates
	var dbRates []db.Rate
	_, err = tx.Select(&dbRates, `SELECT * FROM rates WHERE service_id = $1`, srv.ID)
	if err != nil {
		return fmt.Errorf("cannot inspect existing rates for %s: %w", serviceType, err)
	}
	for _, rateLimit := range rateLimits.Global {
		rateInfo, ok := serviceInfo.Rates[rateLimit.Name]
		if !ok {
			return fmt.Errorf("configuration declares a global rate limit for %s/%s which is not declared by the liquid",
				serviceType, rateLimit.Name)
		}
		if rateLimit.Unit != rateInfo.Unit {
			return fmt.Errorf("configuration uses unit %q for rate %s/%s, but liquid declared unit %q",
				rateLimit.Unit, serviceType, rateLimit.Name, rateInfo.Unit)
		}
	}
	for _, rateLimit := range rateLimits.ProjectDefault {
		rateInfo, ok := serviceInfo.Rates[rateLimit.Name]
		if !ok {
			return fmt.Errorf("configuration declares a project-default rate limit for %s/%s which is not declared by the liquid",
				serviceType, rateLimit.Name)
		}
		if rateLimit.Unit != rateInfo.Unit {
			return fmt.Errorf("configuration uses unit %q for rate %s/%s, but liquid declared unit %q",
				rateLimit.Unit, serviceType, rateLimit.Name, rateInfo.Unit)
		}
	}

	// for unit changes, we need to have some special handling, else we will interpret
	// the old values from the database with the new unit!
	unitChangesByRateID := make(map[db.RateID]unitChange)

	// do update for rates
	rateUpdate := db.SetUpdate[db.Rate, liquid.RateName]{
		ExistingRecords: dbRates,
		WantedKeys:      slices.Sorted(maps.Keys(serviceInfo.Rates)),
		KeyForRecord: func(rate db.Rate) liquid.RateName {
			return rate.Name
		},
		Create: func(rateName liquid.RateName) (db.Rate, error) {
			rateInfo := serviceInfo.Rates[rateName]
			categoryID := options.Map(rateInfo.Category,
				func(cn liquid.CategoryName) db.CategoryID { return categoryByName[cn].ID })
			return db.Rate{
				ServiceID:     dbServices[0].ID,
				Name:          rateName,
				DisplayName:   rateInfo.DisplayName,
				CategoryID:    categoryID,
				Path:          db.RatePath{ServiceType: serviceType, RateName: rateName},
				LiquidVersion: serviceInfo.Version,
				Unit:          rateInfo.Unit,
				Topology:      rateInfo.Topology,
				HasUsage:      rateInfo.HasUsage,
			}, nil
		},
		Update: func(rate *db.Rate) (err error) {
			rateInfo := serviceInfo.Rates[rate.Name]

			rate.LiquidVersion = serviceInfo.Version
			rate.DisplayName = rateInfo.DisplayName
			rate.CategoryID = options.Map(rateInfo.Category,
				func(cn liquid.CategoryName) db.CategoryID { return categoryByName[cn].ID })
			rate.Topology = rateInfo.Topology
			rate.HasUsage = rateInfo.HasUsage

			if rate.Unit != rateInfo.Unit {
				unitChangesByRateID[rate.ID] = unitChange{
					oldUnit: rate.Unit,
					newUnit: rateInfo.Unit,
				}
				rate.Unit = rateInfo.Unit
			}

			return nil
		},
	}
	_, err = rateUpdate.Execute(tx)
	if err != nil {
		return err
	}

	// do rate unit updates if applicable
	for rateID, units := range unitChangesByRateID {
		oldBaseUnit, oldFactor := units.oldUnit.Base()
		newBaseUnit, newFactor := units.newUnit.Base()
		if oldBaseUnit != newBaseUnit {
			// the mitigation for this failing is probably to delete the rates from the database and read them fresh?
			return fmt.Errorf("cannot change unit of rate with id %d from %q to %q, because the base units differ", rateID, units.oldUnit, units.newUnit)
		}

		// For all values which change with the next scrape or from config, we assume rounding is okay.
		_, err = tx.Exec(sqlext.SimplifyWhitespace(`UPDATE project_rates
			SET rate_limit = ROUND(rate_limit * $1 / $2),
			usage_as_bigint = ROUND(usage_as_bigint::BIGINT * $1 / $2)::TEXT
			WHERE rate_id = $3`), oldFactor, newFactor, rateID)
		if err != nil {
			return fmt.Errorf("error while updating project_rates with rate_id %d when changing unit from %q to %q: %w", rateID, units.oldUnit, units.newUnit, err)
		}
	}

	return tx.Commit()
}
