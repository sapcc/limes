/*******************************************************************************
*
* Copyright 2018 SAP SE
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
	"github.com/sapcc/limes/pkg/limes"
)

type capacityCFMPlugin struct {
	cfg limes.CapacitorConfiguration
}

func init() {
	limes.RegisterCapacityPlugin(func(c limes.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) limes.CapacityPlugin {
		return &capacityCFMPlugin{c}
	})
}

//ID implements the limes.CapacityPlugin interface.
func (p *capacityCFMPlugin) ID() string {
	return "cfm"
}

//Scrape implements the limes.CapacityPlugin interface.
func (p *capacityCFMPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID string) (map[string]map[string]limes.CapacityData, error) {
	client, err := newCFMClient(provider, eo)
	if err != nil {
		return nil, err
	}
	pools, err := client.ListPools()
	if err != nil {
		return nil, err
	}

	var totalCapacityBytes uint64
	for _, pool := range pools {
		totalCapacityBytes += pool.Capabilities.TotalCapacityBytes
	}

	return map[string]map[string]limes.CapacityData{
		"database": {
			"cfm_share_capacity": {
				Capacity: totalCapacityBytes,
			},
		},
	}, nil
}
