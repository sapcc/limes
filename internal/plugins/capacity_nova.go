/*******************************************************************************
*
* Copyright 2017-2024 SAP SE
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
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/plugins/nova"
)

type capacityNovaPlugin struct {
	// configuration
	HypervisorSelection         nova.HypervisorSelection    `yaml:"hypervisor_selection"`
	FlavorSelection             nova.FlavorSelection        `yaml:"flavor_selection"`
	FlavorAliases               nova.FlavorTranslationTable `yaml:"flavor_aliases"`
	PooledCoresResourceName     string                      `yaml:"pooled_cores_resource"`
	PooledInstancesResourceName string                      `yaml:"pooled_instances_resource"`
	PooledRAMResourceName       string                      `yaml:"pooled_ram_resource"`
	WithSubcapacities           bool                        `yaml:"with_subcapacities"`
	// connections
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

type novaHypervisorSubcapacity struct {
	ServiceHost      string                      `json:"service_host"`
	AvailabilityZone limes.AvailabilityZone      `json:"az"`
	AggregateName    string                      `json:"aggregate"`
	Capacity         *uint64                     `json:"capacity,omitempty"`
	Usage            *uint64                     `json:"usage,omitempty"`
	CapacityVector   *nova.BinpackVector[uint64] `json:"capacity_vector,omitempty"`
	UsageVector      *nova.BinpackVector[uint64] `json:"usage_vector,omitempty"`
	Traits           []string                    `json:"traits"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityNovaPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	if p.HypervisorSelection.AggregateNameRx == "" {
		return errors.New("missing value for params.hypervisor_selection.aggregate_name_pattern")
	}
	if p.PooledCoresResourceName == "" {
		if p.PooledInstancesResourceName != "" || p.PooledRAMResourceName != "" {
			return errors.New("if params.pooled_cores_resource is empty, then params.pooled_instances_resource and params.pooled_ram_resource must also be empty")
		}
	} else {
		if p.PooledInstancesResourceName == "" || p.PooledRAMResourceName == "" {
			return errors.New("if params.pooled_cores_resource is given, then params.pooled_instances_resource and params.pooled_ram_resource must also be given")
		}
	}

	p.NovaV2, err = openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}
	p.PlacementV1, err = openstack.NewPlacementV1(provider, eo)
	if err != nil {
		return err
	}
	p.PlacementV1.Microversion = "1.6" // for traits endpoint

	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) PluginTypeID() string {
	return "nova"
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityNovaPlugin) Scrape(backchannel core.CapacityPluginBackchannel) (result map[string]map[string]core.PerAZ[core.CapacityData], serializedMetrics []byte, err error) {
	// collect info about flavors with separate instance quota
	// (we are calling these "split flavors" here, as opposed to "pooled flavors" that share a common pool of CPU/instances/RAM capacity)
	allSplitFlavorNames, err := p.FlavorAliases.ListFlavorsWithSeparateInstanceQuota(p.NovaV2)
	if err != nil {
		return nil, nil, err
	}
	isSplitFlavorName := make(map[string]bool, len(allSplitFlavorNames))
	for _, n := range allSplitFlavorNames {
		isSplitFlavorName[n] = true
	}

	// enumerate matching flavors, divide into split and pooled flavors;
	// also, for the pooled instances capacity, we need to know the max root disk size on public pooled flavors
	var (
		splitFlavors    []nova.FullFlavor
		maxRootDiskSize = uint64(0)
	)
	err = p.FlavorSelection.ForeachFlavor(p.NovaV2, func(f nova.FullFlavor) error {
		if isSplitFlavorName[f.Flavor.Name] {
			splitFlavors = append(splitFlavors, f)
		} else {
			maxRootDiskSize = max(maxRootDiskSize, uint64(f.Flavor.Disk))
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if p.PooledCoresResourceName != "" && maxRootDiskSize == 0 {
		return nil, nil, errors.New("pooled capacity requested, but there are no matching flavors")
	}

	// collect all relevant resource demands
	var (
		coresDemand           map[limes.AvailabilityZone]core.ResourceDemand
		instancesDemand       map[limes.AvailabilityZone]core.ResourceDemand
		ramDemand             map[limes.AvailabilityZone]core.ResourceDemand
		coresOvercommitFactor core.OvercommitFactor
	)
	if p.PooledCoresResourceName == "" {
		coresOvercommitFactor = 1
	} else {
		coresDemand, err = backchannel.GetGlobalResourceDemand("compute", p.PooledCoresResourceName)
		if err != nil {
			return nil, nil, fmt.Errorf("while collecting resource demand for compute/%s: %w", p.PooledCoresResourceName, err)
		}
		instancesDemand, err = backchannel.GetGlobalResourceDemand("compute", p.PooledInstancesResourceName)
		if err != nil {
			return nil, nil, fmt.Errorf("while collecting resource demand for compute/%s: %w", p.PooledInstancesResourceName, err)
		}
		ramDemand, err = backchannel.GetGlobalResourceDemand("compute", p.PooledRAMResourceName)
		if err != nil {
			return nil, nil, fmt.Errorf("while collecting resource demand for compute/%s: %w", p.PooledRAMResourceName, err)
		}
		coresOvercommitFactor, err = backchannel.GetOvercommitFactor("compute", p.PooledCoresResourceName)
		if err != nil {
			return nil, nil, fmt.Errorf("while getting overcommit factor for compute/%s: %w", p.PooledCoresResourceName, err)
		}
	}
	logg.Debug("pooled cores demand: %#v (overcommit factor = %g)", coresDemand, coresOvercommitFactor)
	logg.Debug("pooled instances demand: %#v", instancesDemand)
	logg.Debug("pooled RAM demand: %#v", ramDemand)

	demandByFlavorName := make(map[string]map[limes.AvailabilityZone]core.ResourceDemand)
	for _, f := range splitFlavors {
		resourceName := p.FlavorAliases.LimesResourceNameForFlavor(f.Flavor.Name)
		demand, err := backchannel.GetGlobalResourceDemand("compute", resourceName)
		if err != nil {
			return nil, nil, fmt.Errorf("while collecting resource demand for compute/%s: %w", resourceName, err)
		}
		demandByFlavorName[f.Flavor.Name] = demand
	}
	logg.Debug("binpackable flavors: %#v", splitFlavors)
	logg.Debug("demand for binpackable flavors: %#v", demandByFlavorName)

	// enumerate matching hypervisors, prepare data structures for binpacking
	var metrics capacityNovaSerializedMetrics
	hypervisorsByAZ := make(map[limes.AvailabilityZone]nova.BinpackHypervisors)
	err = p.HypervisorSelection.ForeachHypervisor(p.NovaV2, p.PlacementV1, func(h nova.MatchingHypervisor) error {
		// report wellformed-ness of this HV via metrics
		metrics.Hypervisors = append(metrics.Hypervisors, novaHypervisorMetrics{
			Name:             h.Hypervisor.Service.Host,
			Hostname:         h.Hypervisor.HypervisorHostname,
			AggregateName:    h.AggregateName,
			AvailabilityZone: h.AvailabilityZone,
		})

		// ignore HVs that are not associated with an aggregate and AZ
		if !h.CheckTopology() {
			return nil
		}

		bh, err := nova.PrepareHypervisorForBinpacking(h)
		if err != nil {
			return err
		}
		hypervisorsByAZ[h.AvailabilityZone] = append(hypervisorsByAZ[h.AvailabilityZone], bh)

		hc := h.PartialCapacity()
		logg.Debug("%s in %s reports %s capacity, %s used, %d nodes, %s max unit", h.Hypervisor.Description(), h.AvailabilityZone,
			nova.BinpackVector[uint64]{VCPUs: hc.VCPUs.Capacity, MemoryMB: hc.MemoryMB.Capacity, LocalGB: hc.LocalGB.Capacity},
			nova.BinpackVector[uint64]{VCPUs: hc.VCPUs.Usage, MemoryMB: hc.MemoryMB.Usage, LocalGB: hc.LocalGB.Usage},
			len(bh.Nodes), bh.Nodes[0].Capacity,
		)

		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	// during binpacking, place instances of large flavors first to achieve optimal results
	slices.SortFunc(splitFlavors, func(lhs, rhs nova.FullFlavor) int {
		//NOTE: this returns `rhs-lhs` instead of `lhs-rhs` to achieve descending order
		lf := lhs.Flavor
		rf := rhs.Flavor
		if lf.VCPUs != rf.VCPUs {
			return rf.VCPUs - lf.VCPUs
		}
		if lf.RAM != rf.RAM {
			return rf.RAM - lf.RAM
		}
		return rf.Disk - lf.Disk
	})

	// foreach AZ, place demanded split instances in order of priority, unless
	// blocked by pooled instances of equal or higher priority
	for az, hypervisors := range hypervisorsByAZ {
		canPlaceFlavor := make(map[string]bool)
		for _, flavor := range splitFlavors {
			canPlaceFlavor[flavor.Flavor.Name] = true
		}

		// phase 1: block existing usage
		blockedCapacity := nova.BinpackVector[uint64]{
			VCPUs:    coresOvercommitFactor.ApplyInReverseTo(coresDemand[az].Usage),
			MemoryMB: ramDemand[az].Usage,
			LocalGB:  instancesDemand[az].Usage * maxRootDiskSize,
		}
		logg.Debug("[%s] blockedCapacity in phase 1: %s", az, blockedCapacity.String())
		for _, flavor := range splitFlavors {
			if !hypervisors.PlaceSeveralInstances(flavor, "used", coresOvercommitFactor, blockedCapacity, demandByFlavorName[flavor.Flavor.Name][az].Usage) {
				canPlaceFlavor[flavor.Flavor.Name] = false
			}
		}

		// phase 2: block confirmed, but unused commitments
		blockedCapacity.VCPUs += coresOvercommitFactor.ApplyInReverseTo(coresDemand[az].UnusedCommitments)
		blockedCapacity.MemoryMB += ramDemand[az].UnusedCommitments
		blockedCapacity.LocalGB += instancesDemand[az].UnusedCommitments * maxRootDiskSize
		logg.Debug("[%s] blockedCapacity in phase 2: %s", az, blockedCapacity.String())
		for _, flavor := range splitFlavors {
			if !hypervisors.PlaceSeveralInstances(flavor, "committed", coresOvercommitFactor, blockedCapacity, demandByFlavorName[flavor.Flavor.Name][az].UnusedCommitments) {
				canPlaceFlavor[flavor.Flavor.Name] = false
			}
		}

		// phase 3: block pending commitments
		blockedCapacity.VCPUs += coresOvercommitFactor.ApplyInReverseTo(coresDemand[az].PendingCommitments)
		blockedCapacity.MemoryMB += ramDemand[az].PendingCommitments
		blockedCapacity.LocalGB += instancesDemand[az].PendingCommitments * maxRootDiskSize
		logg.Debug("[%s] blockedCapacity in phase 3: %s", az, blockedCapacity.String())
		for _, flavor := range splitFlavors {
			if !hypervisors.PlaceSeveralInstances(flavor, "pending", coresOvercommitFactor, blockedCapacity, demandByFlavorName[flavor.Flavor.Name][az].PendingCommitments) {
				canPlaceFlavor[flavor.Flavor.Name] = false
			}
		}

		// check how many instances we could place until now
		initiallyPlacedInstances := make(map[string]float64)
		sumInitiallyPlacedInstances := uint64(0)
		totalPlacedInstances := make(map[string]float64) // these two will diverge in the final round of placements
		var splitFlavorsUsage nova.BinpackVector[uint64]
		for _, flavor := range splitFlavors {
			count := hypervisors.PlacementCountForFlavor(flavor.Flavor.Name)
			initiallyPlacedInstances[flavor.Flavor.Name] = max(float64(count), 0.1)
			sumInitiallyPlacedInstances += count
			totalPlacedInstances[flavor.Flavor.Name] = float64(count)
			// The max(..., 0.1) is explained below.

			splitFlavorsUsage.VCPUs += coresOvercommitFactor.ApplyInReverseTo(count * uint64(flavor.Flavor.VCPUs))
			splitFlavorsUsage.MemoryMB += count * uint64(flavor.Flavor.RAM)
			splitFlavorsUsage.LocalGB += count * uint64(flavor.Flavor.Disk)
		}

		// for the upcoming final fill, we want to block capacity in such a way that
		// the reported capacity is fairly divided between pooled and split flavors,
		// in a way that matches the existing usage distribution, that is:
		//
		//		capacity blocked for pooled flavors = capacity * (pooled usage / total usage)
		//		                                                  ------------
		//		                                                        ^ this is in blockedCapacity
		//
		totalUsageUntilNow := blockedCapacity.Add(splitFlavorsUsage)
		if !totalUsageUntilNow.IsAnyZero() {
			// we can only do this if .Div() does not cause a divide-by-zero, otherwise we continue with blockedCapacity = 0
			blockedCapacity = hypervisors.TotalCapacity().AsFloat().Mul(blockedCapacity.Div(totalUsageUntilNow)).AsUint()
		}
		logg.Debug("[%s] usage by split flavors after phase 3: %s", az, splitFlavorsUsage.String())
		logg.Debug("[%s] blockedCapacity in final fill: %s (totalCapacity = %s)", az, blockedCapacity.String(), hypervisors.TotalCapacity().String())

		// fill up with padding in a fair way as long as there is space left,
		// except if there is pooling and we don't have any demand at all on the split flavors
		// (in order to avoid weird numerical edge cases in the `blockedCapacity` calculation above)
		fillUp := p.PooledCoresResourceName == "" || sumInitiallyPlacedInstances > 0
		// This uses the Sainte-Laguë method designed for allocation of parliament
		// seats. In this case, the parties are the flavors, the votes are what we
		// allocated based on demand (`initiallyPlacedInstances`), and the seats are
		// the placements (`totalPlacedInstances`).
		for fillUp {
			var (
				bestFlavor *nova.FullFlavor
				bestScore  = -1.0
			)
			for _, flavor := range splitFlavors {
				if !canPlaceFlavor[flavor.Flavor.Name] {
					continue
				}
				score := (initiallyPlacedInstances[flavor.Flavor.Name]) / (2*totalPlacedInstances[flavor.Flavor.Name] + 1)
				// ^ This is why we adjusted all initiallyPlacedInstances[flavor.Name] = 0 to 0.1
				// above. If the nominator of this fraction is 0 for multiple flavors, the first
				// (biggest) flavor always wins unfairly. By adjusting to slightly away from zero,
				// the scoring is more fair and stable.
				if score > bestScore {
					bestScore = score
					flavor := flavor
					bestFlavor = &flavor
				}
			}
			if bestFlavor == nil {
				// no flavor left that can be placed -> stop
				break
			} else {
				if hypervisors.PlaceOneInstance(*bestFlavor, "padding", coresOvercommitFactor, blockedCapacity) {
					totalPlacedInstances[bestFlavor.Flavor.Name]++
				} else {
					canPlaceFlavor[bestFlavor.Flavor.Name] = false
				}
			}
		}
	} ////////// end of placement

	// debug visualization of the binpack placement result
	if logg.ShowDebug {
		for az, hypervisors := range hypervisorsByAZ {
			for _, hypervisor := range hypervisors {
				hypervisor.RenderDebugView(az)
			}
		}
	}

	// compile result for pooled resources
	capacities := make(map[string]core.PerAZ[core.CapacityData], len(splitFlavors)+3)
	if p.PooledCoresResourceName != "" {
		capacities[p.PooledCoresResourceName] = make(core.PerAZ[core.CapacityData], len(hypervisorsByAZ))
		capacities[p.PooledInstancesResourceName] = make(core.PerAZ[core.CapacityData], len(hypervisorsByAZ))
		capacities[p.PooledRAMResourceName] = make(core.PerAZ[core.CapacityData], len(hypervisorsByAZ))

		for az, hypervisors := range hypervisorsByAZ {
			var (
				azCapacity             nova.PartialCapacity
				coresSubcapacities     []any
				instancesSubcapacities []any
				ramSubcapacities       []any
			)
			for _, h := range hypervisors {
				mh := h.Match
				azCapacity.Add(mh.PartialCapacity())

				if p.WithSubcapacities {
					hvCoresCapa := mh.PartialCapacity().IntoCapacityData("cores", float64(maxRootDiskSize), nil)
					coresSubcapacities = append(coresSubcapacities, novaHypervisorSubcapacity{
						ServiceHost:      mh.Hypervisor.Service.Host,
						AggregateName:    mh.AggregateName,
						AvailabilityZone: mh.AvailabilityZone,
						Capacity:         &hvCoresCapa.Capacity,
						Usage:            hvCoresCapa.Usage,
						Traits:           mh.Traits,
					})
					hvInstancesCapa := mh.PartialCapacity().IntoCapacityData("instances", float64(maxRootDiskSize), nil)
					instancesSubcapacities = append(instancesSubcapacities, novaHypervisorSubcapacity{
						ServiceHost:      mh.Hypervisor.Service.Host,
						AggregateName:    mh.AggregateName,
						AvailabilityZone: mh.AvailabilityZone,
						Capacity:         &hvInstancesCapa.Capacity,
						Usage:            hvInstancesCapa.Usage,
						Traits:           mh.Traits,
					})
					hvRAMCapa := mh.PartialCapacity().IntoCapacityData("ram", float64(maxRootDiskSize), nil)
					ramSubcapacities = append(ramSubcapacities, novaHypervisorSubcapacity{
						ServiceHost:      mh.Hypervisor.Service.Host,
						AggregateName:    mh.AggregateName,
						AvailabilityZone: mh.AvailabilityZone,
						Capacity:         &hvRAMCapa.Capacity,
						Usage:            hvRAMCapa.Usage,
						Traits:           mh.Traits,
					})
				}
			}

			capacities[p.PooledCoresResourceName][az] = pointerTo(azCapacity.IntoCapacityData("cores", float64(maxRootDiskSize), coresSubcapacities))
			capacities[p.PooledInstancesResourceName][az] = pointerTo(azCapacity.IntoCapacityData("instances", float64(maxRootDiskSize), instancesSubcapacities))
			capacities[p.PooledRAMResourceName][az] = pointerTo(azCapacity.IntoCapacityData("ram", float64(maxRootDiskSize), ramSubcapacities))
			for _, flavor := range splitFlavors {
				count := hypervisors.PlacementCountForFlavor(flavor.Flavor.Name)
				capacities[p.PooledCoresResourceName][az].Capacity -= coresOvercommitFactor.ApplyInReverseTo(count * uint64(flavor.Flavor.VCPUs))
				capacities[p.PooledInstancesResourceName][az].Capacity-- //TODO: not accurate when uint64(flavor.Disk) != maxRootDiskSize
				capacities[p.PooledRAMResourceName][az].Capacity -= count * uint64(flavor.Flavor.RAM)
			}
		}
	}

	// compile result for split flavors
	slices.SortFunc(splitFlavors, func(lhs, rhs nova.FullFlavor) int {
		return strings.Compare(lhs.Flavor.Name, rhs.Flavor.Name)
	})
	for idx, flavor := range splitFlavors {
		resourceName := p.FlavorAliases.LimesResourceNameForFlavor(flavor.Flavor.Name)
		capacities[resourceName] = make(core.PerAZ[core.CapacityData], len(hypervisorsByAZ))

		for az, hypervisors := range hypervisorsByAZ {
			// if we could not report subcapacities on pooled resources, report them on
			// the first flavor in alphabetic order (this is why we just sorted them)
			var subcapacities []any
			if p.WithSubcapacities && p.PooledCoresResourceName == "" && idx == 0 {
				for _, h := range hypervisors {
					mh := h.Match
					pc := mh.PartialCapacity()
					subcapacities = append(subcapacities, novaHypervisorSubcapacity{
						ServiceHost:      mh.Hypervisor.Service.Host,
						AggregateName:    mh.AggregateName,
						AvailabilityZone: mh.AvailabilityZone,
						CapacityVector: &nova.BinpackVector[uint64]{
							VCPUs:    pc.VCPUs.Capacity,
							MemoryMB: pc.MemoryMB.Capacity,
							LocalGB:  pc.LocalGB.Capacity,
						},
						UsageVector: &nova.BinpackVector[uint64]{
							VCPUs:    pc.VCPUs.Usage,
							MemoryMB: pc.MemoryMB.Usage,
							LocalGB:  pc.LocalGB.Usage,
						},
						Traits: mh.Traits,
					})
				}
			}

			capacities[resourceName][az] = &core.CapacityData{
				Capacity:      hypervisors.PlacementCountForFlavor(flavor.Flavor.Name),
				Subcapacities: subcapacities,
			}
		}
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

func pointerTo[T any](value T) *T {
	return &value
}
