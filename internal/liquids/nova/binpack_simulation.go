// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package nova

import (
	"fmt"
	"math"
	"strings"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/resourceproviders"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/logg"
)

// binpackBehavior contains configuration parameters for the binpack simulation.
type binpackBehavior struct {
	// When ranking nodes during placement, do not include the VCPU count dimension in the score.
	ScoreIgnoresCores bool `json:"score_ignores_cores"`
	// When ranking nodes during placement, do not include the disk size dimension in the score.
	ScoreIgnoresDisk bool `json:"score_ignores_disk"`
	// When ranking nodes during placement, do not include the RAM size dimension in the score.
	ScoreIgnoresRAM bool `json:"score_ignores_ram"`
}

// binpackHypervisor models an entire Nova hypervisor for the purposes of the
// binpacking simulation.
//
// We assume the Nova hypervisor to be an entire cluster of physical nodes.
// We cannot see the sizes of the individual nodes in that cluster, only the
// total capacity and the MaxUnit value on the inventories. We have to make the
// assumption that the individual nodes are of identical size.
type binpackHypervisor struct {
	Match                matchingHypervisor
	Nodes                []*binpackNode
	AcceptsPooledFlavors bool
}

// binpackHypervisors adds methods to type []binpackHypervisor.
type binpackHypervisors []binpackHypervisor

// binpackNode appears in type binpackHypervisor.
type binpackNode struct {
	Capacity  binpackVector[uint64]
	Instances []binpackInstance
}

// binpackInstance appears in type binpackNode. It describes a single instance
// that has been placed on the node as part of the binpacking simulation.
type binpackInstance struct {
	FlavorName string
	Size       binpackVector[uint64]
	Reason     string // one of "used", "committed", "pending", "padding" (in descending priority), only for debug logging
}

func guessNodeCountFromMetric(metric string, inv resourceproviders.Inventory) (uint64, error) {
	if inv.MaxUnit == 0 {
		return 0, fmt.Errorf("missing MaxUnit for %s metric", metric)
	}
	nodeCountFloat := float64(inv.Total-inv.Reserved) / float64(inv.MaxUnit)

	// we prefer to round down (11.6 nodes should go to 11 instead of 12 to be
	// safe), but data sometimes has slight rounding errors that we want to
	// correct (9.98 nodes should be rounded up to 10 instead of down to 9)
	//
	// The cutoff of 0.9 is based on what nova-bigvm does. It too accepts up to
	// 10% difference in RAM between nodes of the same hypervisor cluster.
	nodeCount := uint64(math.Floor(nodeCountFloat))
	if nodeCountFloat-float64(nodeCount) > 0.9 {
		nodeCount = uint64(math.Ceil(nodeCountFloat))
	}
	return nodeCount, nil
}

// prepareHypervisorForBinpacking converts a matchingHypervisor into a binpackHypervisor.
func prepareHypervisorForBinpacking(h matchingHypervisor, pooledExtraSpecs map[string]string) (binpackHypervisor, error) {
	// compute node count based on the assumption of equal-size nodes:
	//     nodeCount = (total - reserved) / maxUnit
	nodeCountAccordingToVCPU, err := guessNodeCountFromMetric("VCPU", h.Inventories["VCPU"])
	if err != nil {
		return binpackHypervisor{}, fmt.Errorf("cannot deduce node count for %s: %w", h.Hypervisor.description(), err)
	}
	nodeCountAccordingToRAM, err := guessNodeCountFromMetric("MEMORY_MB", h.Inventories["MEMORY_MB"])
	if err != nil {
		return binpackHypervisor{}, fmt.Errorf("cannot deduce node count for %s: %w", h.Hypervisor.description(), err)
	}

	// as a sanity check, all metrics must agree on the same node count
	if nodeCountAccordingToVCPU != nodeCountAccordingToRAM {
		vcpuInventory := h.Inventories["VCPU"]
		ramInventory := h.Inventories["MEMORY_MB"]
		return binpackHypervisor{}, fmt.Errorf(
			"cannot deduce node count for %s: guessing %d based on VCPU (total = %d, reserved = %d, maxUnit = %d), but %d based on MEMORY_MB (total = %d, reserved = %d, maxUnit = %d)",
			h.Hypervisor.description(),
			nodeCountAccordingToVCPU, vcpuInventory.Total, vcpuInventory.Reserved, vcpuInventory.MaxUnit,
			nodeCountAccordingToRAM, ramInventory.Total, ramInventory.Reserved, ramInventory.MaxUnit,
		)
	}

	nodeCount := nodeCountAccordingToVCPU
	if nodeCount == 0 {
		return binpackHypervisor{}, fmt.Errorf("node count for %s is zero", h.Hypervisor.description())
	}

	// break down capacity into equal-sized nodes
	nodeTemplate := binpackNode{
		Capacity: binpackVector[uint64]{
			VCPUs:    liquidapi.AtLeastZero(h.Inventories["VCPU"].MaxUnit),
			MemoryMB: liquidapi.AtLeastZero(h.Inventories["MEMORY_MB"].MaxUnit),
			// We do not use `h.Inventories["DISK_GB"].MaxUnit` because it appears to describe the max root
			// disk size for a single instance, rather than the actual available disk size. Maybe this is
			// because the root disks are stored on nearby NFS filers, so MaxUnit is actually the max
			// volume size instead of the total capacity per node. Since we have a good nodeCount number
			// now, we can divide up the total disk space for all nodes.
			LocalGB: liquidapi.SaturatingSub(h.Inventories["DISK_GB"].Total, h.Inventories["DISK_GB"].Reserved) / nodeCount,
		},
	}
	result := binpackHypervisor{
		Match:                h,
		Nodes:                make([]*binpackNode, int(nodeCount)), //nolint:gosec // uint64 -> int conversion is okay, if there is more than 2^63 elements, we have other problems
		AcceptsPooledFlavors: FlavorMatchesHypervisor(flavors.Flavor{ExtraSpecs: pooledExtraSpecs}, h),
	}
	for idx := range result.Nodes {
		node := nodeTemplate // this is an important copy which has nothing to do with loop-variable aliasing!
		result.Nodes[idx] = &node
	}
	return result, nil
}

// renderDebugView prints an overview of the placements in this hypervisor on several logg.Debug lines.
func (h binpackHypervisor) renderDebugView(az liquid.AvailabilityZone) {
	shortID := h.Match.Hypervisor.Service.Host
	logg.Debug("[%s][%s] %s", az, shortID, h.Match.Hypervisor.description())
	for idx, n := range h.Nodes {
		var placements []string
		for _, i := range n.Instances {
			placements = append(placements, fmt.Sprintf("%s:%s", i.Reason, i.FlavorName))
		}
		placements = append(placements, fmt.Sprintf("%s free", n.free()))
		logg.Debug("[%s][%s][node%03d] %s: %s", az, shortID, idx+1, n.Capacity, strings.Join(placements, ", "))
	}
}

// placeSeveralInstances calls placeOneInstance multiple times.
func (hh binpackHypervisors) placeSeveralInstances(f flavors.Flavor, reason string, coresOvercommitFactor liquid.OvercommitFactor, blockedCapacity binpackVector[uint64], bb binpackBehavior, skipTraitMatch bool, count uint64) (ok bool) {
	for range count {
		ok = hh.placeOneInstance(f, reason, coresOvercommitFactor, blockedCapacity, bb, skipTraitMatch)
		if !ok {
			// if we don't have space for this instance, we won't have space for any following ones
			return false
		}
	}
	return true
}

// placeOneInstance places a single instance of the given flavor using the vector-dot binpacking algorithm.
// If the instance cannot be placed, false is returned.
func (hh binpackHypervisors) placeOneInstance(flavor flavors.Flavor, reason string, coresOvercommitFactor liquid.OvercommitFactor, blockedCapacity binpackVector[uint64], bb binpackBehavior, skipTraitMatch bool) (ok bool) {
	// This function implements the vector dot binpacking method described in [Mayank] (section III,
	// subsection D, including the correction presented in the last paragraph of that subsection).
	//
	// Here's the quick summary: We describe capacity and usage as vectors with three dimensions (VCPU,
	// RAM, Disk). When placing an instance, we have the vector corresponding to that instance's
	// flavor, and also one vector per node describing the unused capacity on that node.
	//
	// For each node, we take the instance's size vector S and the node's free capacity vector F, and
	// rescale them component-wise to the range [0..1] (with 0 meaning zero and 1 meaning the node's
	// full capacity). Then we select the node that minimizes the angle between those two vectors.
	// That's the same as maximizing cos(S, F) = |S * F| / |S| * |F|.
	//
	// [Mayank]: https://www.it.iitb.ac.in/~sahoo/papers/cloud2011_mayank.pdf

	vmSize := binpackVector[uint64]{
		VCPUs:    coresOvercommitFactor.ApplyInReverseTo(liquidapi.AtLeastZero(flavor.VCPUs)),
		MemoryMB: liquidapi.AtLeastZero(flavor.RAM),
		LocalGB:  liquidapi.AtLeastZero(flavor.Disk),
	}

	// ensure that placing this instance does not encroach on the overall blocked capacity
	var totalFree binpackVector[uint64]
	for _, hypervisor := range hh {
		for _, node := range hypervisor.Nodes {
			totalFree = totalFree.add(node.free())
		}
	}
	exceedsCapacity := !blockedCapacity.add(vmSize).fitsIn(totalFree)

	var (
		bestNode  *binpackNode
		bestScore = 0.0
	)
	for _, hypervisor := range hh {
		if hypervisor.AcceptsPooledFlavors && exceedsCapacity {
			continue
		}
		// skip hypervisors that the flavor does not accept
		if !skipTraitMatch && !FlavorMatchesHypervisor(flavor, hypervisor.Match) {
			continue
		}

		for _, node := range hypervisor.Nodes {
			// skip nodes that cannot fit this instance at all
			nodeFree := node.free()
			if !vmSize.fitsIn(nodeFree) {
				continue
			}

			// calculate score as cos(S, F)^2 (maximizing the square of the cosine is the same as
			// maximizing just the cosine, but replaces expensive sqrt() in the denominator with cheap
			// squaring in the nominator)
			s := vmSize.div(node.Capacity)
			f := nodeFree.div(node.Capacity)
			dotProduct := s.dot(f, bb)
			score := dotProduct * dotProduct / (s.dot(s, bb) * f.dot(f, bb))
			// Always favor nodes on nongeneral-purpose hypervisors if they have capacity
			// Upperbound of score calculation is 1.0
			if !hypervisor.AcceptsPooledFlavors {
				score += 1.1
			}

			// choose node with best score
			if score > bestScore {
				bestScore = score
				bestNode = node
			}
		}
	}

	if bestNode == nil {
		if exceedsCapacity {
			logg.Debug("refusing to place %s with %s because of blocked capacity %s (total free = %s)",
				flavor.Name, vmSize.String(), blockedCapacity.String(), totalFree.String())
			return false
		}
		logg.Debug("refusing to place %s with %s because no node has enough space", flavor.Name, vmSize.String())
		return false
	} else {
		bestNode.Instances = append(bestNode.Instances, binpackInstance{
			FlavorName: flavor.Name,
			Size:       vmSize,
			Reason:     reason,
		})
		return true
	}
}

func (n binpackNode) usage() (result binpackVector[uint64]) {
	for _, i := range n.Instances {
		result.VCPUs += i.Size.VCPUs
		result.MemoryMB += i.Size.MemoryMB
		result.LocalGB += i.Size.LocalGB
	}
	return
}

func (n binpackNode) free() binpackVector[uint64] {
	return n.Capacity.saturatingSub(n.usage())
}

type binpackVector[T float64 | uint64] struct {
	VCPUs    T `json:"vcpus"`
	MemoryMB T `json:"memory_mib"`
	LocalGB  T `json:"local_disk_gib"`
}

func (v binpackVector[T]) fitsIn(other binpackVector[T]) bool {
	return v.VCPUs <= other.VCPUs && v.MemoryMB <= other.MemoryMB && v.LocalGB <= other.LocalGB
}

func (v binpackVector[T]) add(other binpackVector[T]) binpackVector[T] {
	return binpackVector[T]{
		VCPUs:    v.VCPUs + other.VCPUs,
		MemoryMB: v.MemoryMB + other.MemoryMB,
		LocalGB:  v.LocalGB + other.LocalGB,
	}
}

// Like Sub, but never goes below zero.
func (v binpackVector[T]) saturatingSub(other binpackVector[T]) binpackVector[T] {
	return binpackVector[T]{
		// The expression `max(0, v - other)` is rewritten into `max(v, other) - other`
		// here to protect against underflow for T == uint64.
		VCPUs:    max(v.VCPUs, other.VCPUs) - other.VCPUs,
		MemoryMB: max(v.MemoryMB, other.MemoryMB) - other.MemoryMB,
		LocalGB:  max(v.LocalGB, other.LocalGB) - other.LocalGB,
	}
}

func (v binpackVector[T]) mul(other binpackVector[T]) binpackVector[float64] {
	return binpackVector[float64]{
		VCPUs:    float64(v.VCPUs) * float64(other.VCPUs),
		MemoryMB: float64(v.MemoryMB) * float64(other.MemoryMB),
		LocalGB:  float64(v.LocalGB) * float64(other.LocalGB),
	}
}

func (v binpackVector[T]) div(other binpackVector[T]) binpackVector[float64] {
	return binpackVector[float64]{
		VCPUs:    float64(v.VCPUs) / float64(other.VCPUs),
		MemoryMB: float64(v.MemoryMB) / float64(other.MemoryMB),
		LocalGB:  float64(v.LocalGB) / float64(other.LocalGB),
	}
}

func (v binpackVector[T]) asFloat() binpackVector[float64] {
	return binpackVector[float64]{
		VCPUs:    float64(v.VCPUs),
		MemoryMB: float64(v.MemoryMB),
		LocalGB:  float64(v.LocalGB),
	}
}

func (v binpackVector[T]) asUint() binpackVector[uint64] {
	return binpackVector[uint64]{
		VCPUs:    uint64(v.VCPUs),
		MemoryMB: uint64(v.MemoryMB),
		LocalGB:  uint64(v.LocalGB),
	}
}

func (v binpackVector[T]) dot(other binpackVector[T], bb binpackBehavior) T {
	score := T(0)
	if !bb.ScoreIgnoresCores {
		score += v.VCPUs * other.VCPUs
	}
	if !bb.ScoreIgnoresDisk {
		score += v.LocalGB * other.LocalGB
	}
	if !bb.ScoreIgnoresRAM {
		score += v.MemoryMB * other.MemoryMB
	}
	return score
}

func (v binpackVector[T]) isAnyZero() bool {
	return v.VCPUs == 0 || v.MemoryMB == 0 || v.LocalGB == 0
}

// String implements the fmt.Stringer interface.
func (v binpackVector[T]) String() string {
	// only used for debug output where T = uint64, so these conversions will not hurt
	return fmt.Sprintf("%dc/%dm/%dg", uint64(v.VCPUs), uint64(v.MemoryMB), uint64(v.LocalGB))
}

// totalCapacity returns the sum of capacity over all hypervisor nodes.
func (hh binpackHypervisors) totalCapacity() (result binpackVector[uint64]) {
	for _, hypervisor := range hh {
		for _, node := range hypervisor.Nodes {
			result = result.add(node.Capacity)
		}
	}
	return
}

// placementCountForFlavor returns how many instances have been placed for the given flavor name.
func (hh binpackHypervisors) placementCountForFlavor(flavorName string) uint64 {
	var result uint64
	for _, hypervisor := range hh {
		for _, node := range hypervisor.Nodes {
			for _, instance := range node.Instances {
				if instance.FlavorName == flavorName {
					result++
				}
			}
		}
	}
	return result
}
