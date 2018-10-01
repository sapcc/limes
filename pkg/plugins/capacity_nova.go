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
	"math"
	"regexp"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/limes"
)

type capacityNovaPlugin struct {
	cfg limes.CapacitorConfiguration
}

func init() {
	limes.RegisterCapacityPlugin(func(c limes.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) limes.CapacityPlugin {
		return &capacityNovaPlugin{c}
	})
}

func (p *capacityNovaPlugin) ID() string {
	return "nova"
}

//Scrape implements the limes.CapacityPlugin interface.
func (p *capacityNovaPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID string) (map[string]map[string]limes.CapacityData, error) {
	var hypervisorTypeRx *regexp.Regexp
	if p.cfg.Nova.HypervisorTypePattern != "" {
		var err error
		hypervisorTypeRx, err = regexp.Compile(p.cfg.Nova.HypervisorTypePattern)
		if err != nil {
			return nil, errors.New("invalid value for hypervisor_type: " + err.Error())
		}
	}

	client, err := openstack.NewComputeV2(provider, eo)
	if err != nil {
		return nil, err
	}

	var result gophercloud.Result

	//enumerate hypervisors
	url := client.ServiceURL("os-hypervisors", "detail")
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}
	var hypervisorData struct {
		Hypervisors []struct {
			Type     string `json:"hypervisor_type"`
			Vcpus    uint64 `json:"vcpus"`
			MemoryMb uint64 `json:"memory_mb"`
			LocalGb  uint64 `json:"local_gb"`
		} `json:"hypervisors"`
	}
	err = result.ExtractInto(&hypervisorData)
	if err != nil {
		return nil, err
	}

	//compute sum of cores and RAM for matching hypervisors
	var (
		totalVcpus    uint64
		totalMemoryMb uint64
		totalLocalGb  uint64
	)
	for _, hypervisor := range hypervisorData.Hypervisors {
		if hypervisorTypeRx != nil {
			if !hypervisorTypeRx.MatchString(hypervisor.Type) {
				continue
			}
		}

		totalVcpus += hypervisor.Vcpus
		totalMemoryMb += hypervisor.MemoryMb
		totalLocalGb += hypervisor.LocalGb
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
				logg.Debug("Failed to get extra specs for flavor: %s.", element.ID)
				return false, err
			}

			//necessary to be able to ignore huge baremetal flavors
			//consider only flavors as defined in extra specs
			var extraSpecs map[string]string
			if p.cfg.Nova.ExtraSpecs != nil {
				extraSpecs = p.cfg.Nova.ExtraSpecs
			}

			matches := true
			for key, value := range extraSpecs {
				if value != extras[key] {
					matches = false
					break
				}
			}
			if matches {
				logg.Debug("FlavorName: %s, FlavorID: %s, FlavorSize: %d GiB", element.Name, element.ID, element.Disk)
				maxFlavorSize = math.Max(maxFlavorSize, float64(element.Disk))
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
		if element.ZoneState.Available {
			azCount++
		}
	}

	//get overcommit factor from configuration (hypervisor stats unfortunately is
	//stupid and does not include this factor even though it is in the nova.conf)
	var vcpuOvercommitFactor uint64 = 1
	if p.cfg.Nova.VCPUOvercommitFactor != nil {
		vcpuOvercommitFactor = *p.cfg.Nova.VCPUOvercommitFactor
	}

	capacity := map[string]map[string]limes.CapacityData{
		"compute": {
			"cores": limes.CapacityData{Capacity: totalVcpus * vcpuOvercommitFactor},
			"ram":   limes.CapacityData{Capacity: totalMemoryMb},
		},
	}

	if maxFlavorSize != 0 {
		instanceCapacity := uint64(math.Min(float64(10000*azCount), float64(totalLocalGb)/maxFlavorSize))
		capacity["compute"]["instances"] = limes.CapacityData{Capacity: instanceCapacity}
	} else {
		logg.Error("Nova Capacity: Maximal flavor size is 0. Not reporting instances.")
	}

	return capacity, nil

}

//get flavor extra-specs
//result contains
//{ "vmware:hv_enabled" : 'True' }
//which identifies a VM flavor
func getFlavorExtras(client *gophercloud.ServiceClient, flavorUUID string) (map[string]string, error) {
	var result gophercloud.Result
	var extraSpecs struct {
		ExtraSpecs map[string]string `json:"extra_specs"`
	}

	url := client.ServiceURL("flavors", flavorUUID, "os-extra_specs")
	_, err := client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	err = result.ExtractInto(&extraSpecs)
	if err != nil {
		return nil, err
	}

	return extraSpecs.ExtraSpecs, nil
}
