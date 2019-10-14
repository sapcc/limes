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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
)

type capacityNovaPlugin struct {
	cfg core.CapacitorConfiguration
}

var novaUnmatchedHypervisorsGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_unmatched_nova_hypervisors",
		Help: "Number of available/active Ironic nodes without matching flavor.",
	},
	[]string{"os_cluster"},
)

func init() {
	core.RegisterCapacityPlugin(func(c core.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) core.CapacityPlugin {
		return &capacityNovaPlugin{c}
	})
	prometheus.MustRegister(novaUnmatchedHypervisorsGauge)
}

//Init implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

func (p *capacityNovaPlugin) ID() string {
	return "nova"
}

//Scrape implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID string) (map[string]map[string]core.CapacityData, error) {
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
			ID           int    `json:"id"`
			Type         string `json:"hypervisor_type"`
			Vcpus        uint64 `json:"vcpus"`
			VcpusUsed    uint64 `json:"vcpus_used"`
			MemoryMb     uint64 `json:"memory_mb"`
			MemoryMbUsed uint64 `json:"memory_mb_used"`
			LocalGb      uint64 `json:"local_gb"`
			RunningVms   uint64 `json:"running_vms"`
			Service      struct {
				Host string `json:"host"`
			} `json:"service"`
		} `json:"hypervisors"`
	}
	err = result.ExtractInto(&hypervisorData)
	if err != nil {
		return nil, err
	}

	computeHostsPerAZ, err := getComputeHostsPerAZ(client)
	if err != nil {
		return nil, err
	}

	//compute sum of cores and RAM for matching hypervisors
	var (
		totalVcpus    uint64
		totalMemoryMb uint64
		totalLocalGb  uint64

		localGbPerAZ    = make(map[string]uint64)
		runningVmsPerAZ = make(map[string]uint64)

		vcpusPerAZ    = make(limes.ClusterAvailabilityZoneReports)
		memoryMbPerAZ = make(limes.ClusterAvailabilityZoneReports)

		unmatchedCounter = 0
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

		var hypervisorAZ string
		for az, hosts := range computeHostsPerAZ {
			for _, v := range hosts {
				if hypervisor.Service.Host == v {
					hypervisorAZ = az
					break
				}
			}
		}
		if hypervisorAZ == "" {
			logg.Info("Hypervisor %d with .service.host %q does not match any hosts from host aggregates", hypervisor.ID, hypervisor.Service.Host)
			hypervisorAZ = "unknown"
			unmatchedCounter++
		}
		if _, ok := vcpusPerAZ[hypervisorAZ]; !ok {
			vcpusPerAZ[hypervisorAZ] = &limes.ClusterAvailabilityZoneReport{Name: hypervisorAZ}
			memoryMbPerAZ[hypervisorAZ] = &limes.ClusterAvailabilityZoneReport{Name: hypervisorAZ}
		}

		vcpusPerAZ[hypervisorAZ].Capacity += hypervisor.Vcpus
		vcpusPerAZ[hypervisorAZ].Usage += hypervisor.VcpusUsed

		memoryMbPerAZ[hypervisorAZ].Capacity += hypervisor.MemoryMb
		memoryMbPerAZ[hypervisorAZ].Usage += hypervisor.MemoryMbUsed

		localGbPerAZ[hypervisorAZ] += hypervisor.LocalGb
		runningVmsPerAZ[hypervisorAZ] += hypervisor.RunningVms
	}

	novaUnmatchedHypervisorsGauge.With(
		prometheus.Labels{"os_cluster": clusterID},
	).Set(float64(unmatchedCounter))

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

	//preserve the VCenter HA reserve, which is reported via Nova, but not accessible
	if multiplier := p.cfg.Nova.CPUMultiplier; multiplier != 0 {
		totalVcpus = uint64(float64(totalVcpus) * multiplier)
		for _, vcpus := range vcpusPerAZ {
			vcpus.Capacity = uint64(float64(vcpus.Capacity) * multiplier)
			vcpus.Usage = uint64(float64(vcpus.Usage) * multiplier)
		}
	}
	if multiplier := p.cfg.Nova.RAMMultiplier; multiplier != 0 {
		totalMemoryMb = uint64(float64(totalMemoryMb) * multiplier)
		for _, memoryMb := range memoryMbPerAZ {
			memoryMb.Capacity = uint64(float64(memoryMb.Capacity) * multiplier)
			memoryMb.Usage = uint64(float64(memoryMb.Usage) * multiplier)
		}
	}

	capacity := map[string]map[string]core.CapacityData{
		"compute": {
			"cores": core.CapacityData{Capacity: totalVcpus, CapacityPerAZ: vcpusPerAZ},
			"ram":   core.CapacityData{Capacity: totalMemoryMb, CapacityPerAZ: memoryMbPerAZ},
		},
	}

	azCount := len(computeHostsPerAZ)

	if maxFlavorSize != 0 {
		totalInstances := calculateInstanceAmount(azCount, totalLocalGb, maxFlavorSize)

		instancesPerAZ := make(limes.ClusterAvailabilityZoneReports)
		for az, localGb := range localGbPerAZ {
			instancesPerAZ[az] = &limes.ClusterAvailabilityZoneReport{
				Name:     az,
				Capacity: calculateInstanceAmount(1, localGb, maxFlavorSize),
				Usage:    runningVmsPerAZ[az],
			}
		}

		capacity["compute"]["instances"] = core.CapacityData{Capacity: totalInstances, CapacityPerAZ: instancesPerAZ}
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

func getComputeHostsPerAZ(client *gophercloud.ServiceClient) (map[string][]string, error) {
	var result gophercloud.Result
	var data struct {
		Aggregates []struct {
			AvailabilityZone string   `json:"availability_zone"`
			Hosts            []string `json:"hosts"`
		} `json:"aggregates"`
	}

	url := client.ServiceURL("os-aggregates")
	_, err := client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	err = result.ExtractInto(&data)
	if err != nil {
		return nil, err
	}

	computeHostsPerAZ := make(map[string][]string)
	for _, aggr := range data.Aggregates {
		computeHostsPerAZ[aggr.AvailabilityZone] = append(computeHostsPerAZ[aggr.AvailabilityZone], aggr.Hosts...)
	}
	//multiple aggregates can contain the same host which results in
	//duplicate host values per AZ
	for az, hosts := range computeHostsPerAZ {
		uniqueValues := make([]string, 0, len(hosts))
		isDuplicate := make(map[string]bool, len(hosts))
		for _, v := range hosts {
			if _, ok := isDuplicate[v]; !ok {
				uniqueValues = append(uniqueValues, v)
				isDuplicate[v] = true
			}
		}
		computeHostsPerAZ[az] = uniqueValues
	}

	return computeHostsPerAZ, nil
}

func calculateInstanceAmount(azCount int, localGb uint64, maxFlavorSize float64) uint64 {
	return uint64(math.Min(float64(10000*azCount), float64(localGb)/maxFlavorSize))
}
