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
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

type capacityCinderPlugin struct {
	cfg limes.CapacitorConfiguration
}

func init() {
	limes.RegisterCapacityPlugin(func(c limes.CapacitorConfiguration) limes.CapacityPlugin {
		return &capacityCinderPlugin{c}
	})
}

func (p *capacityCinderPlugin) Client(driver limes.Driver) (*gophercloud.ServiceClient, error) {
	return openstack.NewBlockStorageV2(driver.Client(),
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
}

func (p *capacityCinderPlugin) ID() string {
	return "cinder"
}

//Scrape implements the limes.CapacityPlugin interface.
func (p *capacityCinderPlugin) Scrape(driver limes.Driver) (map[string]map[string]uint64, error) {
	client, err := p.Client(driver)
	if err != nil {
		return nil, err
	}

	var result gophercloud.Result
	var volumeBackendName string

	if p.cfg.Cinder.VolumeBackendName != "" {
		volumeBackendName = p.cfg.Cinder.VolumeBackendName
	}

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

	//Get availability zones
	url = client.ServiceURL("os-availability-zone")
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}
	var availabilityZoneData struct {
		AvailabilityZoneInfo []struct {
			ZoneName  string `json:"zoneName"`
			ZoneState struct {
				Available bool `json:"available"`
			} `json:"zoneState"`
		} `json:"availabilityZoneInfo"`
	}
	err = result.ExtractInto(&availabilityZoneData)
	if err != nil {
		return nil, err
	}

	var totalCapacity, azCount uint64

	//add results from scheduler-stats
	for _, element := range limitData.Pools {
		if (volumeBackendName != "") && (element.Capabilities.VolumeBackendName == volumeBackendName) {
			totalCapacity += uint64(element.Capabilities.TotalCapacity)
			util.LogDebug("Considering %s with volume_backend_name %s", element.Name, element.Capabilities.VolumeBackendName)
		}
		util.LogDebug("Not considering %s with volume_backend_name %s", element.Name, element.Capabilities.VolumeBackendName)
	}

	//count availability zones
	for _, element := range availabilityZoneData.AvailabilityZoneInfo {
		if element.ZoneState.Available == true {
			azCount++
		}
	}

	//returns something like
	//"volumev2": {
	//	"capacity": capacitySum,
	//	"snapshots": 2500 * zoneCount,
	//	"volumes": 2500 * zoneCount,
	//}
	return map[string]map[string]uint64{
		"volumev2": {
			"capacity":  totalCapacity,
			"snapshots": uint64(2500 * azCount),
			"volumes":   uint64(2500 * azCount),
		},
	}, nil

}
