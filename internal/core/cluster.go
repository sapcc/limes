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
	"os"
	"sort"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/osext"
	yaml "gopkg.in/yaml.v2"

	"github.com/sapcc/limes/internal/util"
)

// Cluster contains all configuration and runtime information for the target
// cluster.
type Cluster struct {
	Config            ClusterConfiguration
	DiscoveryPlugin   DiscoveryPlugin
	QuotaPlugins      map[string]QuotaPlugin
	CapacityPlugins   map[string]CapacityPlugin
	Authoritative     bool
	QuotaConstraints  *QuotaConstraintSet
	LowPrivilegeRaise LowPrivilegeRaiseLimitSet
}

// NewCluster creates a new Cluster instance with the given ID and
// configuration, and also initializes all quota and capacity plugins. Errors
// will be logged when some of the requested plugins cannot be found.
func NewCluster(config ClusterConfiguration) (c *Cluster, errs errext.ErrorSet) {
	c = &Cluster{
		Config:          config,
		QuotaPlugins:    make(map[string]QuotaPlugin),
		CapacityPlugins: make(map[string]CapacityPlugin),
		Authoritative:   osext.GetenvBool("LIMES_AUTHORITATIVE"),
	}

	//instantiate discovery plugin
	c.DiscoveryPlugin = DiscoveryPluginRegistry.Instantiate(config.Discovery.Method)
	if c.DiscoveryPlugin == nil {
		errs.Addf("setup for discovery method %s failed: no suitable discovery plugin found", config.Discovery.Method)
	}

	//instantiate quota plugins
	for _, srv := range config.Services {
		plugin := QuotaPluginRegistry.Instantiate(srv.PluginType)
		if plugin == nil {
			errs.Addf("setup for service %s failed: no suitable quota plugin found", srv.ServiceType)
		}
		c.QuotaPlugins[srv.ServiceType] = plugin
	}

	//instantiate capacity plugins
	for _, capa := range config.Capacitors {
		plugin := CapacityPluginRegistry.Instantiate(capa.PluginType)
		if plugin == nil {
			errs.Addf("setup for capacitor %s failed: no suitable capacity plugin found", capa.ID)
		}
		c.CapacityPlugins[capa.ID] = plugin
	}

	return c, errs
}

// Connect calls Connect() on all AuthParameters for this Cluster, thus ensuring
// that all ProviderClient instances are available. It also calls Init() on all
// quota plugins.
//
// It also loads the QuotaConstraints for this cluster, if configured. The
// LowPrivilegeRaise.Limits fields are also initialized here. We also validate
// if Config.ResourceBehavior[].ScalesWith refers to existing resources.
//
// We cannot do any of this earlier because we only know all resources after
// calling Init() on all quota plugins.
func (c *Cluster) Connect(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (errs errext.ErrorSet) {
	//initialize discovery plugin
	err := yaml.UnmarshalStrict([]byte(c.Config.Discovery.Parameters), c.DiscoveryPlugin)
	if err != nil {
		errs.Addf("failed to supply params to discovery method: %w", err)
	} else {
		err = c.DiscoveryPlugin.Init(provider, eo)
		if err != nil {
			errs.Addf("failed to initialize discovery method: %w", util.UnpackError(err))
		}
	}

	//initialize quota plugins
	for _, srv := range c.Config.Services {
		plugin := c.QuotaPlugins[srv.ServiceType]
		err = yaml.UnmarshalStrict([]byte(srv.Parameters), plugin)
		if err != nil {
			errs.Addf("failed to supply params to service %s: %w", srv.ServiceType, err)
			continue
		}
		err := plugin.Init(provider, eo)
		if err != nil {
			errs.Addf("failed to initialize service %s: %w", srv.ServiceType, util.UnpackError(err))
		}
	}

	//initialize capacity plugins
	for _, capa := range c.Config.Capacitors {
		plugin := c.CapacityPlugins[capa.ID]
		err = yaml.UnmarshalStrict([]byte(capa.Parameters), plugin)
		if err != nil {
			errs.Addf("failed to supply params to capacitor %s: %w", capa.ID, err)
			continue
		}
		err := plugin.Init(provider, eo)
		if err != nil {
			errs.Addf("failed to initialize capacitor %s: %w", capa.ID, util.UnpackError(err))
		}
	}

	//if we could not load all plugins, we cannot be sure that we know the
	//correct set of resources, so the following steps will likely report wrong errors
	if !errs.IsEmpty() {
		return errs
	}

	//load quota constraints
	var suberrs errext.ErrorSet
	constraintPath := os.Getenv("LIMES_CONSTRAINTS_PATH")
	if constraintPath != "" && c.QuotaConstraints == nil {
		c.QuotaConstraints, suberrs = NewQuotaConstraints(c, constraintPath)
		errs.Append(suberrs)
	}

	//parse low-privilege raise limits
	c.LowPrivilegeRaise, suberrs = c.Config.LowPrivilegeRaise.parse(c.QuotaPlugins)
	errs.Append(suberrs)

	//validate scaling relations
	for _, behavior := range c.Config.ResourceBehaviors {
		s := behavior.ScalesWith
		if s.ResourceName != "" && !c.HasResource(s.ServiceType, s.ResourceName) {
			errs.Addf(`resources matching "%s" scale with unknown resource "%s/%s"`,
				string(behavior.FullResourceNameRx), s.ServiceType, s.ResourceName)
		}
	}

	return errs
}

// ServiceTypesInAlphabeticalOrder can be used when service types need to be
// iterated over in a stable order (mostly to ensure deterministic behavior in unit tests).
func (c *Cluster) ServiceTypesInAlphabeticalOrder() []string {
	result := make([]string, 0, len(c.QuotaPlugins))
	for serviceType, quotaPlugin := range c.QuotaPlugins {
		if quotaPlugin != nil { //defense in depth (nil values should never be stored in the map anyway)
			result = append(result, serviceType)
		}
	}
	sort.Strings(result)
	return result
}

// AllServiceInfos returns the ServiceInfo for all known services.
func (c *Cluster) AllServiceInfos() []limes.ServiceInfo {
	result := make([]limes.ServiceInfo, 0, len(c.QuotaPlugins))
	for serviceType, quotaPlugin := range c.QuotaPlugins {
		result = append(result, quotaPlugin.ServiceInfo(serviceType))
	}
	return result
}

// HasService checks whether the given service is enabled in this cluster.
func (c *Cluster) HasService(serviceType string) bool {
	return c.QuotaPlugins[serviceType] != nil
}

// HasResource checks whether the given service is enabled in this cluster and
// whether it advertises the given resource.
func (c *Cluster) HasResource(serviceType, resourceName string) bool {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return false
	}
	for _, res := range plugin.Resources() {
		if res.Name == resourceName {
			return true
		}
	}
	return false
}

// InfoForResource finds the plugin for the given serviceType and finds within that
// plugin the ResourceInfo for the given resourceName. If the service or
// resource does not exist, an empty ResourceInfo (with .Unit == UnitNone and
// .Category == "") is returned.
func (c *Cluster) InfoForResource(serviceType, resourceName string) limesresources.ResourceInfo {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return limesresources.ResourceInfo{Name: resourceName, Unit: limes.UnitNone}
	}
	for _, res := range plugin.Resources() {
		if res.Name == resourceName {
			return res
		}
	}
	return limesresources.ResourceInfo{Name: resourceName, Unit: limes.UnitNone}
}

// InfoForService finds the plugin for the given serviceType and returns its
// ServiceInfo(), or an empty ServiceInfo (with .Area == "") when no such
// service exists in this cluster.
func (c *Cluster) InfoForService(serviceType string) limes.ServiceInfo {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return limes.ServiceInfo{Type: serviceType}
	}
	return plugin.ServiceInfo(serviceType)
}

// GetServiceTypesForArea returns all service types that belong to the given area.
func (c *Cluster) GetServiceTypesForArea(area string) (serviceTypes []string) {
	for serviceType, plugin := range c.QuotaPlugins {
		if plugin.ServiceInfo(serviceType).Area == area {
			serviceTypes = append(serviceTypes, serviceType)
		}
	}
	return
}

// BehaviorForResource returns the ResourceBehavior for the given resource in
// the given scope.
//
// `scopeName` should be empty for cluster resources, equal to the domain name
// for domain resources, or equal to `$DOMAIN_NAME/$PROJECT_NAME` for project
// resources.
func (c *Cluster) BehaviorForResource(serviceType, resourceName, scopeName string) ResourceBehavior {
	//default behavior
	maxBurstMultiplier := c.Config.Bursting.MaxMultiplier
	result := ResourceBehavior{
		MaxBurstMultiplier: &maxBurstMultiplier,
	}

	//check for specific behavior
	fullName := serviceType + "/" + resourceName
	for _, behavior := range c.Config.ResourceBehaviors {
		if behavior.Matches(fullName, scopeName) {
			result.Merge(behavior)
		}
	}

	return result
}

// QuotaDistributionConfigForResource returns the QuotaDistributionConfiguration
// for the given resource.
func (c *Cluster) QuotaDistributionConfigForResource(serviceType, resourceName string) QuotaDistributionConfiguration {
	//check for specific behavior
	fullName := serviceType + "/" + resourceName
	for _, dmCfg := range c.Config.QuotaDistributionConfigs {
		if dmCfg.FullResourceNameRx.MatchString(fullName) {
			return *dmCfg
		}
	}

	//default behavior
	return QuotaDistributionConfiguration{Model: limesresources.HierarchicalQuotaDistribution}
}

// HasUsageForRate checks whether the given service is enabled in this cluster and
// whether it scrapes usage for the given rate.
func (c *Cluster) HasUsageForRate(serviceType, rateName string) bool {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return false
	}
	for _, rate := range plugin.Rates() {
		if rate.Name == rateName {
			return true
		}
	}
	return false
}

// InfoForRate finds the plugin for the given serviceType and finds within that
// plugin the RateInfo for the given rateName. If the service or rate does not
// exist, an empty RateInfo (with .Unit == UnitNone) is returned. Note that this
// only returns non-empty RateInfos for rates where a usage is reported. There
// may be rates that only have a limit, as defined in the ClusterConfiguration.
func (c *Cluster) InfoForRate(serviceType, rateName string) limesrates.RateInfo {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return limesrates.RateInfo{Name: rateName, Unit: limes.UnitNone}
	}
	for _, rate := range plugin.Rates() {
		if rate.Name == rateName {
			return rate
		}
	}
	return limesrates.RateInfo{Name: rateName, Unit: limes.UnitNone}
}
