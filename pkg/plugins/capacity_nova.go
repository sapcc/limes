/*******************************************************************************
*
* Copyright 2017-2021 SAP SE
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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/aggregates"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/hypervisors"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/openstack/placement/v1/resourceproviders"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/pkg/core"
)

type capacityNovaPlugin struct {
	cfg                       core.CapacitorConfiguration
	reportSubcapaForCores     bool
	reportSubcapaForInstances bool
	reportSubcapaForRAM       bool
}

type capacityNovaSerializedMetrics struct {
	Hypervisors []novaHypervisorMetrics `json:"hypervisors"`
}

type novaHypervisorMetrics struct {
	Name        string `json:"name"`
	Hostname    string `json:"hostname"`
	BelongsToAZ bool   `json:"belongs_to_az"`
}

func bool2float(val bool) float64 {
	if val {
		return 1
	}
	return 0
}

func init() {
	core.RegisterCapacityPlugin(func(c core.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) core.CapacityPlugin {
		return &capacityNovaPlugin{
			cfg:                       c,
			reportSubcapaForCores:     scrapeSubcapacities["compute"]["cores"],
			reportSubcapaForInstances: scrapeSubcapacities["compute"]["instances"],
			reportSubcapaForRAM:       scrapeSubcapacities["compute"]["ram"],
		}
	})
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

func (p *capacityNovaPlugin) Type() string {
	return "nova"
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (result map[string]map[string]core.CapacityData, serializedMetrics string, err error) {
	var hypervisorTypeRx *regexp.Regexp
	if p.cfg.Nova.HypervisorTypePattern != "" {
		var err error
		hypervisorTypeRx, err = regexp.Compile(p.cfg.Nova.HypervisorTypePattern)
		if err != nil {
			return nil, "", errors.New("invalid value for hypervisor_type: " + err.Error())
		}
	}

	novaClient, err := openstack.NewComputeV2(provider, eo)
	if err != nil {
		return nil, "", err
	}

	//enumerate hypervisors (cannot use type Hypervisor provided by Gophercloud;
	//in our clusters, it breaks because some hypervisor report unexpected NULL
	//values on fields that we are not even interested in)
	page, err := hypervisors.List(novaClient, nil).AllPages()
	if err != nil {
		return nil, "", err
	}
	var hypervisorData struct {
		Hypervisors []novaHypervisor `json:"hypervisors"`
	}
	err = page.(hypervisors.HypervisorPage).ExtractInto(&hypervisorData)
	if err != nil {
		return nil, "", err
	}

	//enumerate compute hosts to establish hypervisor <-> AZ mapping
	azs, aggrs, err := getAggregates(novaClient)
	if err != nil {
		return nil, "", err
	}

	//when using the placement API, we need to enumerate resource providers once
	var allResourceProviders []resourceproviders.ResourceProvider
	if p.cfg.Nova.UsePlacementAPI {
		client, err := openstack.NewPlacementV1(provider, eo)
		if err != nil {
			return nil, "", err
		}
		allPages, err := resourceproviders.List(client, nil).AllPages()
		if err != nil {
			return nil, "", err
		}
		allResourceProviders, err = resourceproviders.ExtractResourceProviders(allPages)
		if err != nil {
			return nil, "", err
		}
	}

	//compute sum of cores and RAM for matching hypervisors
	var (
		total     partialNovaCapacity
		hvMetrics []novaHypervisorMetrics
	)

	for _, hypervisor := range hypervisorData.Hypervisors {
		if hypervisorTypeRx != nil {
			if !hypervisorTypeRx.MatchString(hypervisor.HypervisorType) {
				continue
			}
		}

		var hvCapacity partialNovaCapacity
		if p.cfg.Nova.UsePlacementAPI {
			hvCapacity, err = hypervisor.getCapacityViaPlacementAPI(provider, eo, allResourceProviders)
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

		logg.Debug("Nova hypervisor %d (%s) with .service.host %q reports capacity: %d CPUs, %d MiB RAM, %d GiB disk",
			hypervisor.ID, hypervisor.HypervisorHostname, hypervisor.Service.Host,
			hvCapacity.VCPUs.Capacity, hvCapacity.MemoryMB.Capacity, hvCapacity.LocalGB,
		)

		total.Add(hvCapacity)
		for _, aggr := range aggrs {
			if aggr.ContainsComputeHost[hypervisor.Service.Host] {
				aggr.HypervisorCount++
				aggr.Capacity.Add(hvCapacity)
			}
		}

		var hypervisorAZ string
		for azName, az := range azs {
			if az.ContainsComputeHost[hypervisor.Service.Host] {
				hypervisorAZ = azName
				az.HypervisorCount++
				az.Capacity.Add(hvCapacity)
				break
			}
		}
		if hypervisorAZ == "" {
			logg.Info("Hypervisor %d with .service.host %q does not match any hosts from host aggregates", hypervisor.ID, hypervisor.Service.Host)
			hypervisorAZ = "unknown"
		}
		hvMetrics = append(hvMetrics, novaHypervisorMetrics{
			Name:        hypervisor.Service.Host,
			Hostname:    hypervisor.HypervisorHostname,
			BelongsToAZ: hypervisorAZ != "unknown",
		})
	}

	//serialize hypervisor metrics
	serializedMetricsBytes, err := json.Marshal(capacityNovaSerializedMetrics{hvMetrics})
	if err != nil {
		return nil, "", err
	}

	//list all flavors and get max(flavor_size)
	pages, maxFlavorSize := 0, 0.0
	err = flavors.ListDetail(novaClient, nil).EachPage(func(page pagination.Page) (bool, error) {
		pages++
		f, err := flavors.ExtractFlavors(page)
		if err != nil {
			return false, err
		}
		for _, element := range f {
			extras, err := flavors.ListExtraSpecs(novaClient, element.ID).Extract()
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
		return nil, "", err
	}

	collectSubcapacitiesIf := func(cond bool, getCapa func(*novaHypervisorGroup) *core.CapacityDataForAZ) []interface{} {
		if !cond {
			return nil
		}
		var result []interface{}
		for _, aggr := range aggrs {
			if aggr.HypervisorCount > 0 {
				capa := getCapa(aggr)
				result = append(result, novaAggregateSubcapacity{
					Name:     aggr.Name,
					Metadata: aggr.Metadata,
					Capacity: capa.Capacity,
					Usage:    capa.Usage,
				})
			}
		}
		return result
	}

	capacity := map[string]map[string]core.CapacityData{
		"compute": {
			"cores": core.CapacityData{
				Capacity:      total.VCPUs.Capacity,
				CapacityPerAZ: make(map[string]*core.CapacityDataForAZ, len(azs)),
				Subcapacities: collectSubcapacitiesIf(p.reportSubcapaForCores,
					func(aggr *novaHypervisorGroup) *core.CapacityDataForAZ {
						return &aggr.Capacity.VCPUs
					},
				),
			},
			"instances": core.CapacityData{
				Capacity:      total.GetInstanceCapacity(len(azs), maxFlavorSize).Capacity,
				CapacityPerAZ: make(map[string]*core.CapacityDataForAZ, len(azs)),
				Subcapacities: collectSubcapacitiesIf(p.reportSubcapaForInstances,
					func(aggr *novaHypervisorGroup) *core.CapacityDataForAZ {
						return aggr.Capacity.GetInstanceCapacity(1, maxFlavorSize)
					},
				),
			},
			"ram": core.CapacityData{
				Capacity:      total.MemoryMB.Capacity,
				CapacityPerAZ: make(map[string]*core.CapacityDataForAZ, len(azs)),
				Subcapacities: collectSubcapacitiesIf(p.reportSubcapaForRAM,
					func(aggr *novaHypervisorGroup) *core.CapacityDataForAZ {
						return &aggr.Capacity.MemoryMB
					},
				),
			},
		},
	}

	for azName, az := range azs {
		azCapa := az.Capacity
		capacity["compute"]["cores"].CapacityPerAZ[azName] = &azCapa.VCPUs
		capacity["compute"]["instances"].CapacityPerAZ[azName] = azCapa.GetInstanceCapacity(1, maxFlavorSize)
		capacity["compute"]["ram"].CapacityPerAZ[azName] = &azCapa.MemoryMB
	}

	if maxFlavorSize == 0 {
		logg.Error("Nova Capacity: Maximal flavor size is 0. Not reporting instances.")
		delete(capacity["compute"], "instances")
	}
	return capacity, string(serializedMetricsBytes), nil
}

var novaHypervisorHasAZGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_nova_hypervisor_has_az",
		Help: "Whether the given hypervisor belongs to an availability zone.",
	},
	[]string{"os_cluster", "hypervisor", "hostname"},
)

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	novaHypervisorHasAZGauge.Describe(ch)
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID, serializedMetrics string) error {
	if serializedMetrics == "" {
		return nil
	}
	var metrics capacityNovaSerializedMetrics
	err := json.Unmarshal([]byte(serializedMetrics), &metrics)
	if err != nil {
		return err
	}

	descCh := make(chan *prometheus.Desc, 1)
	novaHypervisorHasAZGauge.Describe(descCh)
	novaHypervisorHasAZDesc := <-descCh

	for _, hv := range metrics.Hypervisors {
		ch <- prometheus.MustNewConstMetric(
			novaHypervisorHasAZDesc,
			prometheus.GaugeValue, bool2float(hv.BelongsToAZ),
			clusterID, hv.Name, hv.Hostname,
		)
	}
	return nil
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

// The capacity of any level of the Nova superstructure (hypervisor, aggregate, AZ, cluster).
type partialNovaCapacity struct {
	VCPUs      core.CapacityDataForAZ
	MemoryMB   core.CapacityDataForAZ
	LocalGB    uint64
	RunningVMs uint64
}

func (c *partialNovaCapacity) Add(hvCapacity partialNovaCapacity) {
	c.VCPUs.Capacity += hvCapacity.VCPUs.Capacity
	c.VCPUs.Usage += hvCapacity.VCPUs.Usage
	c.MemoryMB.Capacity += hvCapacity.MemoryMB.Capacity
	c.MemoryMB.Usage += hvCapacity.MemoryMB.Usage
	c.LocalGB += hvCapacity.LocalGB
	c.RunningVMs += hvCapacity.RunningVMs
}

func (c *partialNovaCapacity) GetInstanceCapacity(azCount int, maxFlavorSize float64) *core.CapacityDataForAZ {
	amount := 10000 * uint64(azCount)
	if maxFlavorSize != 0 {
		maxAmount := uint64(float64(c.LocalGB) / maxFlavorSize)
		if amount > maxAmount {
			amount = maxAmount
		}
	}
	return &core.CapacityDataForAZ{
		Capacity: amount,
		Usage:    c.RunningVMs,
	}
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

func (h novaHypervisor) getCapacityViaPlacementAPI(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, resourceProviders []resourceproviders.ResourceProvider) (partialNovaCapacity, error) {
	//find the resource provider that corresponds to this hypervisor
	var providerID string
	for _, rp := range resourceProviders {
		if rp.Name == h.HypervisorHostname {
			providerID = rp.UUID
			break
		}
	}
	if providerID == "" {
		return partialNovaCapacity{}, fmt.Errorf(
			"cannot find resource provider with name %q", h.HypervisorHostname)
	}

	//collect data about that resource provider from the Placement API
	client, err := openstack.NewPlacementV1(provider, eo)
	if err != nil {
		return partialNovaCapacity{}, err
	}
	inventory, err := resourceproviders.GetInventories(client, providerID).Extract()
	if err != nil {
		return partialNovaCapacity{}, err
	}
	usages, err := resourceproviders.GetUsages(client, providerID).Extract()
	if err != nil {
		return partialNovaCapacity{}, err
	}

	return partialNovaCapacity{
		VCPUs: core.CapacityDataForAZ{
			Capacity: uint64(inventory.Inventories["VCPU"].Total - inventory.Inventories["VCPU"].Reserved),
			Usage:    uint64(usages.Usages["VCPU"]),
		},
		MemoryMB: core.CapacityDataForAZ{
			Capacity: uint64(inventory.Inventories["MEMORY_MB"].Total - inventory.Inventories["MEMORY_MB"].Reserved),
			Usage:    uint64(usages.Usages["MEMORY_MB"]),
		},
		LocalGB:    uint64(inventory.Inventories["DISK_GB"].Total - inventory.Inventories["DISK_GB"].Reserved),
		RunningVMs: h.RunningVMs,
	}, nil
}

// novaHypervisorGroup is any group of hypervisors. We use hypervisor groups to model aggregates, AZs, as well as the entire cluster.
type novaHypervisorGroup struct {
	Name                string
	Metadata            map[string]string //only used for aggregates
	ContainsComputeHost map[string]bool   //only used for aggregates and AZs
	HypervisorCount     uint64
	Capacity            partialNovaCapacity
}

type novaAggregateSubcapacity struct {
	Name     string            `json:"name"`
	Metadata map[string]string `json:"metadata"`
	Capacity uint64            `json:"capacity"`
	Usage    uint64            `json:"usage"`
}

func getAggregates(client *gophercloud.ServiceClient) (availabilityZones, collectedAggregates map[string]*novaHypervisorGroup, err error) {
	allPages, err := aggregates.List(client).AllPages()
	if err != nil {
		return nil, nil, err
	}
	allAggregates, err := aggregates.ExtractAggregates(allPages)
	if err != nil {
		return nil, nil, err
	}

	availabilityZones = make(map[string]*novaHypervisorGroup)
	collectedAggregates = make(map[string]*novaHypervisorGroup)
	for _, apiAggregate := range allAggregates {
		//never show `metadata: null` on the API for subcapacities
		if apiAggregate.Metadata == nil {
			apiAggregate.Metadata = make(map[string]string)
		}

		//create one `novaHypervisorGroup` per aggregate
		aggr := &novaHypervisorGroup{
			Name:                apiAggregate.Name,
			Metadata:            apiAggregate.Metadata,
			ContainsComputeHost: make(map[string]bool, len(apiAggregate.Hosts)),
		}
		for _, host := range apiAggregate.Hosts {
			aggr.ContainsComputeHost[host] = true
		}
		collectedAggregates[aggr.Name] = aggr

		//create one pseudo-aggregate per AZ
		if apiAggregate.AvailabilityZone == "" {
			continue
		}
		azName := apiAggregate.AvailabilityZone
		az := availabilityZones[azName]
		if az == nil {
			az = &novaHypervisorGroup{
				Name:                azName,
				ContainsComputeHost: make(map[string]bool, len(apiAggregate.Hosts)),
			}
			availabilityZones[azName] = az
		}
		for _, host := range apiAggregate.Hosts {
			az.ContainsComputeHost[host] = true
		}
	}

	return
}
