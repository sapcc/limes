/*******************************************************************************
*
* Copyright 2023 SAP SE
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

package core

import (
	"slices"
	"sort"
)

// Topological is a container for data that can either be reported for
// the entire region at once, or separately by AZ.
// Exactly one field shall be non-nil.
type Topological[D TopologicalData[D]] struct {
	Regional *D
	PerAZ    map[string]*D
}

// Regional is a shorthand to construct a Topological instance with the Regional member filled.
func Regional[D TopologicalData[D]](data D) Topological[D] {
	return Topological[D]{Regional: &data}
}

// PerAZ is a shorthand to construct a Topological instance with the PerAZ member filled.
func PerAZ[D TopologicalData[D]](data map[string]*D) Topological[D] {
	return Topological[D]{PerAZ: data}
}

// Sum returns a sum of all data in this container.
// If the Regional field is filled, that data is returned directly.
// Otherwise, all entries in the PerAZ field are summed together.
func (t Topological[D]) Sum() D {
	if t.PerAZ == nil {
		return *t.Regional
	}

	//fold AZ data in a well-defined order for deterministic test result
	azNames := make([]string, 0, len(t.PerAZ))
	for az := range t.PerAZ {
		azNames = append(azNames, az)
	}
	sort.Strings(azNames)

	var result D
	for _, az := range azNames {
		result = result.add(*t.PerAZ[az])
	}
	return result
}

// TopologicalData is an interfaces for types that can be put into the Topological container.
type TopologicalData[Self any] interface {
	// List of permitted types. This is required for type inference, as explained here:
	// <https://stackoverflow.com/a/73851453>
	CapacityData

	// Computes the sum of this structure and `other`.
	// This is used to implement Topological.Sum().
	add(other Self) Self
}

// CapacityData contains capacity data for a single project resource.
type CapacityData struct {
	Capacity      uint64
	Usage         uint64 //NOTE: currently only relevant on AZ level, regional level uses the aggregation of project usages
	Subcapacities []any  //only if supported by plugin and enabled in config
}

// add implements the TopologicalData interface.
//
//nolint:unused // looks like a linter bug
func (d CapacityData) add(other CapacityData) CapacityData {
	return CapacityData{
		Capacity:      d.Capacity + other.Capacity,
		Usage:         d.Usage + other.Usage,
		Subcapacities: append(slices.Clone(d.Subcapacities), other.Subcapacities...),
	}
}
