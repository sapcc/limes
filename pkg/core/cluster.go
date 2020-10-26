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
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes"
)

//Cluster contains all configuration and runtime information about a single
//cluster. It is passed around a lot in Limes code, mostly for the cluster ID,
//the list of enabled services, and access to the quota and capacity plugins.
type Cluster struct {
	ID                string
	Config            *ClusterConfiguration
	ServiceTypes      []string
	IsServiceShared   map[string]bool
	DiscoveryPlugin   DiscoveryPlugin
	QuotaPlugins      map[string]QuotaPlugin
	CapacityPlugins   map[string]CapacityPlugin
	Authoritative     bool
	QuotaConstraints  *QuotaConstraintSet
	LowPrivilegeRaise struct {
		LimitsForDomains  map[string]map[string]LowPrivilegeRaiseLimit
		LimitsForProjects map[string]map[string]LowPrivilegeRaiseLimit
	}
}

//NewCluster creates a new Cluster instance with the given ID and
//configuration, and also initializes all quota and capacity plugins. Errors
//will be logged when some of the requested plugins cannot be found.
func NewCluster(id string, config *ClusterConfiguration) *Cluster {
	factory, exists := discoveryPluginFactories[config.Discovery.Method]
	if !exists {
		logg.Fatal("setup for cluster %s failed: no suitable discovery plugin found", id)
	}

	c := &Cluster{
		ID:              id,
		Config:          config,
		IsServiceShared: make(map[string]bool),
		DiscoveryPlugin: factory(config.Discovery),
		QuotaPlugins:    make(map[string]QuotaPlugin),
		CapacityPlugins: make(map[string]CapacityPlugin),
		Authoritative:   config.Authoritative,
	}

	for _, srv := range config.Services {
		factory, exists := quotaPluginFactories[srv.Type]
		if !exists {
			logg.Error("skipping service %s: no suitable collector plugin found", srv.Type)
			continue
		}

		scrapeSubresources := map[string]bool{}
		for _, resName := range config.Subresources[srv.Type] {
			scrapeSubresources[resName] = true
		}

		plugin := factory(srv, scrapeSubresources)
		if plugin == nil || plugin.ServiceInfo().Type != srv.Type {
			logg.Error("skipping service %s: failed to initialize collector plugin", srv.Type)
			continue
		}

		c.ServiceTypes = append(c.ServiceTypes, srv.Type)
		c.QuotaPlugins[srv.Type] = plugin
		c.IsServiceShared[srv.Type] = srv.Shared
	}

	scrapeSubcapacities := make(map[string]map[string]bool)
	for serviceType, resourceNames := range config.Subcapacities {
		m := make(map[string]bool)
		for _, resourceName := range resourceNames {
			m[resourceName] = true
		}
		scrapeSubcapacities[serviceType] = m
	}

	for _, capa := range config.Capacitors {
		factory, exists := capacityPluginFactories[capa.ID]
		if !exists {
			logg.Error("skipping capacitor %s: no suitable collector plugin found", capa.ID)
			continue
		}
		plugin := factory(capa, scrapeSubcapacities)
		if plugin == nil || plugin.ID() != capa.ID {
			logg.Error("skipping capacitor %s: failed to initialize collector plugin", capa.ID)
			continue
		}
		c.CapacityPlugins[capa.ID] = plugin
	}

	sort.Strings(c.ServiceTypes) //determinism is useful for unit tests

	return c
}

//Connect calls Connect() on all AuthParameters for this Cluster, thus ensuring
//that all ProviderClient instances are available. It also calls Init() on all
//quota plugins.
//
//It also loads the QuotaConstraints for this cluster, if configured. The
//LowPrivilegeRaise.Limits fields are also initialized here. We also validate
//if Config.ResourceBehavior[].ScalesWith refers to existing resources.
//
//We cannot do any of this earlier because we only know all resources after
//calling Init() on all quota plugins.
func (c *Cluster) Connect() error {
	err := c.Config.Auth.Connect()
	if err != nil {
		return fmt.Errorf("failed to authenticate in cluster %s: %s", c.ID, err.Error())
	}

	for _, srv := range c.Config.Services {
		provider := c.Config.Auth.ProviderClient
		eo := c.Config.Auth.EndpointOpts

		if srv.Auth != nil {
			err := srv.Auth.Connect()
			if err != nil {
				return fmt.Errorf("failed to authenticate for service %s in cluster %s: %s", srv.Type, c.ID, err.Error())
			}
			provider = srv.Auth.ProviderClient
			eo = srv.Auth.EndpointOpts
		}

		err := c.QuotaPlugins[srv.Type].Init(provider, eo)
		if err != nil {
			return fmt.Errorf("failed to initialize service %s in cluster %s: %s", srv.Type, c.ID, err.Error())
		}
	}

	for _, capa := range c.Config.Capacitors {
		provider := c.Config.Auth.ProviderClient
		eo := c.Config.Auth.EndpointOpts

		if capa.Auth != nil {
			err := capa.Auth.Connect()
			if err != nil {
				return fmt.Errorf("failed to authenticate for capacitor %s in cluster %s: %s", capa.ID, c.ID, err.Error())
			}
			provider = capa.Auth.ProviderClient
			eo = capa.Auth.EndpointOpts
		}

		err := c.CapacityPlugins[capa.ID].Init(provider, eo)
		if err != nil {
			return fmt.Errorf("failed to initialize capacitor %s in cluster %s: %s", capa.ID, c.ID, err.Error())
		}
	}

	//load quota constraints
	if c.Config.ConstraintConfigPath != "" && c.QuotaConstraints == nil {
		var errs []error
		c.QuotaConstraints, errs = NewQuotaConstraints(c, c.Config.ConstraintConfigPath)
		if len(errs) > 0 {
			for _, err := range errs {
				logg.Error(err.Error())
			}
			return fmt.Errorf("cannot load quota constraints for cluster %s (see errors above)", c.ID)
		}
	}

	//parse low-privilege raise limits
	c.LowPrivilegeRaise.LimitsForDomains, err = c.parseLowPrivilegeRaiseLimits(
		c.Config.LowPrivilegeRaise.Limits.ForDomains, "domain")
	if err != nil {
		return fmt.Errorf("could not parse low-privilege raise limit: %s", err.Error())
	}
	c.LowPrivilegeRaise.LimitsForProjects, err = c.parseLowPrivilegeRaiseLimits(
		c.Config.LowPrivilegeRaise.Limits.ForProjects, "project")
	if err != nil {
		return fmt.Errorf("could not parse low-privilege raise limit: %s", err.Error())
	}

	//validate scaling relations
	for _, behavior := range c.Config.ResourceBehaviors {
		b := behavior.Compiled
		if b.ScalesWithResourceName == "" {
			continue
		}
		if !c.HasResource(b.ScalesWithServiceType, b.ScalesWithResourceName) {
			return fmt.Errorf(`resources matching "%s" scale with unknown resource "%s/%s"`,
				behavior.FullResourceName, b.ScalesWithServiceType, b.ScalesWithResourceName)
		}
	}

	return nil
}

var percentOfClusterRx = regexp.MustCompile(`^([0-9.]+)\s*% of cluster capacity$`)
var untilPercentOfClusterAssignedRx = regexp.MustCompile(`^until ([0-9.]+)\s*% of cluster capacity is assigned$`)

func (c Cluster) parseLowPrivilegeRaiseLimits(inputs map[string]map[string]string, scopeType string) (map[string]map[string]LowPrivilegeRaiseLimit, error) {
	result := make(map[string]map[string]LowPrivilegeRaiseLimit)
	for srvType, quotaPlugin := range c.QuotaPlugins {
		result[srvType] = make(map[string]LowPrivilegeRaiseLimit)
		for _, res := range quotaPlugin.Resources() {
			limit, exists := inputs[srvType][res.Name]
			if !exists {
				continue
			}

			match := percentOfClusterRx.FindStringSubmatch(limit)
			if match != nil {
				percent, err := strconv.ParseFloat(match[1], 64)
				if err != nil {
					return nil, err
				}
				if percent < 0 || percent > 100 {
					return nil, fmt.Errorf("value out of range: %s%%", match[1])
				}
				result[srvType][res.Name] = LowPrivilegeRaiseLimit{
					PercentOfClusterCapacity: percent,
				}
				continue
			}

			//the "until X% of cluster capacity is assigned" syntax is only allowed for domains
			if scopeType == "domain" {
				match := untilPercentOfClusterAssignedRx.FindStringSubmatch(limit)
				if match != nil {
					percent, err := strconv.ParseFloat(match[1], 64)
					if err != nil {
						return nil, err
					}
					if percent < 0 || percent > 100 {
						return nil, fmt.Errorf("value out of range: %s%%", match[1])
					}
					result[srvType][res.Name] = LowPrivilegeRaiseLimit{
						UntilPercentOfClusterCapacityAssigned: percent,
					}
					continue
				}
			}

			rawValue, err := res.Unit.Parse(limit)
			if err != nil {
				return nil, err
			}
			result[srvType][res.Name] = LowPrivilegeRaiseLimit{
				AbsoluteValue: rawValue,
			}
		}
	}
	return result, nil
}

//ProviderClient returns the gophercloud.ProviderClient for this cluster. This
//returns nil unless Connect() is called first. (This usually happens at
//program startup time for the current cluster.)
func (c *Cluster) ProviderClient() (*gophercloud.ProviderClient, gophercloud.EndpointOpts) {
	return c.Config.Auth.ProviderClient, c.Config.Auth.EndpointOpts
}

//ProviderClientForCapacitor returns the gophercloud.ProviderClient for this
//capacitor. This returns nil unless Connect() is called first. (This usually
//happens at program startup time for the current cluster.)
func (c *Cluster) ProviderClientForCapacitor(capacitorID string) (*gophercloud.ProviderClient, gophercloud.EndpointOpts) {
	for _, capa := range c.Config.Capacitors {
		if capa.ID == capacitorID && capa.Auth != nil {
			return capa.Auth.ProviderClient, capa.Auth.EndpointOpts
		}
	}
	return c.Config.Auth.ProviderClient, c.Config.Auth.EndpointOpts
}

//ProviderClientForService returns the gophercloud.ProviderClient for this
//service. This returns nil unless Connect() is called first. (This usually
//happens at program startup time for the current cluster.)
func (c *Cluster) ProviderClientForService(serviceType string) (*gophercloud.ProviderClient, gophercloud.EndpointOpts) {
	for _, srv := range c.Config.Services {
		if srv.Type == serviceType && srv.Auth != nil {
			return srv.Auth.ProviderClient, srv.Auth.EndpointOpts
		}
	}
	return c.Config.Auth.ProviderClient, c.Config.Auth.EndpointOpts
}

//HasService checks whether the given service is enabled in this cluster.
func (c *Cluster) HasService(serviceType string) bool {
	return c.QuotaPlugins[serviceType] != nil
}

//HasResource checks whether the given service is enabled in this cluster and
//whether it advertises the given resource.
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

//InfoForResource finds the plugin for the given serviceType and finds within that
//plugin the ResourceInfo for the given resourceName. If the service or
//resource does not exist, an empty ResourceInfo (with .Unit == UnitNone and
//.Category == "") is returned.
func (c *Cluster) InfoForResource(serviceType, resourceName string) limes.ResourceInfo {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return limes.ResourceInfo{Name: resourceName, Unit: limes.UnitNone}
	}
	for _, res := range plugin.Resources() {
		if res.Name == resourceName {
			return res
		}
	}
	return limes.ResourceInfo{Name: resourceName, Unit: limes.UnitNone}
}

//InfoForService finds the plugin for the given serviceType and returns its
//ServiceInfo(), or an empty ServiceInfo (with .Area == "") when no such
//service exists in this cluster.
func (c *Cluster) InfoForService(serviceType string) limes.ServiceInfo {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return limes.ServiceInfo{Type: serviceType}
	}
	return plugin.ServiceInfo()
}

//BehaviorForResource returns the ResourceBehavior for the given resource in
//the given scope.
//
//`scopeName` should be empty for cluster resources, equal to the domain name
//for domain resources, or equal to `$DOMAIN_NAME/$PROJECT_NAME` for project
//resources.
func (c *Cluster) BehaviorForResource(serviceType, resourceName, scopeName string) ResourceBehavior {
	//default behavior
	result := ResourceBehavior{
		MaxBurstMultiplier: c.Config.Bursting.MaxMultiplier,
	}

	//check for specific behavior
	fullName := serviceType + "/" + resourceName
	for _, behaviorConfig := range c.Config.ResourceBehaviors {
		behavior := behaviorConfig.Compiled
		if !behavior.FullResourceNameRx.MatchString(fullName) {
			continue
		}
		if scopeName != "" && behavior.ScopeRx != nil && !behavior.ScopeRx.MatchString(scopeName) {
			continue
		}

		// merge `behavior` into `result`
		if result.MaxBurstMultiplier > behavior.MaxBurstMultiplier {
			result.MaxBurstMultiplier = behavior.MaxBurstMultiplier
		}
		if behavior.OvercommitFactor != 0 {
			result.OvercommitFactor = behavior.OvercommitFactor
		}
		if behavior.ScalingFactor != 0 {
			result.ScalesWithServiceType = behavior.ScalesWithServiceType
			result.ScalesWithResourceName = behavior.ScalesWithResourceName
			result.ScalingFactor = behavior.ScalingFactor
		}
		if result.MinNonZeroProjectQuota < behavior.MinNonZeroProjectQuota {
			result.MinNonZeroProjectQuota = behavior.MinNonZeroProjectQuota
		}
		if len(behavior.Annotations) > 0 && result.Annotations == nil {
			result.Annotations = make(map[string]interface{})
		}
		for k, v := range behavior.Annotations {
			result.Annotations[k] = v
		}
	}

	return result
}

//HasUsageForRate checks whether the given service is enabled in this cluster and
//whether it scrapes usage for the given rate.
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

//InfoForRate finds the plugin for the given serviceType and finds within that
//plugin the RateInfo for the given rateName. If the service or rate does not
//exist, an empty RateInfo (with .Unit == UnitNone) is returned. Note that this
//only returns non-empty RateInfos for rates where a usage is reported. There
//may be rates that only have a limit, as defined in the ClusterConfiguration.
func (c *Cluster) InfoForRate(serviceType, rateName string) limes.RateInfo {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return limes.RateInfo{Name: rateName, Unit: limes.UnitNone}
	}
	for _, rate := range plugin.Rates() {
		if rate.Name == rateName {
			return rate
		}
	}
	return limes.RateInfo{Name: rateName, Unit: limes.UnitNone}
}
