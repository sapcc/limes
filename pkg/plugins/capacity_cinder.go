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
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

type capacityCinderPlugin struct {
	cfg limes.CapacitorConfiguration
}

func init() {
	limes.RegisterCapacityPlugin(func(c limes.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) limes.CapacityPlugin {
		return &capacityCinderPlugin{c}
	})
}

//ID implements the limes.CapacityPlugin interface.
func (p *capacityCinderPlugin) ID() string {
	return "cinder"
}

//Scrape implements the limes.CapacityPlugin interface.
func (p *capacityCinderPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID string) (map[string]map[string]limes.CapacityData, error) {
	client, err := openstack.NewBlockStorageV2(provider, eo)
	if err != nil {
		return nil, err
	}

	var result gophercloud.Result

	//Get absolute limits for a tenant
	url := client.ServiceURL("scheduler-stats", "get_pools") + "?detail=True"
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	var limitData struct {
		Pools []struct {
			Name         string `json:"name"`
			Capabilities struct {
				TotalCapacity     util.Float64OrUnknown `json:"total_capacity_gb"`
				VolumeBackendName string                `json:"volume_backend_name"`
			} `json:"capabilities"`
		} `json:"pools"`
	}
	err = result.ExtractInto(&limitData)
	if err != nil {
		return nil, err
	}

	var totalCapacity uint64
	volumeBackendName := p.cfg.Cinder.VolumeBackendName

	//add results from scheduler-stats
	for _, element := range limitData.Pools {
		if (volumeBackendName != "") && (element.Capabilities.VolumeBackendName != volumeBackendName) {
			logg.Debug("Not considering %s with volume_backend_name %s", element.Name, element.Capabilities.VolumeBackendName)
		} else {
			totalCapacity += uint64(element.Capabilities.TotalCapacity)
			logg.Debug("Considering %s with volume_backend_name %s", element.Name, element.Capabilities.VolumeBackendName)
		}

	}

	return map[string]map[string]limes.CapacityData{
		"volumev2": {
			"capacity": limes.CapacityData{Capacity: totalCapacity},
			//NOTE: no estimates for no. of snapshots/volumes here; this depends highly on the backend
			//(on SAP CC, we configure capacity for snapshots/volumes via the "manual" capacitor)
		},
	}, nil

}
