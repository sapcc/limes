/*******************************************************************************
*
* Copyright 2019-2024 SAP SE
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

	"github.com/sapcc/limes/internal/core"
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

func (c PartialCapacity) IntoCapacityData(resourceName string, maxRootDiskSize float64, subcapacities []any) core.CapacityData {
	switch resourceName {
	case "cores":
		return core.CapacityData{
			Capacity:      c.VCPUs.Capacity,
			Usage:         &c.VCPUs.Usage,
			Subcapacities: subcapacities,
		}
	case "ram":
		return core.CapacityData{
			Capacity:      c.MemoryMB.Capacity,
			Usage:         &c.MemoryMB.Usage,
			Subcapacities: subcapacities,
		}
	case "instances":
		amount := 10000 * uint64(len(c.MatchingAggregates))
		if maxRootDiskSize != 0 {
			maxAmount := uint64(float64(c.LocalGB.Capacity) / maxRootDiskSize)
			if amount > maxAmount {
				amount = maxAmount
			}
		}
		return core.CapacityData{
			Capacity:      amount,
			Usage:         &c.RunningVMs,
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
