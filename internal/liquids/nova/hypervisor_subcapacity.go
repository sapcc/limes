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
	"github.com/sapcc/go-api-declarations/limes"
)

// Subcapacity is the structure for subcapacities reported by the "nova" capacity plugin.
// Each subcapacity refers to a single Nova hypervisor.
//
// This structure can appear on both pooled resources (using the Capacity and Usage fields to report only one dimension at a time),
// or on split flavors (using the CapacityVector and UsageVector fields to report all dimensions at once).
type Subcapacity struct {
	ServiceHost      string                 `json:"service_host"`
	AvailabilityZone limes.AvailabilityZone `json:"az"`
	AggregateName    string                 `json:"aggregate"`
	Capacity         *uint64                `json:"capacity,omitempty"`
	Usage            *uint64                `json:"usage,omitempty"`
	CapacityVector   *BinpackVector[uint64] `json:"capacity_vector,omitempty"`
	UsageVector      *BinpackVector[uint64] `json:"usage_vector,omitempty"`
	Traits           []string               `json:"traits"`
}

// PooledSubcapacityBuilder is used to build subcapacity lists for pooled resources.
type PooledSubcapacityBuilder struct {
	// These are actually []Subcapacity, but we store them as []any because
	// that's what goes into type core.CapacityData in the end.
	CoresSubcapacities     []any
	InstancesSubcapacities []any
	RAMSubcapacities       []any
}

func (b *PooledSubcapacityBuilder) AddHypervisor(h MatchingHypervisor, maxRootDiskSize float64) {
	pc := h.PartialCapacity()

	hvCoresCapa := pc.IntoCapacityData("cores", maxRootDiskSize, nil)
	b.CoresSubcapacities = append(b.CoresSubcapacities, Subcapacity{
		ServiceHost:      h.Hypervisor.Service.Host,
		AggregateName:    h.AggregateName,
		AvailabilityZone: h.AvailabilityZone,
		Capacity:         &hvCoresCapa.Capacity,
		Usage:            hvCoresCapa.Usage,
		Traits:           h.Traits,
	})
	hvInstancesCapa := pc.IntoCapacityData("instances", maxRootDiskSize, nil)
	b.InstancesSubcapacities = append(b.InstancesSubcapacities, Subcapacity{
		ServiceHost:      h.Hypervisor.Service.Host,
		AggregateName:    h.AggregateName,
		AvailabilityZone: h.AvailabilityZone,
		Capacity:         &hvInstancesCapa.Capacity,
		Usage:            hvInstancesCapa.Usage,
		Traits:           h.Traits,
	})
	hvRAMCapa := pc.IntoCapacityData("ram", maxRootDiskSize, nil)
	b.RAMSubcapacities = append(b.RAMSubcapacities, Subcapacity{
		ServiceHost:      h.Hypervisor.Service.Host,
		AggregateName:    h.AggregateName,
		AvailabilityZone: h.AvailabilityZone,
		Capacity:         &hvRAMCapa.Capacity,
		Usage:            hvRAMCapa.Usage,
		Traits:           h.Traits,
	})
}

// PooledSubcapacityBuilder is used to build subcapacity lists for split flavors.
// These subcapacities are reported on the first flavor in alphabetic order.
type SplitFlavorSubcapacityBuilder struct {
	Subcapacities []any
}

func (b *SplitFlavorSubcapacityBuilder) AddHypervisor(h MatchingHypervisor) {
	pc := h.PartialCapacity()
	b.Subcapacities = append(b.Subcapacities, Subcapacity{
		ServiceHost:      h.Hypervisor.Service.Host,
		AggregateName:    h.AggregateName,
		AvailabilityZone: h.AvailabilityZone,
		CapacityVector: &BinpackVector[uint64]{
			VCPUs:    pc.VCPUs.Capacity,
			MemoryMB: pc.MemoryMB.Capacity,
			LocalGB:  pc.LocalGB.Capacity,
		},
		UsageVector: &BinpackVector[uint64]{
			VCPUs:    pc.VCPUs.Usage,
			MemoryMB: pc.MemoryMB.Usage,
			LocalGB:  pc.LocalGB.Usage,
		},
		Traits: h.Traits,
	})
}
