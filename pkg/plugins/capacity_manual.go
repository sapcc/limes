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
	"errors"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/limes/pkg/limes"
)

type capacityManualPlugin struct {
	cfg limes.CapacitorConfiguration
}

func init() {
	limes.RegisterCapacityPlugin(func(c limes.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) limes.CapacityPlugin {
		return &capacityManualPlugin{c}
	})
}

//ID implements the limes.CapacityPlugin interface.
func (p *capacityManualPlugin) ID() string {
	return "manual"
}

var errNoManualData = errors.New(`missing values for capacitor plugin "manual"`)

//Scrape implements the limes.CapacityPlugin interface.
func (p *capacityManualPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID string) (map[string]map[string]limes.CapacityData, error) {
	if p.cfg.Manual == nil {
		return nil, errNoManualData
	}

	result := make(map[string]map[string]limes.CapacityData)
	for serviceType, serviceData := range p.cfg.Manual {
		serviceResult := make(map[string]limes.CapacityData)
		for resourceName, capacity := range serviceData {
			serviceResult[resourceName] = limes.CapacityData{Capacity: capacity}
		}
		result[serviceType] = serviceResult
	}

	return result, nil
}
