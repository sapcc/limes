/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package nova

import (
	"fmt"
	"math"
	"strings"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
)

// BinpackHypervisor models an entire Nova hypervisor for the purposes of the
// binpacking simulation.
//
// We assume the Nova hypervisor to be an entire cluster of physical nodes.
// We cannot see the sizes of the individual nodes in that cluster, only the
// total capacity and the MaxUnit value on the inventories. We have to make the
// assumption that the individual nodes are of identical size.
type BinpackHypervisor struct {
	Match MatchingHypervisor
	Nodes []*BinpackNode
}

// BinpackHypervisors adds methods to type []BinpackHypervisor.
type BinpackHypervisors []BinpackHypervisor

// BinpackNode appears in type BinpackHypervisor.
type BinpackNode struct {
	Capacity  BinpackVector[uint64]
	Instances []BinpackInstance
}

// BinpackInstance appears in type BinpackNode. It describes a single instance
// that has been placed on the node as part of the binpacking simulation.
type BinpackInstance struct {
	FlavorName string
	Size       BinpackVector[uint64]
	Reason     string // one of "used", "committed", "pending", "padding" (in descending priority), only for debug logging
}

// PrepareHypervisorForBinpacking converts a MatchingHypervisor into a BinpackHypervisor.
func PrepareHypervisorForBinpacking(h MatchingHypervisor) (BinpackHypervisor, error) {
	// compute node count based on the assumption of equal-size nodes:
	//     nodeCount = (total - reserved) / maxUnit
	nodeCountCandidates := map[uint64][]string{}
	for _, metric := range []string{"VCPU", "MEMORY_MB"} {
		inv := h.Inventories[metric]
		if inv.MaxUnit == 0 {
			return BinpackHypervisor{}, fmt.Errorf(
				"cannot deduce node count for %s (missing MaxUnit for %s metric)",
				h.Hypervisor.Description(), metric,
			)
		}
		nodeCountFloat := float64(inv.Total-inv.Reserved) / float64(inv.MaxUnit)

		// we prefer to round down (11.6 nodes should go to 11 instead of 12 to be
		// safe), but data sometimes has slight rounding errors that we want to
		// correct (9.98 nodes should be rounded up to 10 instead of down to 9)
		nodeCount := uint64(math.Floor(nodeCountFloat))
		if nodeCountFloat-float64(nodeCount) > 0.98 {
			nodeCount = uint64(math.Ceil(nodeCountFloat))
		}
		nodeCountCandidates[nodeCount] = append(nodeCountCandidates[nodeCount], metric)
	}

	// as a sanity check, all metrics must agree on the same node count
	if len(nodeCountCandidates) > 1 {
		return BinpackHypervisor{}, fmt.Errorf(
			"cannot deduce node count for %s (candidate values by metric = %#v)",
			h.Hypervisor.Description(), nodeCountCandidates)
	}
	var nodeCount uint64
	for nodeCountCandidate := range nodeCountCandidates {
		nodeCount = nodeCountCandidate
		break
	}
	if nodeCount == 0 {
		return BinpackHypervisor{}, fmt.Errorf("node count for %s is zero", h.Hypervisor.Description())
	}

	// break down capacity into equal-sized nodes
	nodeTemplate := BinpackNode{
		Capacity: BinpackVector[uint64]{
			VCPUs:    uint64(h.Inventories["VCPU"].MaxUnit),
			MemoryMB: uint64(h.Inventories["MEMORY_MB"].MaxUnit),
			// We do not use `h.Inventories["DISK_GB"].MaxUnit` because it appears to describe the max root
			// disk size for a single instance, rather than the actual available disk size. Maybe this is
			// because the root disks are stored on nearby NFS filers, so MaxUnit is actually the max
			// volume size instead of the total capacity per node. Since we have a good nodeCount number
			// now, we can divide up the total disk space for all nodes.
			LocalGB: uint64(h.Inventories["DISK_GB"].Total-h.Inventories["DISK_GB"].Reserved) / nodeCount,
		},
	}
	result := BinpackHypervisor{
		Match: h,
		Nodes: make([]*BinpackNode, int(nodeCount)),
	}
	for idx := range result.Nodes {
		node := nodeTemplate
		result.Nodes[idx] = &node
	}
	return result, nil
}

// RenderDebugView prints an overview of the placements in this hypervisor on several logg.Debug lines.
func (h BinpackHypervisor) RenderDebugView(az limes.AvailabilityZone) {
	shortID := h.Match.Hypervisor.Service.Host
	logg.Debug("[%s][%s] %s", az, shortID, h.Match.Hypervisor.Description())
	for idx, n := range h.Nodes {
		var placements []string
		for _, i := range n.Instances {
			placements = append(placements, fmt.Sprintf("%s:%s", i.Reason, i.FlavorName))
		}
		placements = append(placements, fmt.Sprintf("%s free", n.free()))
		logg.Debug("[%s][%s][node%03d] %s: %s", az, shortID, idx+1, n.Capacity, strings.Join(placements, ", "))
	}
}

// PlaceSeveralInstances calls PlaceOneInstance multiple times.
func (hh BinpackHypervisors) PlaceSeveralInstances(ff FullFlavor, reason string, coresOvercommitFactor core.OvercommitFactor, blockedCapacity BinpackVector[uint64], count uint64) (ok bool) {
	for i := uint64(0); i < count; i++ {
		ok = hh.PlaceOneInstance(ff, reason, coresOvercommitFactor, blockedCapacity)
		if !ok {
			// if we don't have space for this instance, we won't have space for any following ones
			return false
		}
	}
	return true
}

// PlaceOneInstance places a single instance of the given flavor using the vector-dot binpacking algorithm.
// If the instance cannot be placed, false is returned.
func (hh BinpackHypervisors) PlaceOneInstance(ff FullFlavor, reason string, coresOvercommitFactor core.OvercommitFactor, blockedCapacity BinpackVector[uint64]) (ok bool) {
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

	vmSize := BinpackVector[uint64]{
		VCPUs:    coresOvercommitFactor.ApplyInReverseTo(uint64(ff.Flavor.VCPUs)),
		MemoryMB: uint64(ff.Flavor.RAM),
		LocalGB:  uint64(ff.Flavor.Disk),
	}

	// ensure that placing this instance does not encroach on the overall blocked capacity
	var totalFree BinpackVector[uint64]
	for _, hypervisor := range hh {
		for _, node := range hypervisor.Nodes {
			totalFree = totalFree.Add(node.free())
		}
	}
	if !blockedCapacity.Add(vmSize).FitsIn(totalFree) {
		logg.Debug("refusing to place %s with %s because of blocked capacity %s (total free = %s)",
			ff.Flavor.Name, vmSize.String(), blockedCapacity.String(), totalFree.String())
		return false
	}

	var (
		bestNode  *BinpackNode
		bestScore = 0.0
	)
	for _, hypervisor := range hh {
		// skip hypervisors that the flavor does not accept
		if !ff.MatchesHypervisor(hypervisor.Match) {
			continue
		}

		for _, node := range hypervisor.Nodes {
			// skip nodes that cannot fit this instance at all
			nodeFree := node.free()
			if !vmSize.FitsIn(nodeFree) {
				continue
			}

			// calculate score as cos(S, F)^2 (maximizing the square of the cosine is the same as
			// maximizing just the cosine, but replaces expensive sqrt() in the denominator with cheap
			// squaring in the nominator)
			s := vmSize.Div(node.Capacity)
			f := nodeFree.Div(node.Capacity)
			dotProduct := s.Dot(f)
			score := dotProduct * dotProduct / (s.Dot(s) * f.Dot(f))

			// choose node with best score
			if score > bestScore {
				bestScore = score
				bestNode = node
			}
		}
	}

	if bestNode == nil {
		logg.Debug("refusing to place %s with %s because no node has enough space", ff.Flavor.Name, vmSize.String())
		return false
	} else {
		bestNode.Instances = append(bestNode.Instances, BinpackInstance{
			FlavorName: ff.Flavor.Name,
			Size:       vmSize,
			Reason:     reason,
		})
		return true
	}
}

func (n BinpackNode) usage() (result BinpackVector[uint64]) {
	for _, i := range n.Instances {
		result.VCPUs += i.Size.VCPUs
		result.MemoryMB += i.Size.MemoryMB
		result.LocalGB += i.Size.LocalGB
	}
	return
}

func (n BinpackNode) free() BinpackVector[uint64] {
	return n.Capacity.SaturatingSub(n.usage())
}

type BinpackVector[T float64 | uint64] struct {
	VCPUs    T `json:"vcpus"`
	MemoryMB T `json:"memory_mib"`
	LocalGB  T `json:"local_disk_gib"`
}

func (v BinpackVector[T]) FitsIn(other BinpackVector[T]) bool {
	return v.VCPUs <= other.VCPUs && v.MemoryMB <= other.MemoryMB && v.LocalGB <= other.LocalGB
}

func (v BinpackVector[T]) Add(other BinpackVector[T]) BinpackVector[T] {
	return BinpackVector[T]{
		VCPUs:    v.VCPUs + other.VCPUs,
		MemoryMB: v.MemoryMB + other.MemoryMB,
		LocalGB:  v.LocalGB + other.LocalGB,
	}
}

// Like Sub, but never goes below zero.
func (v BinpackVector[T]) SaturatingSub(other BinpackVector[T]) BinpackVector[T] {
	return BinpackVector[T]{
		// The expression `max(0, v - other)` is rewritten into `max(v, other) - other`
		// here to protect against underflow for T == uint64.
		VCPUs:    max(v.VCPUs, other.VCPUs) - other.VCPUs,
		MemoryMB: max(v.MemoryMB, other.MemoryMB) - other.MemoryMB,
		LocalGB:  max(v.LocalGB, other.LocalGB) - other.LocalGB,
	}
}

func (v BinpackVector[T]) Mul(other BinpackVector[T]) BinpackVector[float64] {
	return BinpackVector[float64]{
		VCPUs:    float64(v.VCPUs) * float64(other.VCPUs),
		MemoryMB: float64(v.MemoryMB) * float64(other.MemoryMB),
		LocalGB:  float64(v.LocalGB) * float64(other.LocalGB),
	}
}

func (v BinpackVector[T]) Div(other BinpackVector[T]) BinpackVector[float64] {
	return BinpackVector[float64]{
		VCPUs:    float64(v.VCPUs) / float64(other.VCPUs),
		MemoryMB: float64(v.MemoryMB) / float64(other.MemoryMB),
		LocalGB:  float64(v.LocalGB) / float64(other.LocalGB),
	}
}

func (v BinpackVector[T]) AsFloat() BinpackVector[float64] {
	return BinpackVector[float64]{
		VCPUs:    float64(v.VCPUs),
		MemoryMB: float64(v.MemoryMB),
		LocalGB:  float64(v.LocalGB),
	}
}

func (v BinpackVector[T]) AsUint() BinpackVector[uint64] {
	return BinpackVector[uint64]{
		VCPUs:    uint64(v.VCPUs),
		MemoryMB: uint64(v.MemoryMB),
		LocalGB:  uint64(v.LocalGB),
	}
}

func (v BinpackVector[T]) Dot(other BinpackVector[T]) T {
	return v.VCPUs*other.VCPUs + v.MemoryMB*other.MemoryMB + v.LocalGB*other.LocalGB
}

func (v BinpackVector[T]) IsAnyZero() bool {
	return v.VCPUs == 0 || v.MemoryMB == 0 || v.LocalGB == 0
}

func (v BinpackVector[T]) String() string {
	// only used for debug output where T = uint64, so these conversions will not hurt
	return fmt.Sprintf("%dc/%dm/%dg", uint64(v.VCPUs), uint64(v.MemoryMB), uint64(v.LocalGB))
}

// TotalCapacity returns the sum of capacity over all hypervisor nodes.
func (hh BinpackHypervisors) TotalCapacity() (result BinpackVector[uint64]) {
	for _, hypervisor := range hh {
		for _, node := range hypervisor.Nodes {
			result = result.Add(node.Capacity)
		}
	}
	return
}

// PlacementCountForFlavor returns how many instances have been placed for the given flavor name.
func (hh BinpackHypervisors) PlacementCountForFlavor(flavorName string) uint64 {
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
