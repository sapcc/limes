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
	"fmt"
	"math"
	"regexp"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/hypervisors"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/core"
)

type capacityNovaPlugin struct {
	cfg      core.CapacitorConfiguration
	hvStates []novaHypervisorState
}

type novaHypervisorState struct {
	Name        string
	Hostname    string
	BelongsToAZ bool
}

func (s novaHypervisorState) Labels(clusterID string) prometheus.Labels {
	return prometheus.Labels{
		"os_cluster": clusterID,
		"hypervisor": s.Name,
		"hostname":   s.Hostname,
	}
}

func bool2float(val bool) float64 {
	if val {
		return 1
	}
	return 0
}

var (
	novaHypervisorHasAZGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "limes_nova_hypervisor_has_az",
			Help: "Whether the given hypervisor belongs to an availability zone.",
		},
		[]string{"os_cluster", "hypervisor", "hostname"},
	)
)

func init() {
	core.RegisterCapacityPlugin(func(c core.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) core.CapacityPlugin {
		return &capacityNovaPlugin{c, nil}
	})
	prometheus.MustRegister(novaHypervisorHasAZGauge)
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

	//enumerate hypervisors (cannot use type Hypervisor provided by Gophercloud;
	//in our clusters, it breaks because some hypervisor report unexpected NULL
	//values on fields that we are not even interested in)
	page, err := hypervisors.List(client).AllPages()
	if err != nil {
		return nil, err
	}
	var hypervisorData struct {
		Hypervisors []novaHypervisor `json:"hypervisors"`
	}
	err = page.(hypervisors.HypervisorPage).ExtractInto(&hypervisorData)
	if err != nil {
		return nil, err
	}

	//enumerate compute hosts to establish hypervisor <-> AZ mapping
	computeHostsPerAZ, err := getComputeHostsPerAZ(client)
	if err != nil {
		return nil, err
	}

	//when using the placement API, we need to enumerate resource providers once
	var resourceProviders []placementResourceProvider
	if p.cfg.Nova.UsePlacementAPI {
		placementClient, err := newPlacementClient(provider, eo)
		if err != nil {
			return nil, err
		}
		resourceProviders, err = placementClient.ListResourceProviders()
		if err != nil {
			return nil, err
		}
	}

	//compute sum of cores and RAM for matching hypervisors
	var (
		totalVcpus    uint64
		totalMemoryMb uint64
		totalLocalGb  uint64

		azCapacities = make(map[string]*partialNovaCapacity)

		hvStates []novaHypervisorState
	)

	for _, hypervisor := range hypervisorData.Hypervisors {
		if hypervisorTypeRx != nil {
			if !hypervisorTypeRx.MatchString(hypervisor.HypervisorType) {
				continue
			}
		}

		var hvCapacity partialNovaCapacity
		if p.cfg.Nova.UsePlacementAPI {
			hvCapacity, err = hypervisor.getCapacityViaPlacementAPI(provider, eo, resourceProviders)
			if err != nil {
				logg.Error("cannot get capacity for hypervisor %d (%s) with .service.host %q from Placement API (falling back to Nova Hypervisor API): %s",
					hypervisor.ID, hypervisor.HypervisorHostname, hypervisor.Service.Host,
					err.Error(),
				)
				hvCapacity = hypervisor.getCapacityViaNovaAPI()
			}
		} else {
			hvCapacity = hypervisor.getCapacityViaNovaAPI()
		}

		totalVcpus += hvCapacity.VCPUs.Capacity
		totalMemoryMb += hvCapacity.MemoryMB.Capacity
		totalLocalGb += hvCapacity.LocalGB

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
		}
		hvStates = append(hvStates, novaHypervisorState{
			Name:        hypervisor.Service.Host,
			Hostname:    hypervisor.HypervisorHostname,
			BelongsToAZ: hypervisorAZ != "unknown",
		})

		if _, ok := azCapacities[hypervisorAZ]; !ok {
			azCapacities[hypervisorAZ] = &partialNovaCapacity{}
		}
		azCapacities[hypervisorAZ].VCPUs.Capacity += hvCapacity.VCPUs.Capacity
		azCapacities[hypervisorAZ].VCPUs.Usage += hvCapacity.VCPUs.Usage
		azCapacities[hypervisorAZ].MemoryMB.Capacity += hvCapacity.MemoryMB.Capacity
		azCapacities[hypervisorAZ].MemoryMB.Usage += hvCapacity.MemoryMB.Usage
		azCapacities[hypervisorAZ].LocalGB += hvCapacity.LocalGB
		azCapacities[hypervisorAZ].RunningVMs += hvCapacity.RunningVMs
	}

	//commit changes to hypervisor metrics
	for _, state := range hvStates {
		novaHypervisorHasAZGauge.With(state.Labels(clusterID)).Set(bool2float(state.BelongsToAZ))
	}
	for _, state := range p.hvStates {
		isDeleted := true
		for _, otherState := range hvStates {
			if state.Name == otherState.Name && state.Hostname == otherState.Hostname {
				isDeleted = false
				break
			}
		}
		if isDeleted {
			novaHypervisorHasAZGauge.Delete(state.Labels(clusterID))
		}
	}
	p.hvStates = hvStates

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

	capacity := map[string]map[string]core.CapacityData{
		"compute": {
			"cores": core.CapacityData{
				Capacity:      totalVcpus,
				CapacityPerAZ: make(map[string]*core.CapacityDataForAZ, len(azCapacities)),
			},
			"instances": core.CapacityData{
				Capacity:      calculateInstanceAmount(len(computeHostsPerAZ), totalLocalGb, maxFlavorSize),
				CapacityPerAZ: make(map[string]*core.CapacityDataForAZ, len(azCapacities)),
			},
			"ram": core.CapacityData{
				Capacity:      totalMemoryMb,
				CapacityPerAZ: make(map[string]*core.CapacityDataForAZ, len(azCapacities)),
			},
		},
	}

	for azName, azCapa := range azCapacities {
		capacity["compute"]["cores"].CapacityPerAZ[azName] = &azCapa.VCPUs
		capacity["compute"]["instances"].CapacityPerAZ[azName] = &core.CapacityDataForAZ{
			Capacity: calculateInstanceAmount(1, azCapa.LocalGB, maxFlavorSize),
			Usage:    azCapa.RunningVMs,
		}
		capacity["compute"]["ram"].CapacityPerAZ[azName] = &azCapa.MemoryMB
	}

	if maxFlavorSize == 0 {
		logg.Error("Nova Capacity: Maximal flavor size is 0. Not reporting instances.")
		delete(capacity["compute"], "instances")
	}
	return capacity, nil
}

type novaHypervisor struct {
	ID                 int                 `json:"id"`
	HypervisorHostname string              `json:"hypervisor_hostname"`
	HypervisorType     string              `json:"hypervisor_type"`
	LocalGB            uint64              `json:"local_gb"`
	MemoryMB           uint64              `json:"memory_mb"`
	MemoryMBUsed       uint64              `json:"memory_mb_used"`
	RunningVMs         uint64              `json:"running_vms"`
	Service            hypervisors.Service `json:"service"`
	VCPUs              uint64              `json:"vcpus"`
	VCPUsUsed          uint64              `json:"vcpus_used"`
}

//The capacity for either a single AZ or a single hypervisor.
type partialNovaCapacity struct {
	VCPUs      core.CapacityDataForAZ
	MemoryMB   core.CapacityDataForAZ
	LocalGB    uint64
	RunningVMs uint64
}

func (h novaHypervisor) getCapacityViaNovaAPI() partialNovaCapacity {
	//When only using the Nova API, we already have all the information we need
	//from the hypervisors.List() call where we got this object.
	return partialNovaCapacity{
		VCPUs: core.CapacityDataForAZ{
			Capacity: h.VCPUs,
			Usage:    h.VCPUsUsed,
		},
		MemoryMB: core.CapacityDataForAZ{
			Capacity: h.MemoryMB,
			Usage:    h.MemoryMBUsed,
		},
		LocalGB:    h.LocalGB,
		RunningVMs: h.RunningVMs,
	}
}

func (h novaHypervisor) getCapacityViaPlacementAPI(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, resourceProviders []placementResourceProvider) (partialNovaCapacity, error) {
	//find the resource provider that corresponds to this hypervisor
	var providerID string
	for _, rp := range resourceProviders {
		if rp.Name == h.HypervisorHostname {
			providerID = rp.ID
			break
		}
	}
	if providerID == "" {
		return partialNovaCapacity{}, fmt.Errorf(
			"cannot find resource provider with name %q", h.HypervisorHostname)
	}

	//collect data about that resource provider from the Placement API
	client, err := newPlacementClient(provider, eo)
	if err != nil {
		return partialNovaCapacity{}, err
	}
	inventory, err := client.GetInventory(providerID)
	if err != nil {
		return partialNovaCapacity{}, err
	}
	usages, err := client.GetUsages(providerID)
	if err != nil {
		return partialNovaCapacity{}, err
	}

	return partialNovaCapacity{
		VCPUs: core.CapacityDataForAZ{
			Capacity: inventory["VCPU"].UsableCapacity(),
			Usage:    usages["VCPU"],
		},
		MemoryMB: core.CapacityDataForAZ{
			Capacity: inventory["MEMORY_MB"].UsableCapacity(),
			Usage:    usages["MEMORY_MB"],
		},
		LocalGB:    inventory["DISK_GB"].UsableCapacity(),
		RunningVMs: h.RunningVMs,
	}, nil
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
			AvailabilityZone *string  `json:"availability_zone"`
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
		//ignore host aggregates that just give scheduling hints but which don't
		//contain an AZ assignment
		if aggr.AvailabilityZone != nil {
			az := *aggr.AvailabilityZone
			computeHostsPerAZ[az] = append(computeHostsPerAZ[az], aggr.Hosts...)
		}
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
	amount := 10000 * uint64(azCount)
	if maxFlavorSize != 0 {
		maxAmount := uint64(float64(localGb) / maxFlavorSize)
		if amount > maxAmount {
			return maxAmount
		}
	}
	return amount
}
