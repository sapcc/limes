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

package plugins

import (
	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/promquery"

	"github.com/sapcc/limes/internal/core"
)

type capacityPrometheusPlugin struct {
	APIConfig promquery.Config                                             `yaml:"api"`
	Queries   map[limes.ServiceType]map[limesresources.ResourceName]string `yaml:"queries"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityPrometheusPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) PluginTypeID() string {
	return "prometheus"
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) Scrape(_ core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (result map[limes.ServiceType]map[limesresources.ResourceName]core.PerAZ[core.CapacityData], _ []byte, err error) {
	client, err := p.APIConfig.Connect()
	if err != nil {
		return nil, nil, err
	}

	result = make(map[limes.ServiceType]map[limesresources.ResourceName]core.PerAZ[core.CapacityData])
	for serviceType, queries := range p.Queries {
		serviceResult := make(map[limesresources.ResourceName]core.PerAZ[core.CapacityData])
		for resourceName, query := range queries {
			value, err := client.GetSingleValue(query, nil)
			if err != nil {
				return nil, nil, err
			}
			serviceResult[resourceName] = core.InAnyAZ(core.CapacityData{Capacity: uint64(value)})
		}
		result[serviceType] = serviceResult
	}
	return result, nil, nil
}

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	// not used by this plugin
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte, capacitorID string) error {
	// not used by this plugin
	return nil
}
