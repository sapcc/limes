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

	"github.com/sapcc/limes/pkg/core"
)

type capacityPrometheusPlugin struct {
	APIConfig core.PrometheusAPIConfiguration `yaml:"api"`
	Queries   map[string]map[string]string    `yaml:"queries"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityPrometheusPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubcapacities map[string]map[string]bool) error {
	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) PluginTypeID() string {
	return "prometheus"
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (result map[string]map[string]core.CapacityData, _ string, err error) {
	client, err := prometheusClient(p.APIConfig)
	if err != nil {
		return nil, "", err
	}

	result = make(map[string]map[string]core.CapacityData)
	for serviceType, queries := range p.Queries {
		serviceResult := make(map[string]core.CapacityData)
		for resourceName, query := range queries {
			value, err := prometheusGetSingleValue(client, query, nil)
			if err != nil {
				return nil, "", err
			}
			serviceResult[resourceName] = core.CapacityData{Capacity: uint64(value)}
		}
		result[serviceType] = serviceResult
	}
	return result, "", nil
}

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID, serializedMetrics string) error {
	//not used by this plugin
	return nil
}
