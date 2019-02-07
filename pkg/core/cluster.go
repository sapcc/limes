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
	"sort"

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
		LimitsForDomains  map[string]map[string]uint64
		LimitsForProjects map[string]map[string]uint64
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

	//load quota constraints
	if len(c.Config.ConstraintConfigPaths) != 0 && c.QuotaConstraints == nil {
		for _, path := range c.Config.ConstraintConfigPaths {
			var errs []error
			constraints, errs := NewQuotaConstraints(c, path)
			if len(errs) > 0 {
				for _, err := range errs {
					logg.Error(err.Error())
				}
				return fmt.Errorf("cannot load quota constraints for cluster %s from %s (see errors above)", c.ID, path)
			}
			if c.QuotaConstraints == nil {
				c.QuotaConstraints = constraints
			} else {
				c.QuotaConstraints.ExtendWith(constraints)
			}
		}
	}

	//parse low-privilege raise limits
	c.LowPrivilegeRaise.LimitsForDomains, err = c.parseLowPrivilegeRaiseLimits(
		c.Config.LowPrivilegeRaise.Limits.ForDomains)
	if err != nil {
		return err
	}
	c.LowPrivilegeRaise.LimitsForProjects, err = c.parseLowPrivilegeRaiseLimits(
		c.Config.LowPrivilegeRaise.Limits.ForProjects)
	if err != nil {
		return err
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

func (c Cluster) parseLowPrivilegeRaiseLimits(inputs map[string]map[string]string) (map[string]map[string]uint64, error) {
	result := make(map[string]map[string]uint64)
	var err error
	for srvType, quotaPlugin := range c.QuotaPlugins {
		result[srvType] = make(map[string]uint64)
		for _, res := range quotaPlugin.Resources() {
			limit, exists := inputs[srvType][res.Name]
			if !exists {
				continue
			}
			result[srvType][res.Name], err = res.Unit.Parse(limit)
			if err != nil {
				return nil, fmt.Errorf("could not parse low-privilege raise limit: %s", err.Error())
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

//BehaviorForResource returns the ResourceBehavior for the given resource. If
//no special behavior has been configured for this resource, or if this
//resource does not exist, a zero-initialized struct is returned.
func (c *Cluster) BehaviorForResource(serviceType, resourceName string) ResourceBehavior {
	//default behavior
	result := ResourceBehavior{
		MaxBurstMultiplier: c.Config.Bursting.MaxMultiplier,
	}

	//check for specific behavior
	fullName := serviceType + "/" + resourceName
	for _, behaviorConfig := range c.Config.ResourceBehaviors {
		behavior := behaviorConfig.Compiled
		if !behavior.FullResourceName.MatchString(fullName) {
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
	}

	return result
}
