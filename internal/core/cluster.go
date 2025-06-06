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

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// Cluster contains all configuration and runtime information for the target
// cluster.
type Cluster struct {
	Config            ClusterConfiguration
	DiscoveryPlugin   DiscoveryPlugin
	LiquidConnections map[db.ServiceType]*LiquidConnection
	// reference of the DB is necessary to delete leftover LiquidConnections
	DB *gorp.DbMap
}

// NewCluster creates a new Cluster instance also initializes the LiquidConnections. Errors
// will be logged when the requested DiscoveryPlugin cannot be found.
func NewCluster(config ClusterConfiguration, timeNow func() time.Time, dbm *gorp.DbMap) (c *Cluster, errs errext.ErrorSet) {
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

	// fill LiquidConnection map
	for serviceType, l := range config.Liquids {
		connection := MakeLiquidConnection(l, serviceType, timeNow, dbm)
		c.LiquidConnections[serviceType] = &connection
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

	// initialize liquid connections
	for serviceType := range c.Config.Liquids {
		conn := c.LiquidConnections[serviceType]
		err := conn.Init(ctx, provider, eo)
		if err != nil {
			errs.Addf("failed to initialize service %s: %w", serviceType, util.UnpackError(err))
		}
	}

	return errs
}

// ReconcileLiquidConnections should be called once on startup of the limes-collect task, so that
// the database tables are in a consistent state with the configured LiquidConnections. For this,
// each individual LiquidConnection is reconciled and leftover entries are deleted. This means resources
// and rates can change without restarting the limes-collect task, but a change in of LiquidConnections
// needs restart.
func (c *Cluster) ReconcileLiquidConnections() (err error) {
	// sort for testing purposes
	serviceTypes := slices.Sorted(maps.Keys(c.LiquidConnections))

	for _, serviceType := range serviceTypes {
		err := c.LiquidConnections[serviceType].reconcileLiquidConnection()
		if err != nil {
			return err
		}
	}

	// delete all orphaned cluster_services
	// respective cluster_resources, cluster_az_resources and cluster_rates are handled by delete-cascade
	_, err = c.DB.Exec(`DELETE FROM cluster_services WHERE type != ALL($1)`, pq.Array(serviceTypes))
	if err != nil {
		return fmt.Errorf("cannot cleanup orphaned cluster_services: %w", err)
	}
	return nil
}

// ServiceTypesInAlphabeticalOrder can be used when service types need to be
// iterated over in a stable order (mostly to ensure deterministic behavior in unit tests).
func (c *Cluster) ServiceTypesInAlphabeticalOrder() []db.ServiceType {
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
	return c.LiquidConnections[serviceType] != nil
}

// HasResource checks whether the given service is enabled in this cluster and
// whether it advertises the given resource.
func (c *Cluster) HasResource(serviceType db.ServiceType, resourceName liquid.ResourceName) bool {
	connection := c.LiquidConnections[serviceType]
	if connection == nil {
		return false
	}
	_, exists := connection.ServiceInfo().Resources[resourceName]
	return exists
}

// InfoForResource finds the connection for the given serviceType and finds within that
// connection the ResourceInfo for the given resourceName. If the service or
// resource does not exist, an empty ResourceInfo (with .Unit == UnitNone and
// .Category == "") is returned.
func (c *Cluster) InfoForResource(serviceType db.ServiceType, resourceName liquid.ResourceName) liquid.ResourceInfo {
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
	connection := c.LiquidConnections[serviceType]
	if connection == nil {
		return false
	}
	_, exists := connection.ServiceInfo().Rates[rateName]
	return exists
}

// InfoForRate finds the connection for the given serviceType and finds within that
// connection the RateInfo for the given rateName. If the service or rate does not
// exist, an empty RateInfo (with .Unit == UnitNone) is returned. Note that this
// only returns non-empty RateInfos for rates where a usage is reported. There
// may be rates that only have a limit, as defined in the ClusterConfiguration.
func (c *Cluster) InfoForRate(serviceType db.ServiceType, rateName liquid.RateName) liquid.RateInfo {
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
