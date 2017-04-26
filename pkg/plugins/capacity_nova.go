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
	"math"

	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

type capacityNovaPlugin struct {
	cfg limes.CapacitorConfiguration
}

type extraSpecs struct {
	ExtraSpecs map[string]string `json:"extra_specs"`
}

func init() {
	limes.RegisterCapacityPlugin(func(c limes.CapacitorConfiguration) limes.CapacityPlugin {
		return &capacityNovaPlugin{c}
	})
}

func (p *capacityNovaPlugin) Client(driver limes.Driver) (*gophercloud.ServiceClient, error) {
	return openstack.NewComputeV2(driver.Client(),
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
}

func (p *capacityNovaPlugin) ID() string {
	return "nova"
}

//Scrape implements the limes.CapacityPlugin interface.
func (p *capacityNovaPlugin) Scrape(driver limes.Driver) (map[string]map[string]uint64, error) {
	client, err := p.Client(driver)
	if err != nil {
		return nil, err
	}

	var result gophercloud.Result

	//Get absolute limits for a tenant
	url := client.ServiceURL("os-hypervisors", "statistics")
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}
	var hypervisorData struct {
		HypervisorStatistics struct {
			Vcpus    int `json:"vcpus"`
			MemoryMb int `json:"memory_mb"`
			LocalGb  int `json:"local_gb"`
		} `json:"hypervisor_statistics"`
	}
	err = result.ExtractInto(&hypervisorData)
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

	//list all flavors and get max(flavor_size)
	pages, maxFlavorSize := 0, 0.0
	err = flavors.ListDetail(client, nil).EachPage(func(page pagination.Page) (bool, error) {
		pages++
		f, err := flavors.ExtractFlavors(page)
		if err != nil {
			return false, err
		}
		for _, element := range f {
			extras, err := getFlavorExtras(client, element.ID)
			if err != nil {
				util.LogDebug("Failed to get extra specs for flavor: %s.", element.ID)
				return false, err
			}

			//necessary to be able to ignore huge baremetal flavors
			//consider only flavors as defined in extra specs
			var extraSpecs map[string]string
			if p.cfg.Nova.ExtraSpecs != nil {
				extraSpecs = p.cfg.Nova.ExtraSpecs
			}

			for key, value := range extraSpecs {
				if value == strings.Replace(extras.ExtraSpecs[key], "'", "", -1) {
					maxFlavorSize = math.Max(maxFlavorSize, float64(element.Disk))
				}
			}
		}

		return true, nil
	})
	if err != nil {
		return nil, err
	}

	var azCount int

	//count availability zones
	for _, element := range availabilityZoneData.AvailabilityZoneInfo {
		if element.ZoneState.Available == true {
			azCount++
		}
	}

	//get overcommit factor from configuration (hypervisor stats unfortunately is
	//stupid and does not include this factor even though it is in the nova.conf)
	var vcpuOvercommitFactor uint64 = 1
	if p.cfg.Nova.VCPUOvercommitFactor != nil {
		vcpuOvercommitFactor = *p.cfg.Nova.VCPUOvercommitFactor
	}

	//returns something like
	//"volumev2": {
	//	"cores": total_vcpus,
	//	"instances": min(10000 per Availability Zone, local_gb/max(flavor size)),
	//	"ram": total_memory_mb,
	//}
	return map[string]map[string]uint64{
		"compute": {
			"cores":     uint64(hypervisorData.HypervisorStatistics.Vcpus) * vcpuOvercommitFactor,
			"instances": uint64(math.Min(float64(10000*azCount), float64(hypervisorData.HypervisorStatistics.LocalGb)/maxFlavorSize)),
			"ram":       uint64(hypervisorData.HypervisorStatistics.MemoryMb),
		},
	}, nil

}

//get flavor extra-specs
//result contains
//{ "vmware:hv_enabled" : 'True' }
//which identifies a VM flavor
func getFlavorExtras(client *gophercloud.ServiceClient, flavorUUID string) (*extraSpecs, error) {
	var result gophercloud.Result
	var extraSpecsData extraSpecs

	url := client.ServiceURL("flavors", flavorUUID, "os-extra_specs")
	_, err := client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	err = result.ExtractInto(&extraSpecsData)
	if err != nil {
		return nil, err
	}

	return &extraSpecsData, nil
}
