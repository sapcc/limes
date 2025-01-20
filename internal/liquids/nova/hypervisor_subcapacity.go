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
	"encoding/json"
	"fmt"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
)

// DeprecatedSubcapacity is the structure for subcapacities reported by the "nova" capacity plugin.
// Each subcapacity refers to a single Nova hypervisor.
//
// This structure can appear on both pooled resources (using the Capacity and Usage fields to report only one dimension at a time),
// or on split flavors (using the CapacityVector and UsageVector fields to report all dimensions at once).
type DeprecatedSubcapacity struct {
	ServiceHost      string                 `json:"service_host"`
	AvailabilityZone limes.AvailabilityZone `json:"az"`
	AggregateName    string                 `json:"aggregate"`
	Capacity         *uint64                `json:"capacity,omitempty"`
	Usage            *uint64                `json:"usage,omitempty"`
	CapacityVector   *BinpackVector[uint64] `json:"capacity_vector,omitempty"`
	UsageVector      *BinpackVector[uint64] `json:"usage_vector,omitempty"`
	Traits           []string               `json:"traits"`
}

// TODO: Remove when switching to liquid-nova
// PooledSubcapacityBuilder is used to build subcapacity lists for pooled resources.
type DeprecatedPooledSubcapacityBuilder struct {
	// These are actually []Subcapacity, but we store them as []any because
	// that's what goes into type core.CapacityData in the end.
	CoresSubcapacities     []any
	InstancesSubcapacities []any
	RAMSubcapacities       []any
}

// PooledSubcapacityBuilder is used to build subcapacity lists for pooled resources.
type PooledSubcapacityBuilder struct {
	CoresSubcapacities     []liquid.Subcapacity
	InstancesSubcapacities []liquid.Subcapacity
	RAMSubcapacities       []liquid.Subcapacity
}

type SubcapacityAttributes struct {
	AggregateName string   `json:"aggregate_name"`
	Traits        []string `json:"traits"`
}

// TODO: Remove when switching to liquid-nova
func (b *DeprecatedPooledSubcapacityBuilder) AddHypervisor(h MatchingHypervisor, maxRootDiskSize float64) {
	pc := h.PartialCapacity()

	hvCoresCapa := pc.IntoCapacityData("cores", maxRootDiskSize, nil)
	b.CoresSubcapacities = append(b.CoresSubcapacities, DeprecatedSubcapacity{
		ServiceHost:      h.Hypervisor.Service.Host,
		AggregateName:    h.AggregateName,
		AvailabilityZone: h.AvailabilityZone,
		Capacity:         &hvCoresCapa.Capacity,
		Usage:            hvCoresCapa.Usage,
		Traits:           h.Traits,
	})
	hvInstancesCapa := pc.IntoCapacityData("instances", maxRootDiskSize, nil)
	b.InstancesSubcapacities = append(b.InstancesSubcapacities, DeprecatedSubcapacity{
		ServiceHost:      h.Hypervisor.Service.Host,
		AggregateName:    h.AggregateName,
		AvailabilityZone: h.AvailabilityZone,
		Capacity:         &hvInstancesCapa.Capacity,
		Usage:            hvInstancesCapa.Usage,
		Traits:           h.Traits,
	})
	hvRAMCapa := pc.IntoCapacityData("ram", maxRootDiskSize, nil)
	b.RAMSubcapacities = append(b.RAMSubcapacities, DeprecatedSubcapacity{
		ServiceHost:      h.Hypervisor.Service.Host,
		AggregateName:    h.AggregateName,
		AvailabilityZone: h.AvailabilityZone,
		Capacity:         &hvRAMCapa.Capacity,
		Usage:            hvRAMCapa.Usage,
		Traits:           h.Traits,
	})
}

func (b *PooledSubcapacityBuilder) AddHypervisor(h MatchingHypervisor, maxRootDiskSize float64) error {
	pc := h.PartialCapacity()

	attrs := SubcapacityAttributes{
		AggregateName: h.AggregateName,
		Traits:        h.Traits,
	}
	buf, err := json.Marshal(attrs)
	if err != nil {
		return fmt.Errorf("while serializing Subcapacity Attributes: %w", err)
	}

	hvCoresCapa := pc.IntoCapacityData("cores", maxRootDiskSize, nil)
	b.CoresSubcapacities = append(b.CoresSubcapacities, liquid.Subcapacity{
		Name:       h.Hypervisor.Service.Host,
		Capacity:   hvCoresCapa.Capacity,
		Usage:      hvCoresCapa.Usage,
		Attributes: json.RawMessage(buf),
	})
	hvInstancesCapa := pc.IntoCapacityData("instances", maxRootDiskSize, nil)
	b.InstancesSubcapacities = append(b.InstancesSubcapacities, liquid.Subcapacity{
		Name:       h.Hypervisor.Service.Host,
		Capacity:   hvInstancesCapa.Capacity,
		Usage:      hvInstancesCapa.Usage,
		Attributes: json.RawMessage(buf),
	})
	hvRAMCapa := pc.IntoCapacityData("ram", maxRootDiskSize, nil)
	b.RAMSubcapacities = append(b.RAMSubcapacities, liquid.Subcapacity{
		Name:       h.Hypervisor.Service.Host,
		Capacity:   hvRAMCapa.Capacity,
		Usage:      hvRAMCapa.Usage,
		Attributes: json.RawMessage(buf),
	})

	return nil
}

// TODO: Remove when switching to liquid-nova
// PooledSubcapacityBuilder is used to build subcapacity lists for split flavors.
// These subcapacities are reported on the first flavor in alphabetic order.
type DeprecatedSplitFlavorSubcapacityBuilder struct {
	Subcapacities []any
}

func (b *DeprecatedSplitFlavorSubcapacityBuilder) AddHypervisor(h MatchingHypervisor) {
	pc := h.PartialCapacity()
	b.Subcapacities = append(b.Subcapacities, DeprecatedSubcapacity{
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
