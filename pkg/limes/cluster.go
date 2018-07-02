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

package limes

import (
	"fmt"
	"sort"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/limes/pkg/util"
)

//Cluster contains all configuration and runtime information about a single
//cluster. It is passed around a lot in Limes code, mostly for the cluster ID,
//the list of enabled services, and access to the quota and capacity plugins.
type Cluster struct {
	ID               string
	Config           *ClusterConfiguration
	ServiceTypes     []string
	IsServiceShared  map[string]bool
	DiscoveryPlugin  DiscoveryPlugin
	QuotaPlugins     map[string]QuotaPlugin
	CapacityPlugins  map[string]CapacityPlugin
	Authoritative    bool
	QuotaConstraints *QuotaConstraintSet
}

//NewCluster creates a new Cluster instance with the given ID and
//configuration, and also initializes all quota and capacity plugins. Errors
//will be logged when some of the requested plugins cannot be found.
func NewCluster(id string, config *ClusterConfiguration) *Cluster {
	factory, exists := discoveryPluginFactories[config.Discovery.Method]
	if !exists {
		util.LogFatal("setup for cluster %s failed: no suitable discovery plugin found", id)
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
			util.LogError("skipping service %s: no suitable collector plugin found", srv.Type)
			continue
		}

		scrapeSubresources := map[string]bool{}
		for _, resName := range config.Subresources[srv.Type] {
			scrapeSubresources[resName] = true
		}

		plugin := factory(srv, scrapeSubresources)
		if plugin == nil || plugin.ServiceInfo().Type != srv.Type {
			util.LogError("skipping service %s: failed to initialize collector plugin", srv.Type)
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
			util.LogError("skipping capacitor %s: no suitable collector plugin found", capa.ID)
			continue
		}
		plugin := factory(capa, scrapeSubcapacities)
		if plugin == nil || plugin.ID() != capa.ID {
			util.LogError("skipping capacitor %s: failed to initialize collector plugin", capa.ID)
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
//It also loads the QuotaConstraints for thie cluster, if configured.
func (c *Cluster) Connect() error {
	if c.Config.ConstraintConfigPath != "" && c.QuotaConstraints == nil {
		var errs []error
		c.QuotaConstraints, errs = NewQuotaConstraints(c, c.Config.ConstraintConfigPath)
		if len(errs) > 0 {
			for _, err := range errs {
				util.LogError(err.Error())
			}
			return fmt.Errorf("cannot load quota constraints for cluster %s (see errors above)", c.ID)
		}
	}

	err := c.Config.Auth.Connect()
	if err != nil {
		return fmt.Errorf("failed to authenticate in cluster %s: %s", c.ID, err.Error())
	}

	for _, srv := range c.Config.Services {
		provider := c.Config.Auth.ProviderClient

		if srv.Auth != nil {
			err := srv.Auth.Connect()
			if err != nil {
				return fmt.Errorf("failed to authenticate for service %s in cluster %s: %s", srv.Type, c.ID, err.Error())
			}
			provider = srv.Auth.ProviderClient
		}

		err := c.QuotaPlugins[srv.Type].Init(provider)
		if err != nil {
			return fmt.Errorf("failed to initialize service %s in cluster %s: %s", srv.Type, c.ID, err.Error())
		}
	}

	return nil
}

//ProviderClient returns the gophercloud.ProviderClient for this cluster. This
//returns nil unless Connect() is called first. (This usually happens at
//program startup time for the current cluster.)
func (c *Cluster) ProviderClient() *gophercloud.ProviderClient {
	return c.Config.Auth.ProviderClient
}

//ProviderClientForService returns the gophercloud.ProviderClient for this
//service. This returns nil unless Connect() is called first. (This usually
//happens at program startup time for the current cluster.)
func (c *Cluster) ProviderClientForService(serviceType string) *gophercloud.ProviderClient {
	for _, srv := range c.Config.Services {
		if srv.Type == serviceType && srv.Auth != nil {
			return srv.Auth.ProviderClient
		}
	}
	return c.Config.Auth.ProviderClient
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
func (c *Cluster) InfoForResource(serviceType, resourceName string) ResourceInfo {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return ResourceInfo{Name: resourceName, Unit: UnitNone}
	}
	for _, res := range plugin.Resources() {
		if res.Name == resourceName {
			return res
		}
	}
	return ResourceInfo{Name: resourceName, Unit: UnitNone}
}

//InfoForService finds the plugin for the given serviceType and returns its
//ServiceInfo(), or an empty ServiceInfo (with .Area == "") when no such
//service exists in this cluster.
func (c *Cluster) InfoForService(serviceType string) ServiceInfo {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return ServiceInfo{Type: serviceType}
	}
	return plugin.ServiceInfo()
}
