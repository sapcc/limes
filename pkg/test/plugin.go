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

package test

import (
	"errors"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
)

//Plugin is a core.QuotaPlugin implementation for unit tests.
type Plugin struct {
	StaticServiceType  string
	StaticResourceData map[string]*core.ResourceData
	StaticCapacity     map[string]uint64
	OverrideQuota      map[string]map[string]uint64
	//behavior flags that can be set by a unit test
	ScrapeFails                   bool
	SetQuotaFails                 bool
	WithExternallyManagedResource bool
}

var resources = []limes.ResourceInfo{
	{
		Name: "capacity",
		Unit: limes.UnitBytes,
	},
	{
		Name: "things",
		Unit: limes.UnitNone,
	},
}

//NewPlugin creates a new Plugin for the given service type.
func NewPlugin(serviceType string) *Plugin {
	return &Plugin{
		StaticServiceType: serviceType,
		StaticResourceData: map[string]*core.ResourceData{
			"things":          {Quota: 42, Usage: 2},
			"capacity":        {Quota: 100, Usage: 0},
			"external_things": {Quota: 5, Usage: 0},
		},
		OverrideQuota: make(map[string]map[string]uint64),
	}
}

//NewPluginFactory creates a new PluginFactory for core.RegisterQuotaPlugin.
func NewPluginFactory(serviceType string) func(core.ServiceConfiguration, map[string]bool) core.QuotaPlugin {
	return func(cfg core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		//cfg and scrapeSubresources is ignored
		return NewPlugin(serviceType)
	}
}

//Init implements the core.QuotaPlugin interface.
func (p *Plugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

//ServiceInfo implements the core.QuotaPlugin interface.
func (p *Plugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type: p.StaticServiceType,
		Area: p.StaticServiceType,
	}
}

//Resources implements the core.QuotaPlugin interface.
func (p *Plugin) Resources() []limes.ResourceInfo {
	result := resources
	if p.WithExternallyManagedResource {
		result = append(result, limes.ResourceInfo{
			Name:              "external_things",
			Unit:              limes.UnitNone,
			ExternallyManaged: true,
		})
	}
	return result
}

//Scrape implements the core.QuotaPlugin interface.
func (p *Plugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string) (map[string]core.ResourceData, error) {
	if p.ScrapeFails {
		return nil, errors.New("Scrape failed as requested")
	}

	result := make(map[string]core.ResourceData)
	for key, val := range p.StaticResourceData {
		if !p.WithExternallyManagedResource && key == "external_things" {
			continue
		}
		copyOfVal := *val

		//test coverage for PhysicalUsage != Usage
		if key == "capacity" {
			physUsage := val.Usage / 2
			copyOfVal.PhysicalUsage = &physUsage
		}

		result[key] = copyOfVal
	}

	data, exists := p.OverrideQuota[projectUUID]
	if exists {
		for resourceName, quota := range data {
			result[resourceName] = core.ResourceData{
				Quota:         int64(quota),
				Usage:         result[resourceName].Usage,
				PhysicalUsage: result[resourceName].PhysicalUsage,
			}
		}
	}

	//make up some subresources for "things"
	thingsUsage := int(result["things"].Usage)
	subres := make([]interface{}, thingsUsage)
	for idx := 0; idx < thingsUsage; idx++ {
		subres[idx] = map[string]interface{}{
			"index": idx,
		}
	}
	result["things"] = core.ResourceData{
		Quota:        result["things"].Quota,
		Usage:        result["things"].Usage,
		Subresources: subres,
	}

	return result, nil
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *Plugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	if p.SetQuotaFails {
		return errors.New("SetQuota failed as requested")
	}
	p.OverrideQuota[projectUUID] = quotas
	return nil
}

//CapacityPlugin is a core.CapacityPlugin implementation for unit tests.
type CapacityPlugin struct {
	PluginID          string
	Resources         []string //each formatted as "servicetype/resourcename"
	Capacity          uint64
	WithSubcapacities bool
}

//NewCapacityPlugin creates a new CapacityPlugin.
func NewCapacityPlugin(id string, resources ...string) *CapacityPlugin {
	return &CapacityPlugin{id, resources, 42, false}
}

//Init implements the core.CapacityPlugin interface.
func (p *CapacityPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

//ID implements the core.CapacityPlugin interface.
func (p *CapacityPlugin) ID() string {
	return p.PluginID
}

//Scrape implements the core.CapacityPlugin interface.
func (p *CapacityPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID string) (map[string]map[string]core.CapacityData, error) {
	var subcapacities []interface{}
	if p.WithSubcapacities {
		subcapacities = []interface{}{
			map[string]uint64{"smaller_half": p.Capacity / 3},
			map[string]uint64{"larger_half": p.Capacity - p.Capacity/3},
		}
	}

	result := make(map[string]map[string]core.CapacityData)
	for _, str := range p.Resources {
		parts := strings.SplitN(str, "/", 2)
		_, exists := result[parts[0]]
		if !exists {
			result[parts[0]] = make(map[string]core.CapacityData)
		}
		result[parts[0]][parts[1]] = core.CapacityData{Capacity: p.Capacity, Subcapacities: subcapacities}
	}
	return result, nil
}
