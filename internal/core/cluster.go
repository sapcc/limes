/*******************************************************************************
*
* Copyright 2017 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package core

import (
	"context"
	"slices"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/errext"
	yaml "gopkg.in/yaml.v2"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// Cluster contains all configuration and runtime information for the target
// cluster.
type Cluster struct {
	Config            ClusterConfiguration
	DiscoveryPlugin   DiscoveryPlugin
	LiquidConnections map[db.ServiceType]*LiquidConnection
}

// NewCluster creates a new Cluster instance also initializes the LiquidConnections. Errors
// will be logged when the requested DiscoveryPlugin cannot be found.
func NewCluster(config ClusterConfiguration) (c *Cluster, errs errext.ErrorSet) {
	c = &Cluster{
		Config:            config,
		LiquidConnections: make(map[db.ServiceType]*LiquidConnection),
	}

	// instantiate discovery plugin
	c.DiscoveryPlugin = DiscoveryPluginRegistry.Instantiate(config.Discovery.Method)
	if c.DiscoveryPlugin == nil {
		errs.Addf("setup for discovery method %s failed: no suitable discovery plugin found", config.Discovery.Method)
	}

	// fill LiquidConnection map
	for _, srv := range config.Liquids {
		connection := MakeLiquidConnection(srv)
		c.LiquidConnections[srv.ServiceType] = &connection
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
	err := yaml.UnmarshalStrict([]byte(c.Config.Discovery.Parameters), c.DiscoveryPlugin)
	if err != nil {
		errs.Addf("failed to supply params to discovery method: %w", err)
	} else {
		err = c.DiscoveryPlugin.Init(ctx, provider, eo)
		if err != nil {
			errs.Addf("failed to initialize discovery method: %w", util.UnpackError(err))
		}
	}

	// initialize liquid connections
	for _, srv := range c.Config.Liquids {
		conn := c.LiquidConnections[srv.ServiceType]
		err := conn.Init(ctx, provider, eo, srv.ServiceType)
		if err != nil {
			errs.Addf("failed to initialize service %s: %w", srv.ServiceType, util.UnpackError(err))
		}
	}

	return errs
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
	for _, cfg := range c.Config.Liquids {
		if cfg.ServiceType == serviceType {
			return cfg
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
