// SPDX-FileCopyrightText: 2019 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package nova

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/logg"
)

// PartialCapacity describes compute capacity at a level below the entire
// cluster (e.g. for a single hypervisor, aggregate or AZ).
type PartialCapacity struct {
	VCPUs              PartialCapacityMetric
	MemoryMB           PartialCapacityMetric
	LocalGB            PartialCapacityMetric
	RunningVMs         uint64
	MatchingAggregates map[string]bool
	Subcapacities      []any // only filled on AZ level
}

func (c *PartialCapacity) Add(other PartialCapacity) {
	c.VCPUs.Capacity += other.VCPUs.Capacity
	c.VCPUs.Usage += other.VCPUs.Usage
	c.MemoryMB.Capacity += other.MemoryMB.Capacity
	c.MemoryMB.Usage += other.MemoryMB.Usage
	c.LocalGB.Capacity += other.LocalGB.Capacity
	c.LocalGB.Usage += other.LocalGB.Usage
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

func (c PartialCapacity) CappedToUsage() PartialCapacity {
	return PartialCapacity{
		VCPUs:              c.VCPUs.CappedToUsage(),
		MemoryMB:           c.MemoryMB.CappedToUsage(),
		LocalGB:            c.LocalGB.CappedToUsage(),
		RunningVMs:         c.RunningVMs,
		MatchingAggregates: c.MatchingAggregates,
		Subcapacities:      c.Subcapacities,
	}
}

func (c PartialCapacity) IntoCapacityData(resourceName string, maxRootDiskSize float64, subcapacities []liquid.Subcapacity) *liquid.AZResourceCapacityReport {
	switch resourceName {
	case "cores":
		return &liquid.AZResourceCapacityReport{
			Capacity:      c.VCPUs.Capacity,
			Usage:         Some(c.VCPUs.Usage),
			Subcapacities: subcapacities,
		}
	case "ram":
		return &liquid.AZResourceCapacityReport{
			Capacity:      c.MemoryMB.Capacity,
			Usage:         Some(c.MemoryMB.Usage),
			Subcapacities: subcapacities,
		}
	case "instances":
		amount := 10000 * uint64(len(c.MatchingAggregates))
		if maxRootDiskSize != 0 {
			maxAmount := uint64(float64(c.LocalGB.Capacity) / maxRootDiskSize)
			amount = min(amount, maxAmount)
		}
		return &liquid.AZResourceCapacityReport{
			Capacity:      amount,
			Usage:         Some(c.RunningVMs),
			Subcapacities: subcapacities,
		}
	default:
		panic(fmt.Sprintf("called with unknown resourceName %q", resourceName))
	}
}

// PartialCapacityMetric appears in type PartialCapacity.
type PartialCapacityMetric struct {
	Capacity uint64
	Usage    uint64
}

func (m PartialCapacityMetric) CappedToUsage() PartialCapacityMetric {
	return PartialCapacityMetric{
		Capacity: min(m.Capacity, m.Usage),
		Usage:    m.Usage,
	}
}

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	// enumerate matching flavors, divide into split and pooled flavors;
	// ("split flavors" are those with separate instance quota, as opposed to
	// "pooled flavors" that share a common pool of CPU/instances/RAM capacity)
	//
	// also, for the pooled instances capacity, we need to know the max root disk size on public pooled flavors
	var (
		splitFlavors    []flavors.Flavor
		maxRootDiskSize = uint64(0)
	)
	pooledExtraSpecs := make(map[string]string)
	err := l.FlavorSelection.ForeachFlavor(ctx, l.NovaV2, func(f flavors.Flavor) error {
		switch {
		case IsIronicFlavor(f):
			// ignore Ironic flavors
		case IsSplitFlavor(f):
			splitFlavors = append(splitFlavors, f)
		case f.IsPublic:
			// require that all pooled flavors agree on the same trait-match extra specs
			for spec, val := range f.ExtraSpecs {
				trait, matches := strings.CutPrefix(spec, "trait:")
				if !matches || slices.Contains(l.IgnoredTraits, trait) {
					continue
				}
				if pooledVal, exists := pooledExtraSpecs[spec]; !exists {
					pooledExtraSpecs[spec] = val
				} else if val != pooledVal {
					return fmt.Errorf("conflict: pooled flavors both require extra spec %s values %s and %s", spec, val, pooledVal)
				}
			}
			// only public flavor contribute to the `maxRootDiskSize` calculation (in
			// the wild, we have seen non-public legacy flavors with wildly large
			// disk sizes that would throw off all estimates derived from this number)
			maxRootDiskSize = max(maxRootDiskSize, liquidapi.AtLeastZero(f.Disk))
		}
		return nil
	})
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}
	logg.Debug("pooled extra specs = %v", pooledExtraSpecs)
	if maxRootDiskSize == 0 {
		return liquid.ServiceCapacityReport{}, errors.New("pooled capacity requested, but there are no matching flavors")
	}
	logg.Debug("max root disk size = %d GiB", maxRootDiskSize)

	// collect all relevant resource demands
	coresDemand := req.DemandByResource["cores"]
	instancesDemand := req.DemandByResource["instances"]
	ramDemand := req.DemandByResource["ram"]

	logg.Debug("pooled cores demand: %#v (overcommit factor = %g)", coresDemand.PerAZ, coresDemand.OvercommitFactor)
	logg.Debug("pooled instances demand: %#v", instancesDemand.PerAZ)
	logg.Debug("pooled RAM demand: %#v", ramDemand.PerAZ)

	demandByFlavorName := make(map[string]liquid.ResourceDemand)
	for _, f := range splitFlavors {
		resourceName := ResourceNameForFlavor(f.Name)
		demand := req.DemandByResource[resourceName]
		if demand.OvercommitFactor != 1 && demand.OvercommitFactor != 0 {
			return liquid.ServiceCapacityReport{}, fmt.Errorf("overcommit on compute/%s is not supported", resourceName)
		}
		demandByFlavorName[f.Name] = demand
	}
	logg.Debug("binpackable flavors: %#v", splitFlavors)
	logg.Debug("demand for binpackable flavors: %#v", demandByFlavorName)

	// enumerate matching hypervisors, prepare data structures for binpacking
	hypervisorsByAZ := make(map[liquid.AvailabilityZone]BinpackHypervisors)
	shadowedHypervisorsByAZ := make(map[liquid.AvailabilityZone][]MatchingHypervisor)
	isShadowedHVHostname := make(map[string]bool)
	err = l.HypervisorSelection.ForeachHypervisor(ctx, l.NovaV2, l.PlacementV1, func(h MatchingHypervisor) error {
		// ignore HVs that are not associated with an aggregate and AZ
		if !h.CheckTopology() {
			return nil
		}

		if h.ShadowedByTrait == "" {
			bh, err := PrepareHypervisorForBinpacking(h, pooledExtraSpecs)
			if err != nil {
				return err
			}
			hypervisorsByAZ[h.AvailabilityZone] = append(hypervisorsByAZ[h.AvailabilityZone], bh)

			hc := h.PartialCapacity()
			logg.Debug("%s in %s reports %s capacity, %s used, %d nodes, %s max unit, traits: %v", h.Hypervisor.Description(), h.AvailabilityZone,
				BinpackVector[uint64]{VCPUs: hc.VCPUs.Capacity, MemoryMB: hc.MemoryMB.Capacity, LocalGB: hc.LocalGB.Capacity},
				BinpackVector[uint64]{VCPUs: hc.VCPUs.Usage, MemoryMB: hc.MemoryMB.Usage, LocalGB: hc.LocalGB.Usage},
				len(bh.Nodes), bh.Nodes[0].Capacity, h.Traits,
			)
		} else {
			shadowedHypervisorsByAZ[h.AvailabilityZone] = append(shadowedHypervisorsByAZ[h.AvailabilityZone], h)
			isShadowedHVHostname[h.Hypervisor.HypervisorHostname] = true
			logg.Debug("%s in %s is shadowed by trait %s", h.Hypervisor.Description(), h.AvailabilityZone, h.ShadowedByTrait)
		}

		return nil
	})
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}

	// during binpacking, place instances of large flavors first to achieve optimal results
	slices.SortFunc(splitFlavors, func(lhs, rhs flavors.Flavor) int {
		//NOTE: this returns `rhs-lhs` instead of `lhs-rhs` to achieve descending order
		if lhs.VCPUs != rhs.VCPUs {
			return rhs.VCPUs - lhs.VCPUs
		}
		if lhs.RAM != rhs.RAM {
			return rhs.RAM - lhs.RAM
		}
		return rhs.Disk - lhs.Disk
	})

	// if Nova can tell us where existing instances are running, we prefer this
	// information since it will make our simulation more accurate
	instancesPlacedOnShadowedHypervisors := make(map[string]map[liquid.AvailabilityZone]uint64) // first key is flavor name
	bb := l.BinpackBehavior
	for _, flavor := range splitFlavors {
		shadowedForThisFlavor := make(map[liquid.AvailabilityZone]uint64)

		// list all servers for this flavor, parsing only placement information from the result
		listOpts := servers.ListOpts{
			Flavor:     flavor.ID,
			AllTenants: true,
		}
		allPages, err := servers.List(l.NovaV2, listOpts).AllPages(ctx)
		if err != nil {
			return liquid.ServiceCapacityReport{}, fmt.Errorf("while listing active instances for flavor %s: %w", flavor.Name, err)
		}
		var instances []struct {
			ID                 string `json:"id"`
			AZ                 string `json:"OS-EXT-AZ:availability_zone"`
			HypervisorHostname string `json:"OS-EXT-SRV-ATTR:hypervisor_hostname"`
		}
		err = servers.ExtractServersInto(allPages, &instances)
		if err != nil {
			return liquid.ServiceCapacityReport{}, fmt.Errorf("while listing active instances for flavor %s: %w", flavor.Name, err)
		}

		for _, instance := range instances {
			az := liquid.NormalizeAZ(instance.AZ, req.AllAZs)

			// If we are absolutely sure that this instance is placed on a shadowed hypervisor,
			// we remember this and have the final capacity take those into account without
			// including them in the binpacking simulation.
			if isShadowedHVHostname[instance.HypervisorHostname] {
				shadowedForThisFlavor[az]++
				continue
			}

			// If the instance is placed on a known hypervisor, place it right now.
			// The number of instances thus placed will be skipped below to avoid double counting.
			for _, hv := range hypervisorsByAZ[az] {
				if hv.Match.Hypervisor.HypervisorHostname == instance.HypervisorHostname {
					var zero BinpackVector[uint64]
					placed := BinpackHypervisors{hv}.PlaceOneInstance(flavor, "USED", coresDemand.OvercommitFactor, zero, bb, true)
					if !placed {
						logg.Debug("could not simulate placement of known instance %s on %s", instance.ID, hv.Match.Hypervisor.Description())
					}
					break
				}
			}
		}

		if len(shadowedForThisFlavor) > 0 {
			instancesPlacedOnShadowedHypervisors[flavor.Name] = shadowedForThisFlavor
		}
	}
	logg.Debug("instances for split flavors placed on shadowed hypervisors: %v", instancesPlacedOnShadowedHypervisors)

	// foreach AZ, place demanded split instances in order of priority, unless
	// blocked by pooled instances of equal or higher priority
	for az, hypervisors := range hypervisorsByAZ {
		canPlaceFlavor := make(map[string]bool)
		for _, flavor := range splitFlavors {
			canPlaceFlavor[flavor.Name] = true
		}

		// phase 1: block existing usage
		blockedCapacity := BinpackVector[uint64]{
			VCPUs:    coresDemand.OvercommitFactor.ApplyInReverseTo(coresDemand.PerAZ[az].Usage),
			MemoryMB: ramDemand.PerAZ[az].Usage,
			LocalGB:  instancesDemand.PerAZ[az].Usage * maxRootDiskSize,
		}
		logg.Debug("[%s] blockedCapacity in phase 1: %s", az, blockedCapacity.String())
		for _, flavor := range splitFlavors {
			// do not place instances that have already been placed in the simulation,
			// as well as instances that run on hypervisors that do not participate in the binpacking simulation
			placedUsage := hypervisors.PlacementCountForFlavor(flavor.Name)
			shadowedUsage := instancesPlacedOnShadowedHypervisors[flavor.Name][az]
			unplacedUsage := liquidapi.SaturatingSub(demandByFlavorName[flavor.Name].PerAZ[az].Usage, placedUsage+shadowedUsage)
			if !hypervisors.PlaceSeveralInstances(flavor, "used", coresDemand.OvercommitFactor, blockedCapacity, bb, false, unplacedUsage) {
				canPlaceFlavor[flavor.Name] = false
			}
		}

		// phase 2: block confirmed, but unused commitments
		blockedCapacity.VCPUs += coresDemand.OvercommitFactor.ApplyInReverseTo(coresDemand.PerAZ[az].UnusedCommitments)
		blockedCapacity.MemoryMB += ramDemand.PerAZ[az].UnusedCommitments
		blockedCapacity.LocalGB += instancesDemand.PerAZ[az].UnusedCommitments * maxRootDiskSize
		logg.Debug("[%s] blockedCapacity in phase 2: %s", az, blockedCapacity.String())
		for _, flavor := range splitFlavors {
			if !hypervisors.PlaceSeveralInstances(flavor, "committed", coresDemand.OvercommitFactor, blockedCapacity, bb, false, demandByFlavorName[flavor.Name].PerAZ[az].UnusedCommitments) {
				canPlaceFlavor[flavor.Name] = false
			}
		}

		// phase 3: block pending commitments
		blockedCapacity.VCPUs += coresDemand.OvercommitFactor.ApplyInReverseTo(coresDemand.PerAZ[az].PendingCommitments)
		blockedCapacity.MemoryMB += ramDemand.PerAZ[az].PendingCommitments
		blockedCapacity.LocalGB += instancesDemand.PerAZ[az].PendingCommitments * maxRootDiskSize
		logg.Debug("[%s] blockedCapacity in phase 3: %s", az, blockedCapacity.String())
		for _, flavor := range splitFlavors {
			if !hypervisors.PlaceSeveralInstances(flavor, "pending", coresDemand.OvercommitFactor, blockedCapacity, bb, false, demandByFlavorName[flavor.Name].PerAZ[az].PendingCommitments) {
				canPlaceFlavor[flavor.Name] = false
			}
		}

		// check how many instances we could place until now
		initiallyPlacedInstances := make(map[string]float64)
		sumInitiallyPlacedInstances := uint64(0)
		totalPlacedInstances := make(map[string]float64) // these two will diverge in the final round of placements
		var splitFlavorsUsage BinpackVector[uint64]
		for _, flavor := range splitFlavors {
			count := hypervisors.PlacementCountForFlavor(flavor.Name)
			initiallyPlacedInstances[flavor.Name] = max(float64(count), 0.1)
			sumInitiallyPlacedInstances += count
			totalPlacedInstances[flavor.Name] = float64(count)
			// The max(..., 0.1) is explained below.

			splitFlavorsUsage.VCPUs += coresDemand.OvercommitFactor.ApplyInReverseTo(count * liquidapi.AtLeastZero(flavor.VCPUs))
			splitFlavorsUsage.MemoryMB += count * liquidapi.AtLeastZero(flavor.RAM)
			splitFlavorsUsage.LocalGB += count * liquidapi.AtLeastZero(flavor.Disk)
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

		// Fill up with padding in a fair way as long as there is space left.
		// This uses the Sainte-Laguë method designed for allocation of parliament
		// seats. In this case, the parties are the flavors, the votes are what we
		// allocated based on demand (`initiallyPlacedInstances`), and the seats are
		// the placements (`totalPlacedInstances`).
		for {
			var (
				bestFlavor *flavors.Flavor
				bestScore  = -1.0
			)
			for _, flavor := range splitFlavors {
				if !canPlaceFlavor[flavor.Name] {
					continue
				}
				score := (initiallyPlacedInstances[flavor.Name]) / (2*totalPlacedInstances[flavor.Name] + 1)
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
				if hypervisors.PlaceOneInstance(*bestFlavor, "padding", coresDemand.OvercommitFactor, blockedCapacity, bb, false) {
					totalPlacedInstances[bestFlavor.Name]++
				} else {
					canPlaceFlavor[bestFlavor.Name] = false
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
	capacities := make(map[liquid.ResourceName]*liquid.ResourceCapacityReport, len(splitFlavors)+3)
	capacities["cores"] = &liquid.ResourceCapacityReport{
		PerAZ: make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport),
	}
	capacities["instances"] = &liquid.ResourceCapacityReport{
		PerAZ: make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport),
	}
	capacities["ram"] = &liquid.ResourceCapacityReport{
		PerAZ: make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport),
	}

	for az, hypervisors := range hypervisorsByAZ {
		// Only consider hypersivors in the calculation that match all extra specs the pooled flavors agreed on
		var matchingHypervisors BinpackHypervisors
		for _, h := range hypervisors {
			if h.AcceptsPooledFlavors {
				matchingHypervisors = append(matchingHypervisors, h)
			}
		}
		var (
			azCapacity PartialCapacity
			builder    PooledSubcapacityBuilder
		)
		for _, h := range matchingHypervisors {
			azCapacity.Add(h.Match.PartialCapacity())
			if l.WithSubcapacities {
				err = builder.AddHypervisor(h.Match, float64(maxRootDiskSize))
				if err != nil {
					return liquid.ServiceCapacityReport{}, fmt.Errorf("could not add hypervisor as subcapacity: %w", err)
				}
			}
		}
		for _, h := range shadowedHypervisorsByAZ[az] {
			if !FlavorMatchesHypervisor(flavors.Flavor{ExtraSpecs: pooledExtraSpecs}, h) {
				continue
			}
			azCapacity.Add(h.PartialCapacity().CappedToUsage())
			if l.WithSubcapacities {
				err = builder.AddHypervisor(h, float64(maxRootDiskSize))
				if err != nil {
					return liquid.ServiceCapacityReport{}, fmt.Errorf("could not add hypervisor as subcapacity: %w", err)
				}
			}
		}

		capacities["cores"].PerAZ[az] = azCapacity.IntoCapacityData("cores", float64(maxRootDiskSize), builder.CoresSubcapacities)
		capacities["instances"].PerAZ[az] = azCapacity.IntoCapacityData("instances", float64(maxRootDiskSize), builder.InstancesSubcapacities)
		capacities["ram"].PerAZ[az] = azCapacity.IntoCapacityData("ram", float64(maxRootDiskSize), builder.RAMSubcapacities)
		for _, flavor := range splitFlavors {
			count := matchingHypervisors.PlacementCountForFlavor(flavor.Name)
			capacities["cores"].PerAZ[az].Capacity = liquidapi.SaturatingSub(capacities["cores"].PerAZ[az].Capacity, coresDemand.OvercommitFactor.ApplyInReverseTo(count*liquidapi.AtLeastZero(flavor.VCPUs)))
			capacities["instances"].PerAZ[az].Capacity = liquidapi.SaturatingSub(capacities["instances"].PerAZ[az].Capacity, count) //TODO: not accurate when uint64(flavor.Disk) != maxRootDiskSize
			capacities["ram"].PerAZ[az].Capacity = liquidapi.SaturatingSub(capacities["ram"].PerAZ[az].Capacity, count*liquidapi.AtLeastZero(flavor.RAM))
		}
	}

	// compile result for split flavors
	for _, flavor := range splitFlavors {
		resourceName := ResourceNameForFlavor(flavor.Name)
		capacities[resourceName] = &liquid.ResourceCapacityReport{
			PerAZ: make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport),
		}

		for az, hypervisors := range hypervisorsByAZ {
			capacities[resourceName].PerAZ[az] = &liquid.AZResourceCapacityReport{
				Capacity: hypervisors.PlacementCountForFlavor(flavor.Name),
			}
		}

		// if shadowed hypervisors are still carrying instances of this flavor,
		// increase the capacity accordingly to more accurately represent the
		// free capacity on the unshadowed hypervisors
		for az, shadowedCount := range instancesPlacedOnShadowedHypervisors[flavor.Name] {
			if capacities[resourceName].PerAZ[az] == nil {
				capacities[resourceName].PerAZ[az] = &liquid.AZResourceCapacityReport{
					Capacity: shadowedCount,
				}
			} else {
				capacities[resourceName].PerAZ[az].Capacity += shadowedCount
			}
		}
	}

	return liquid.ServiceCapacityReport{
		InfoVersion: serviceInfo.Version,
		Resources:   capacities,
	}, nil
}
