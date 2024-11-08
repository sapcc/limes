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
	"context"
	"errors"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

type capacityManualPlugin struct {
	Values map[db.ServiceType]map[liquid.ResourceName]uint64 `yaml:"values"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityManualPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityManualPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacityManualPlugin) PluginTypeID() string {
	return "manual"
}

var errNoManualData = errors.New(`missing values for capacitor plugin "manual"`)

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityManualPlugin) Scrape(ctx context.Context, _ core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (result map[db.ServiceType]map[liquid.ResourceName]core.PerAZ[core.CapacityData], _ []byte, err error) {
	if p.Values == nil {
		return nil, nil, errNoManualData
	}

	result = make(map[db.ServiceType]map[liquid.ResourceName]core.PerAZ[core.CapacityData])
	for serviceType, serviceData := range p.Values {
		serviceResult := make(map[liquid.ResourceName]core.PerAZ[core.CapacityData])
		for resourceName, capacity := range serviceData {
			serviceResult[resourceName] = core.InAnyAZ(core.CapacityData{Capacity: capacity})
		}
		result[serviceType] = serviceResult
	}

	return result, nil, nil
}

func (p *capacityManualPlugin) BuildServiceCapacityRequest(backchannel core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (liquid.ServiceCapacityRequest, error) {
	return liquid.ServiceCapacityRequest{}, core.ErrNotALiquid
}

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityManualPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	// not used by this plugin
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityManualPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte, capacitorID string) error {
	// not used by this plugin
	return nil
}
