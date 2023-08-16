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
	"slices"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/aggregates"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/hypervisors"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/openstack/placement/v1/resourceproviders"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/regexpext"

	"github.com/sapcc/limes/internal/core"
)

type capacityNovaPlugin struct {
	//configuration
	AggregateNameRx          regexpext.PlainRegexp `yaml:"aggregate_name_pattern"`
	MaxInstancesPerAggregate uint64                `yaml:"max_instances_per_aggregate"`
	ExtraSpecs               map[string]string     `yaml:"extra_specs"`
	HypervisorTypeRx         regexpext.PlainRegexp `yaml:"hypervisor_type_pattern"`
	//computed state
	reportSubcapacities map[string]bool `yaml:"-"`
	//connections
	NovaV2      *gophercloud.ServiceClient `yaml:"-"`
	PlacementV1 *gophercloud.ServiceClient `yaml:"-"`
}

type capacityNovaSerializedMetrics struct {
	Hypervisors []novaHypervisorMetrics `json:"hv"`
}

type novaHypervisorMetrics struct {
	Name              string   `json:"n"`
	Hostname          string   `json:"hn"`
	Aggregates        []string `json:"ag"`
	AvailabilityZones []string `json:"az"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityNovaPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubcapacities map[string]map[string]bool) (err error) {
	p.reportSubcapacities = scrapeSubcapacities["compute"]

	if p.AggregateNameRx == "" {
		return errors.New("missing value for nova.aggregate_name_pattern")
	}
	if p.MaxInstancesPerAggregate == 0 {
		return errors.New("missing value for nova.max_instances_per_aggregate")
	}

	p.NovaV2, err = openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}
	p.PlacementV1, err = openstack.NewPlacementV1(provider, eo)
	if err != nil {
		return err
	}
	p.PlacementV1.Microversion = "1.6" //for traits endpoint

	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) PluginTypeID() string {
	return "nova"
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) Scrape() (result map[string]map[string]core.CapacityData, serializedMetrics string, err error) {
	//enumerate aggregates which establish the hypervisor <-> AZ mapping
	page, err := aggregates.List(p.NovaV2).AllPages()
	if err != nil {
		return nil, "", err
	}
	allAggregates, err := aggregates.ExtractAggregates(page)
	if err != nil {
		return nil, "", err
	}

	//enumerate hypervisors (cannot use type Hypervisor provided by Gophercloud;
	//in our clusters, it breaks because some hypervisor report unexpected NULL
	//values on fields that we are not even interested in)
	page, err = hypervisors.List(p.NovaV2, nil).AllPages()
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

	//enumerate resource providers (we need to match these to the hypervisors later)
	page, err = resourceproviders.List(p.PlacementV1, nil).AllPages()
	if err != nil {
		return nil, "", err
	}
	allResourceProviders, err := resourceproviders.ExtractResourceProviders(page)
	if err != nil {
		return nil, "", err
	}

	//for the instances capacity, we need to know the max root disk size on public flavors
	maxRootDiskSize, err := getMaxRootDiskSize(p.NovaV2, p.ExtraSpecs)
	if err != nil {
		return nil, "", err
	}

	//we need to prepare several aggregations in the big loop below
	var (
		resourceNames   = []string{"cores", "instances", "ram"}
		totalCapacity   partialNovaCapacity
		azCapacities    = make(map[string]*partialNovaCapacity)
		hvSubcapacities = make(map[string][]any)
	)

	//foreach hypervisor...
	var metrics capacityNovaSerializedMetrics
	for _, hypervisor := range hypervisorData.Hypervisors {
		//ignore hypervisor if excluded by HypervisorTypePattern
		if !p.HypervisorTypeRx.MatchString(hypervisor.HypervisorType) {
			//NOTE: If no pattern was given, the regex will be empty and thus always match.
			continue
		}

		//query Placement API for hypervisor capacity
		hvCapacity, traits, err := hypervisor.getCapacity(p.PlacementV1, allResourceProviders)
		if err != nil {
			return nil, "", fmt.Errorf(
				"cannot get capacity for hypervisor %s with .service.host %q from Placement API (falling back to Nova Hypervisor API): %s",
				hypervisor.HypervisorHostname, hypervisor.Service.Host, err.Error())
		}
		logg.Debug("Nova hypervisor %s with .service.host %q reports capacity: %d CPUs, %d MiB RAM, %d GiB disk",
			hypervisor.HypervisorHostname, hypervisor.Service.Host,
			hvCapacity.VCPUs.Capacity, hvCapacity.MemoryMB.Capacity, hvCapacity.LocalGB,
		)

		//ignore hypervisor that is about to be decommissioned
		//(no domain quota should be given out for its capacity anymore)
		if slices.Contains(traits, "CUSTOM_DECOMMISSIONING") {
			logg.Info("ignoring Nova hypervisor %s with .service.host %q because of CUSTOM_DECOMMISSIONING trait",
				hypervisor.HypervisorHostname, hypervisor.Service.Host,
			)
			continue
		}

		//ignore hypervisor without installed capacity (this often happens during buildup before the hypervisor is set live)
		if hvCapacity.IsEmpty() {
			continue
		}

		//match hypervisor with AZ and relevant aggregate
		matchingAZs := make(map[string]bool)
		matchingAggregates := make(map[string]bool)
		for _, aggr := range allAggregates {
			if !hypervisor.IsInAggregate(aggr) {
				continue
			}
			if p.AggregateNameRx.MatchString(aggr.Name) {
				matchingAggregates[aggr.Name] = true
			}
			if az := aggr.AvailabilityZone; az != "" {
				//We also use aggregates not matching our naming pattern to establish a
				//hypervisor <-> AZ relationship. We have observed in the wild that
				//matching aggregates do not always have their AZ field maintained.
				matchingAZs[az] = true
			}
		}

		//report wellformed-ness of this HV via metrics
		metrics.Hypervisors = append(metrics.Hypervisors, novaHypervisorMetrics{
			Name:              hypervisor.Service.Host,
			Hostname:          hypervisor.HypervisorHostname,
			Aggregates:        boolMapToList(matchingAggregates),
			AvailabilityZones: boolMapToList(matchingAZs),
		})

		//the mapping from hypervisor to aggregate/AZ must be unique (otherwise the
		//capacity will be counted either not at all or multiple times)
		if len(matchingAggregates) == 0 {
			//This is not a fatal error: During buildup, new hypervisors may not be
			//mapped to an aggregate to prevent scheduling of instances onto them -
			//we just log an error and ignore this hypervisor's capacity.
			logg.Error(
				"hypervisor %s with .service.host %q does not belong to any matching aggregates",
				hypervisor.HypervisorHostname, hypervisor.Service.Host)
			continue
		}
		if len(matchingAggregates) > 1 {
			return nil, "", fmt.Errorf(
				"hypervisor %s with .service.host %q could not be uniquely matched to an aggregate (matching aggregates = %v)",
				hypervisor.HypervisorHostname, hypervisor.Service.Host, matchingAggregates)
		}
		if len(matchingAZs) == 0 {
			//This is not a fatal error: During buildup, new aggregates will not be
			//mapped to an AZ to prevent scheduling of instances onto them - we just
			//log an error and ignore this aggregate's capacity.
			logg.Error(
				"hypervisor %s with .service.host %q could not be matched to any AZ (matching aggregates = %v)",
				hypervisor.HypervisorHostname, hypervisor.Service.Host, matchingAggregates)
			continue
		}
		if len(matchingAZs) > 1 {
			return nil, "", fmt.Errorf(
				"hypervisor %s with .service.host %q could not be uniquely matched to an AZ (matching AZs = %v)",
				hypervisor.HypervisorHostname, hypervisor.Service.Host, matchingAZs)
		}
		var (
			matchingAggregateName    string
			matchingAvailabilityZone string
		)
		for aggr := range matchingAggregates {
			matchingAggregateName = aggr
		}
		for az := range matchingAZs {
			matchingAvailabilityZone = az
		}
		hvCapacity.MatchingAggregates = map[string]bool{matchingAggregateName: true}

		//count this hypervisor's capacity towards the totals for the whole cloud...
		totalCapacity.Add(hvCapacity)
		//...and the AZ level
		azCapacity := azCapacities[matchingAvailabilityZone]
		if azCapacity == nil {
			azCapacity = &partialNovaCapacity{}
			azCapacities[matchingAvailabilityZone] = azCapacity
		}
		azCapacity.Add(hvCapacity)

		//report subcapacity for this hypervisor if requested
		for _, resName := range resourceNames {
			if p.reportSubcapacities[resName] {
				resCapa := hvCapacity.GetCapacity(resName, maxRootDiskSize)
				hvSubcapacities[resName] = append(hvSubcapacities[resName], novaHypervisorSubcapacity{
					ServiceHost:      hypervisor.Service.Host,
					AggregateName:    matchingAggregateName,
					AvailabilityZone: matchingAvailabilityZone,
					Capacity:         resCapa.Capacity,
					Usage:            resCapa.Usage,
					Traits:           traits,
				})
			}
		}
	}

	//build final report
	capacities := make(map[string]core.CapacityData, len(resourceNames))
	for _, resName := range resourceNames {
		resCapa := totalCapacity.GetCapacity(resName, maxRootDiskSize)
		capacities[resName] = core.CapacityData{
			Capacity:      resCapa.Capacity,
			CapacityPerAZ: make(map[string]*core.CapacityDataForAZ, len(azCapacities)),
			Subcapacities: hvSubcapacities[resName],
		}
		for az, azCapacity := range azCapacities {
			resCapa := azCapacity.GetCapacity(resName, maxRootDiskSize)
			capacities[resName].CapacityPerAZ[az] = &resCapa
		}
	}

	if maxRootDiskSize == 0 {
		logg.Error("Nova Capacity: Maximal flavor size is 0. Not reporting instances.")
		delete(capacities, "instances")
	}

	serializedMetricsBytes, err := json.Marshal(metrics)
	return map[string]map[string]core.CapacityData{"compute": capacities}, string(serializedMetricsBytes), err
}

var novaHypervisorWellformedGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_nova_hypervisor_is_wellformed",
		Help: "One metric per Nova hypervisor that was discovered by Limes's capacity scanner. Value is 1 for wellformed hypervisors that could be uniquely matched to an aggregate and an AZ, 0 otherwise.",
	},
	[]string{"hypervisor", "hostname", "aggregate", "az"},
)

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	novaHypervisorWellformedGauge.Describe(ch)
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics string) error {
	var metrics capacityNovaSerializedMetrics
	err := json.Unmarshal([]byte(serializedMetrics), &metrics)
	if err != nil {
		return err
	}

	descCh := make(chan *prometheus.Desc, 1)
	novaHypervisorWellformedGauge.Describe(descCh)
	novaHypervisorWellformedDesc := <-descCh

	for _, hv := range metrics.Hypervisors {
		aggrList := nameListToLabelValue(hv.Aggregates)
		azList := nameListToLabelValue(hv.AvailabilityZones)
		isWellformed := float64(0)
		if len(hv.Aggregates) == 1 && len(hv.AvailabilityZones) == 1 {
			isWellformed = 1
		}

		ch <- prometheus.MustNewConstMetric(
			novaHypervisorWellformedDesc,
			prometheus.GaugeValue, isWellformed,
			hv.Name, hv.Hostname, aggrList, azList,
		)
	}
	return nil
}

func boolMapToList(m map[string]bool) (result []string) {
	for k := range m {
		result = append(result, k)
	}
	sort.Strings(result)
	return
}

func nameListToLabelValue(names []string) string {
	if len(names) == 0 {
		return "unknown"
	}
	return strings.Join(names, ",")
}

// The capacity of any level of the Nova superstructure (hypervisor, aggregate, AZ, cluster).
type partialNovaCapacity struct {
	VCPUs              core.CapacityDataForAZ
	MemoryMB           core.CapacityDataForAZ
	LocalGB            uint64
	RunningVMs         uint64
	MatchingAggregates map[string]bool
}

func (c partialNovaCapacity) IsEmpty() bool {
	return c.VCPUs.Capacity == 0 || c.MemoryMB.Capacity == 0 || c.LocalGB == 0
}

func (c *partialNovaCapacity) Add(other partialNovaCapacity) {
	c.VCPUs.Capacity += other.VCPUs.Capacity
	c.VCPUs.Usage += other.VCPUs.Usage
	c.MemoryMB.Capacity += other.MemoryMB.Capacity
	c.MemoryMB.Usage += other.MemoryMB.Usage
	c.LocalGB += other.LocalGB
	c.RunningVMs += other.RunningVMs

	if c.MatchingAggregates == nil {
		c.MatchingAggregates = make(map[string]bool)
	}
	for aggrName, matches := range other.MatchingAggregates {
		if matches {
			c.MatchingAggregates[aggrName] = true
		}
	}
}

func (c partialNovaCapacity) GetCapacity(resourceName string, maxRootDiskSize float64) core.CapacityDataForAZ {
	switch resourceName {
	case "cores":
		return c.VCPUs
	case "ram":
		return c.MemoryMB
	case "instances":
		amount := 10000 * uint64(len(c.MatchingAggregates))
		if maxRootDiskSize != 0 {
			maxAmount := uint64(float64(c.LocalGB) / maxRootDiskSize)
			if amount > maxAmount {
				amount = maxAmount
			}
		}
		return core.CapacityDataForAZ{
			Capacity: amount,
			Usage:    c.RunningVMs,
		}
	default:
		panic(fmt.Sprintf("called with unknown resourceName %q", resourceName))
	}
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

func (h novaHypervisor) IsInAggregate(aggr aggregates.Aggregate) bool {
	for _, host := range aggr.Hosts {
		if h.Service.Host == host {
			return true
		}
	}
	return false
}

func (h novaHypervisor) getCapacity(placementClient *gophercloud.ServiceClient, resourceProviders []resourceproviders.ResourceProvider) (partialNovaCapacity, []string, error) {
	//find the resource provider that corresponds to this hypervisor
	var providerID string
	for _, rp := range resourceProviders {
		if rp.Name == h.HypervisorHostname {
			providerID = rp.UUID
			break
		}
	}
	if providerID == "" {
		return partialNovaCapacity{}, nil, fmt.Errorf(
			"cannot find resource provider with name %q", h.HypervisorHostname)
	}

	//collect data about that resource provider from the Placement API
	inventory, err := resourceproviders.GetInventories(placementClient, providerID).Extract()
	if err != nil {
		return partialNovaCapacity{}, nil, err
	}
	usages, err := resourceproviders.GetUsages(placementClient, providerID).Extract()
	if err != nil {
		return partialNovaCapacity{}, nil, err
	}
	traits, err := resourceproviders.GetTraits(placementClient, providerID).Extract()
	if err != nil {
		return partialNovaCapacity{}, nil, err
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
	}, traits.Traits, nil
}

type novaHypervisorSubcapacity struct {
	ServiceHost      string   `json:"service_host"` //TODO service.host
	AvailabilityZone string   `json:"az"`
	AggregateName    string   `json:"aggregate"`
	Capacity         uint64   `json:"capacity"`
	Usage            uint64   `json:"usage"`
	Traits           []string `json:"traits"`
}

func getMaxRootDiskSize(novaClient *gophercloud.ServiceClient, expectedExtraSpecs map[string]string) (float64, error) {
	maxRootDiskSize := 0.0
	page, err := flavors.ListDetail(novaClient, nil).AllPages()
	if err != nil {
		return 0, err
	}
	flavorList, err := flavors.ExtractFlavors(page)
	if err != nil {
		return 0, err
	}

	for _, flavor := range flavorList {
		extras, err := flavors.ListExtraSpecs(novaClient, flavor.ID).Extract()
		if err != nil {
			logg.Debug("Failed to get extra specs for flavor: %s.", flavor.ID)
			return 0, err
		}

		//necessary to be able to ignore huge baremetal flavors
		//consider only flavors as defined in extra specs
		matches := true
		for key, value := range expectedExtraSpecs {
			if value != extras[key] {
				matches = false
				break
			}
		}
		if matches {
			logg.Debug("FlavorName: %s, FlavorID: %s, FlavorSize: %d GiB", flavor.Name, flavor.ID, flavor.Disk)
			maxRootDiskSize = math.Max(maxRootDiskSize, float64(flavor.Disk))
		}
	}

	return maxRootDiskSize, nil
}
