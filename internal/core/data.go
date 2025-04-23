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

	. "github.com/majewsky/gg/option"
	"github.com/majewsky/gg/options"
	"github.com/sapcc/go-api-declarations/limes"
)

// PerAZ is a container for data that can be reported for each AZ.
type PerAZ[D AZAwareData[D]] map[limes.AvailabilityZone]*D

// InAnyAZ is a convenience constructor for PerAZ that puts all data in the "any" AZ.
// Use this for data relating to resources that are not AZ-aware.
func InAnyAZ[D AZAwareData[D]](data D) PerAZ[D] {
	return PerAZ[D]{limes.AvailabilityZoneAny: &data}
}

// InUnknownAZUnlessEmpty is a convenience constructor for PerAZ that puts all data in the "unknown" AZ.
// Use this for data relating to AZ-aware resources where the AZ association is unknown.
//
// If the provided data is empty, an empty map is returned instead.
// (We usually only report "unknown" if there is actually something to report.)
func InUnknownAZUnlessEmpty[D AZAwareData[D]](data D) PerAZ[D] {
	if data.isEmpty() {
		return PerAZ[D]{}
	} else {
		return PerAZ[D]{limes.AvailabilityZoneUnknown: &data}
	}
}

// AndZeroInTheseAZs adds zero-valued entries for each of the given AZs, then returns the same map.
//
// This is used for AZ-aware usage reporting when the main API is not AZ-aware.
// The initial UsageData is constructed as `InUnknownAZ(totalUsage).AndZeroInTheseAZs(knownAZs)`.
// Then as we iterate through AZ-localized objects, their respective usage is moved
// from AZ `any` to their specific AZ using AddLocalizedUsage().
func (p PerAZ[D]) AndZeroInTheseAZs(availabilityZones []limes.AvailabilityZone) PerAZ[D] {
	for _, az := range availabilityZones {
		var empty D
		p[az] = &empty
	}
	return p
}

// Clone returns a deep copy of this map.
func (p PerAZ[D]) Clone() PerAZ[D] {
	result := make(PerAZ[D], len(p))
	for az, data := range p {
		cloned := (*data).clone()
		result[az] = &cloned
	}
	return result
}

// Keys returns all availability zones that have entries in this map.
func (p PerAZ[D]) Keys() []limes.AvailabilityZone {
	result := make([]limes.AvailabilityZone, 0, len(p))
	for az := range p {
		result = append(result, az)
	}
	slices.Sort(result)
	return result
}

// Sum returns a sum of all data in this container.
// This can be used if data can only be stored as a whole, not broken down by AZ.
func (p PerAZ[D]) Sum() D {
	// fold AZ data in a well-defined order for deterministic test result
	azNames := make([]limes.AvailabilityZone, 0, len(p))
	for az := range p {
		azNames = append(azNames, az)
	}
	slices.Sort(azNames)

	var (
		result  D
		isFirst = true
	)
	for _, az := range azNames {
		if isFirst {
			result = (*p[az]).clone()
		} else {
			result = result.add(*p[az])
		}
		isFirst = false
	}
	return result
}

// Normalize sums all data for unknown AZs into the pseudo-AZ "unknown".
func (p PerAZ[D]) Normalize(knownAZs []limes.AvailabilityZone) PerAZ[D] {
	unknowns := make(PerAZ[D])
	result := make(PerAZ[D])
	for az, data := range p {
		if az == limes.AvailabilityZoneAny || slices.Contains(knownAZs, az) {
			result[az] = data
		} else {
			unknowns[az] = data
		}
	}
	if len(unknowns) > 0 {
		unknownsSum := unknowns.Sum()
		result[limes.AvailabilityZoneUnknown] = &unknownsSum
	}
	return result
}

// AZAwareData is an interface for types that can be put into the PerAZ container.
type AZAwareData[Self any] interface {
	// List of permitted types. This is required for type inference, as explained here:
	// <https://stackoverflow.com/a/73851453>
	UsageData | CapacityData

	// Makes a deep copy of itself.
	// This is used to implement PerAZ.Sum().
	clone() Self

	// Computes the sum of this structure and `other`.
	// This is used to implement PerAZ.Sum().
	add(other Self) Self

	// Whether this object contains any non-zero data.
	// This is used to implement InUnknownAZUnlessEmpty().
	isEmpty() bool
}

// ResourceData contains quota and usage data for a single project resource.
type ResourceData struct {
	Quota     int64          // negative values indicate infinite quota
	MinQuota  Option[uint64] // if set, indicates that SetQuota will reject values below this level
	MaxQuota  Option[uint64] // if set, indicates that SetQuota will reject values above this level
	UsageData PerAZ[UsageData]
}

// UsageInAZ is like `r.UsageData[az]`, but inserts a new zero-valued UsageData on first access.
// This is useful when calculating AZ-aware usage by iterating through a list of AZ-localized objects.
func (r ResourceData) UsageInAZ(az limes.AvailabilityZone) *UsageData {
	if r.UsageData == nil {
		panic("ResourceData.GetOrCreateEntry cannot operate on a nil UsageData")
	}
	entry := r.UsageData[az]
	if entry == nil {
		entry = &UsageData{}
		r.UsageData[az] = entry
	}
	return entry
}

// AddLocalizedUsage subtracts the given `usage from the `unknown` AZ (if any)
// and adds it to the given AZ instead.
//
// This is used when breaking down a usage total reported by a non-AZ-aware API
// by iterating over AZ-localized objects. If the sum of usage of the
// AZ-localized objects matches the reported usage total, the entry for the
// "unknown" AZ will be removed entirely once it reaches zero usage.
func (r ResourceData) AddLocalizedUsage(az limes.AvailabilityZone, usage uint64) {
	ud := r.UsageData
	if u := ud[limes.AvailabilityZoneUnknown]; u == nil || u.Usage <= usage {
		delete(ud, limes.AvailabilityZoneUnknown)
	} else {
		ud[limes.AvailabilityZoneUnknown].Usage -= usage
	}

	if _, exists := ud[az]; exists {
		ud[az].Usage += usage
	} else {
		ud[az] = &UsageData{Usage: usage}
	}
}

// UsageData contains usage data for a single project resource.
// It appears in type ResourceData.
type UsageData struct {
	Quota         Option[int64]
	Usage         uint64
	PhysicalUsage Option[uint64] // only supported by some plugins
	Subresources  []any          // only if supported by plugin and enabled in config
}

// clone implements the AZAwareData interface.
func (d UsageData) clone() UsageData {
	return UsageData{
		Usage:         d.Usage,
		Subresources:  slices.Clone(d.Subresources),
		PhysicalUsage: d.PhysicalUsage,
	}
}

// add implements the AZAwareData interface.
func (d UsageData) add(other UsageData) UsageData {
	result := UsageData{
		Usage:        d.Usage + other.Usage,
		Subresources: append(slices.Clone(d.Subresources), other.Subresources...),
	}

	// the sum can only have a PhysicalUsage value if both sides have it
	if lhs, ok := d.PhysicalUsage.Unpack(); ok {
		if rhs, ok := other.PhysicalUsage.Unpack(); ok {
			result.PhysicalUsage = Some(lhs + rhs)
		}
	}

	return result
}

// isEmpty implements the AZAwareData interface.
func (d UsageData) isEmpty() bool {
	return d.Usage == 0 && options.IsNoneOrZero(d.PhysicalUsage) && len(d.Subresources) == 0
}

// CapacityData contains capacity data for a single project resource.
type CapacityData struct {
	//NOTE: The json tags are only relevant for the output of `limes test-scan-capacity`.
	Capacity      uint64         `json:"capacity"`
	Usage         Option[uint64] `json:"usage,omitzero"`          //NOTE: currently only relevant on AZ level, regional level uses the aggregation of project usages
	Subcapacities []any          `json:"subcapacities,omitempty"` // only if supported by plugin and enabled in config
}

// clone implements the AZAwareData interface.
func (d CapacityData) clone() CapacityData {
	return CapacityData{
		Capacity:      d.Capacity,
		Usage:         d.Usage,
		Subcapacities: slices.Clone(d.Subcapacities),
	}
}

// add implements the AZAwareData interface.
func (d CapacityData) add(other CapacityData) CapacityData {
	result := CapacityData{
		Capacity:      d.Capacity + other.Capacity,
		Subcapacities: append(slices.Clone(d.Subcapacities), other.Subcapacities...),
	}

	// the sum can only have a Usage value if both sides have it
	if lhs, ok := d.Usage.Unpack(); ok {
		if rhs, ok := other.Usage.Unpack(); ok {
			result.Usage = Some(lhs + rhs)
		}
	}

	return result
}

// isEmpty implements the AZAwareData interface.
func (d CapacityData) isEmpty() bool {
	return d.Capacity == 0 && options.IsNoneOrZero(d.Usage) && len(d.Subcapacities) == 0
}
