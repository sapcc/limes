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
	"sort"

	"github.com/sapcc/limes/pkg/util"
)

//Cluster contains all configuration and runtime information about a single
//cluster. It is passed around a lot in Limes code, mostly for the cluster ID,
//the list of enabled services, and access to the quota and capacity plugins.
type Cluster struct {
	ID              string
	Config          *ClusterConfiguration
	ServiceTypes    []string
	IsServiceShared map[string]bool
	QuotaPlugins    map[string]QuotaPlugin
	CapacityPlugins map[string]CapacityPlugin
}

//NewCluster creates a new Cluster instance with the given ID and
//configuration, and also initializes all quota and capacity plugins. Errors
//will be logged when some of the requested plugins cannot be found.
func NewCluster(id string, config *ClusterConfiguration) *Cluster {
	c := &Cluster{
		ID:              id,
		Config:          config,
		IsServiceShared: make(map[string]bool),
		QuotaPlugins:    make(map[string]QuotaPlugin),
		CapacityPlugins: make(map[string]CapacityPlugin),
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

	for _, capa := range config.Capacitors {
		factory, exists := capacityPluginFactories[capa.ID]
		if !exists {
			util.LogError("skipping capacitor %s: no suitable collector plugin found", capa.ID)
			continue
		}
		plugin := factory(capa)
		if plugin == nil || plugin.ID() != capa.ID {
			util.LogError("skipping capacitor %s: failed to initialize collector plugin", capa.ID)
			continue
		}
		c.CapacityPlugins[capa.ID] = plugin
	}

	sort.Strings(c.ServiceTypes) //determinism is useful for unit tests

	return c
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
