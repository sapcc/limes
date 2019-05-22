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
	"errors"
	"fmt"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/core"
)

type capacityCFMPlugin struct {
	cfg       core.CapacitorConfiguration
	projectID string
}

func init() {
	core.RegisterCapacityPlugin(func(c core.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) core.CapacityPlugin {
		return &capacityCFMPlugin{cfg: c}
	})
}

//Init implements the core.CapacityPlugin interface.
func (p *capacityCFMPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	p.projectID, err = getProjectIDForToken(provider, eo)
	return err
}

//ID implements the core.CapacityPlugin interface.
func (p *capacityCFMPlugin) ID() string {
	return "cfm"
}

//Scrape implements the core.CapacityPlugin interface.
func (p *capacityCFMPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID string) (map[string]map[string]core.CapacityData, error) {
	client, err := newCFMClient(provider, eo, p.projectID)
	if err != nil {
		return nil, err
	}
	pools, err := client.ListPools()
	if err != nil {
		return nil, err
	}

	//The CFM API is weird and sometimes returns the same pools multiple times.
	//We need to take precautions to avoid double-counting.
	sizesSeen := make(map[string]uint64)
	inconsistentData := false

	var totalCapacityBytes uint64
	for _, pool := range pools {
		idStr := fmt.Sprintf("%s/%s (%s)", pool.HostName, pool.Name, pool.Type)
		size := pool.Capabilities.TotalCapacityBytes

		if previousSize, exists := sizesSeen[idStr]; exists {
			//accept duplicate pools if they are *at least* consistent...
			if previousSize != size {
				//...but choke when multiple entries have different opinions about the size of the same pool
				inconsistentData = true
				logg.Error("CFM pool %s was reported as being both %d bytes and %d bytes large", previousSize, size)
			}
			continue
		}

		totalCapacityBytes += size
		sizesSeen[idStr] = size
	}

	if inconsistentData {
		return nil, errors.New("some pools were reported with inconsistent sizes; see errors above")
	}

	return map[string]map[string]core.CapacityData{
		"database": {
			"cfm_share_capacity": {
				Capacity: totalCapacityBytes,
			},
		},
	}, nil
}
