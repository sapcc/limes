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

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/plugins/nova"
)

type capacityNovaPlugin struct {
	//configuration
	HypervisorSelection      nova.HypervisorSelection `yaml:"hypervisor_selection"`
	MaxInstancesPerAggregate uint64                   `yaml:"max_instances_per_aggregate"`
	FlavorSelection          nova.FlavorSelection     `yaml:"flavor_selection"`
	WithSubcapacities        bool                     `yaml:"with_subcapacities"`
	//connections
	NovaV2      *gophercloud.ServiceClient `yaml:"-"`
	PlacementV1 *gophercloud.ServiceClient `yaml:"-"`
}

type capacityNovaSerializedMetrics struct {
	Hypervisors []novaHypervisorMetrics `json:"hv"`
}

type novaHypervisorMetrics struct {
	Name             string                 `json:"n"`
	Hostname         string                 `json:"hn"`
	AggregateName    string                 `json:"ag"`
	AvailabilityZone limes.AvailabilityZone `json:"az"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityNovaPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	if p.HypervisorSelection.AggregateNameRx == "" {
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
func (p *capacityNovaPlugin) Scrape(_ core.CapacityPluginBackchannel) (result map[string]map[string]core.PerAZ[core.CapacityData], serializedMetrics []byte, err error) {
	//for the instances capacity, we need to know the max root disk size on public flavors
	maxRootDiskSize := 0.0
	err = p.FlavorSelection.ForeachFlavor(p.NovaV2, func(f flavors.Flavor, _ map[string]string) error {
		maxRootDiskSize = max(maxRootDiskSize, float64(f.Disk))
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	//we need to prepare several aggregations in the big loop below
	var (
		resourceNames = []string{"cores", "instances", "ram"}
		azCapacities  = make(map[limes.AvailabilityZone]*partialNovaCapacity)
		metrics       capacityNovaSerializedMetrics
	)

	//foreach hypervisor...
	err = p.HypervisorSelection.ForeachHypervisor(p.NovaV2, p.PlacementV1, func(h nova.MatchingHypervisor) error {
		//report wellformed-ness of this HV via metrics
		metrics.Hypervisors = append(metrics.Hypervisors, novaHypervisorMetrics{
			Name:             h.Hypervisor.Service.Host,
			Hostname:         h.Hypervisor.HypervisorHostname,
			AggregateName:    h.AggregateName,
			AvailabilityZone: h.AvailabilityZone,
		})

		//ignore HVs that are not associated with an aggregate and AZ
		//
		//This is not a fatal error: During buildup, new hypervisors may not be
		//mapped to an aggregate to prevent scheduling of instances onto them -
		//we just log an error and ignore this hypervisor's capacity.
		if h.AggregateName == "" {
			logg.Error("%s does not belong to any matching aggregates", h.Hypervisor.Description())
			return nil
		}
		if h.AvailabilityZone == "" {
			logg.Error("%s could not be matched to any AZ (aggregate = %q)", h.Hypervisor.Description(), h.AggregateName)
			return nil
		}

		//get capacity for this hypervisor
		hvCapacity := partialNovaCapacity{
			VCPUs: partialNovaCapacityMetric{
				Capacity: uint64(h.Inventories["VCPU"].Total - h.Inventories["VCPU"].Reserved),
				Usage:    uint64(h.Usages["VCPU"]),
			},
			MemoryMB: partialNovaCapacityMetric{
				Capacity: uint64(h.Inventories["MEMORY_MB"].Total - h.Inventories["MEMORY_MB"].Reserved),
				Usage:    uint64(h.Usages["MEMORY_MB"]),
			},
			LocalGB:            uint64(h.Inventories["DISK_GB"].Total - h.Inventories["DISK_GB"].Reserved),
			RunningVMs:         h.Hypervisor.RunningVMs,
			MatchingAggregates: map[string]bool{h.AggregateName: true},
		}
		logg.Debug("%s reports capacity: %d CPUs, %d MiB RAM, %d GiB disk",
			h.Hypervisor.Description(), hvCapacity.VCPUs.Capacity, hvCapacity.MemoryMB.Capacity, hvCapacity.LocalGB,
		)

		//count this hypervisor's capacity towards the totals for the AZ level
		azCapacity := azCapacities[h.AvailabilityZone]
		if azCapacity == nil {
			azCapacity = &partialNovaCapacity{}
			azCapacities[h.AvailabilityZone] = azCapacity
		}
		azCapacity.Add(hvCapacity)

		//report subcapacity for this hypervisor if requested
		if p.WithSubcapacities {
			for _, resName := range resourceNames {
				resCapa := hvCapacity.GetCapacity(resName, maxRootDiskSize)
				azCapacity.Subcapacities = append(azCapacity.Subcapacities, novaHypervisorSubcapacity{
					ServiceHost:      h.Hypervisor.Service.Host,
					AggregateName:    h.AggregateName,
					AvailabilityZone: h.AvailabilityZone,
					Capacity:         resCapa.Capacity,
					Usage:            *resCapa.Usage,
					Traits:           h.Traits,
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	//build final report
	capacities := make(map[string]core.PerAZ[core.CapacityData], len(resourceNames))
	for _, resName := range resourceNames {
		capacities[resName] = make(core.PerAZ[core.CapacityData], len(azCapacities))
		for az, azCapacity := range azCapacities {
			resCapa := azCapacity.GetCapacity(resName, maxRootDiskSize)
			capacities[resName][az] = &resCapa
		}
	}

	if maxRootDiskSize == 0 {
		logg.Error("Nova Capacity: Maximal flavor size is 0. Not reporting instances.")
		delete(capacities, "instances")
	}

	serializedMetrics, err = json.Marshal(metrics)
	return map[string]map[string]core.PerAZ[core.CapacityData]{"compute": capacities}, serializedMetrics, err
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
func (p *capacityNovaPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte) error {
	var metrics capacityNovaSerializedMetrics
	err := json.Unmarshal(serializedMetrics, &metrics)
	if err != nil {
		return err
	}

	descCh := make(chan *prometheus.Desc, 1)
	novaHypervisorWellformedGauge.Describe(descCh)
	novaHypervisorWellformedDesc := <-descCh

	for _, hv := range metrics.Hypervisors {
		isWellformed := float64(0)
		if hv.AggregateName != "" && hv.AvailabilityZone != "" {
			isWellformed = 1
		}

		ch <- prometheus.MustNewConstMetric(
			novaHypervisorWellformedDesc,
			prometheus.GaugeValue, isWellformed,
			hv.Name, hv.Hostname, stringOrUnknown(hv.AggregateName), stringOrUnknown(hv.AvailabilityZone),
		)
	}
	return nil
}

func stringOrUnknown[S ~string](value S) string {
	if value == "" {
		return "unknown"
	}
	return string(value)
}

// The capacity of any level of the Nova superstructure (hypervisor, aggregate, AZ).
type partialNovaCapacity struct {
	VCPUs              partialNovaCapacityMetric
	MemoryMB           partialNovaCapacityMetric
	LocalGB            uint64
	RunningVMs         uint64
	MatchingAggregates map[string]bool
	Subcapacities      []any // only filled on AZ level
}

type partialNovaCapacityMetric struct {
	Capacity uint64
	Usage    uint64
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

func (c partialNovaCapacity) GetCapacity(resourceName string, maxRootDiskSize float64) core.CapacityData {
	switch resourceName {
	case "cores":
		return core.CapacityData{
			Capacity: c.VCPUs.Capacity,
			Usage:    &c.VCPUs.Usage,
		}
	case "ram":
		return core.CapacityData{
			Capacity: c.MemoryMB.Capacity,
			Usage:    &c.MemoryMB.Usage,
		}
	case "instances":
		amount := 10000 * uint64(len(c.MatchingAggregates))
		if maxRootDiskSize != 0 {
			maxAmount := uint64(float64(c.LocalGB) / maxRootDiskSize)
			if amount > maxAmount {
				amount = maxAmount
			}
		}
		return core.CapacityData{
			Capacity: amount,
			Usage:    &c.RunningVMs,
		}
	default:
		panic(fmt.Sprintf("called with unknown resourceName %q", resourceName))
	}
}

type novaHypervisorSubcapacity struct {
	ServiceHost      string                 `json:"service_host"`
	AvailabilityZone limes.AvailabilityZone `json:"az"`
	AggregateName    string                 `json:"aggregate"`
	Capacity         uint64                 `json:"capacity"`
	Usage            uint64                 `json:"usage"`
	Traits           []string               `json:"traits"`
}
