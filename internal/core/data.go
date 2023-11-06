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

	"github.com/sapcc/go-api-declarations/limes"
)

// PerAZ is a container for data that can be reported for each AZ.
type PerAZ[D AZAwareData[D]] map[limes.AvailabilityZone]*D

// InAnyAZ is a convenience constructor for PerAZ that puts all data in the "any" AZ.
// Use this for data relating to resources that are not AZ-aware.
func InAnyAZ[D AZAwareData[D]](data D) PerAZ[D] {
	return PerAZ[D]{limes.AvailabilityZoneAny: &data}
}

// InUnknownAZ is a convenience constructor for PerAZ that puts all data in the "any" AZ.
// Use this for data relating to AZ-aware resources where the AZ association is unknown.
func InUnknownAZ[D AZAwareData[D]](data D) PerAZ[D] {
	return PerAZ[D]{limes.AvailabilityZoneUnknown: &data}
}

// ZeroInTheseAZs is a convenience constructor for PerAZ that creates a map with
// zero-valued entries for each of the given AZs.
//
// This is used for AZ-aware usage reporting when the main API is not AZ-aware.
// Plugins will calculate AZ-aware usage by iterating through AZ-localized objects.
// Using this constructor for the PerAZ[UsageData] ensures that each AZ reports
// a usage value, even if the project in question does not have usage in that AZ.
func ZeroInTheseAZs[D AZAwareData[D]](availabilityZones []limes.AvailabilityZone) PerAZ[D] {
	result := make(PerAZ[D], len(availabilityZones))
	for _, az := range availabilityZones {
		var empty D
		result[az] = &empty
	}
	return result
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
	//fold AZ data in a well-defined order for deterministic test result
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
}

// ResourceData contains quota and usage data for a single project resource.
type ResourceData struct {
	Quota     int64 //negative values indicate infinite quota
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

// EnsureTotalUsageNotBelow adds usage to the `unknown` AZ such that the total
// usage is at least as high as the given total.
//
// This is used for AZ-aware usage reporting when the main API is not AZ-aware.
// Plugins will first calculate AZ-aware usage by iterating through AZ-localized objects,
// then enter the non-AZ-aware usage data reported by the backend's quota API using this function.
// Any previously unexplained usage will be added to the AZ `unknown`.

// This does not do any corrections for `usage < totalUsage`,
// because there is not really any way to correct downwards.
func (r ResourceData) EnsureTotalUsageNotBelow(usage uint64) {
	var totalUsage uint64
	for _, u := range r.UsageData {
		totalUsage += u.Usage
	}

	if usage > totalUsage {
		r.UsageInAZ(limes.AvailabilityZoneUnknown).Usage += usage - totalUsage
	}
}

// UsageData contains usage data for a single project resource.
// It appears in type ResourceData.
type UsageData struct {
	Usage         uint64
	PhysicalUsage *uint64 //only supported by some plugins
	Subresources  []any   //only if supported by plugin and enabled in config
}

// clone implements the AZAwareData interface.
//
//nolint:unused // looks like a linter bug
func (d UsageData) clone() UsageData {
	result := UsageData{
		Usage:        d.Usage,
		Subresources: slices.Clone(d.Subresources),
	}
	if d.PhysicalUsage != nil {
		val := *d.PhysicalUsage
		result.PhysicalUsage = &val
	}
	return result
}

// add implements the AZAwareData interface.
//
//nolint:unused // looks like a linter bug
func (d UsageData) add(other UsageData) UsageData {
	result := UsageData{
		Usage:        d.Usage + other.Usage,
		Subresources: append(slices.Clone(d.Subresources), other.Subresources...),
	}

	//the sum can only have a PhysicalUsage value if both sides have it
	if d.PhysicalUsage != nil && other.PhysicalUsage != nil {
		physUsage := *d.PhysicalUsage + *other.PhysicalUsage
		result.PhysicalUsage = &physUsage
	}

	return result
}

// CapacityData contains capacity data for a single project resource.
type CapacityData struct {
	Capacity      uint64
	Usage         *uint64 //NOTE: currently only relevant on AZ level, regional level uses the aggregation of project usages
	Subcapacities []any   //only if supported by plugin and enabled in config
}

// clone implements the AZAwareData interface.
//
//nolint:unused // looks like a linter bug
func (d CapacityData) clone() CapacityData {
	return CapacityData{
		Capacity:      d.Capacity,
		Usage:         d.Usage,
		Subcapacities: slices.Clone(d.Subcapacities),
	}
}

// add implements the AZAwareData interface.
//
//nolint:unused // looks like a linter bug
func (d CapacityData) add(other CapacityData) CapacityData {
	result := CapacityData{
		Capacity:      d.Capacity + other.Capacity,
		Subcapacities: append(slices.Clone(d.Subcapacities), other.Subcapacities...),
	}

	//the sum can only have a PhysicalUsage value if both sides have it
	if d.Usage != nil && other.Usage != nil {
		usage := *d.Usage + *other.Usage
		result.Usage = &usage
	}

	return result
}
