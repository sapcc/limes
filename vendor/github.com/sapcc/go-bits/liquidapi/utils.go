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

package liquidapi

import (
	"slices"
	"sync"

	"github.com/sapcc/go-api-declarations/liquid"
)

// RestrictToKnownAZs takes a mapping of objects sorted by AZ,
// and moves all objects in unknown AZs into the pseudo-AZ "unknown".
//
// The resulting map will have an entry for each known AZ (possibly a nil slice),
// and at most one additional key (the well-known value "unknown").
func RestrictToKnownAZs[T any](input map[liquid.AvailabilityZone][]T, allAZs []liquid.AvailabilityZone) map[liquid.AvailabilityZone][]T {
	output := make(map[liquid.AvailabilityZone][]T, len(allAZs))
	for _, az := range allAZs {
		output[az] = input[az]
	}
	for az, items := range input {
		if !slices.Contains(allAZs, az) {
			output[liquid.AvailabilityZoneUnknown] = append(output[liquid.AvailabilityZoneUnknown], items...)
		}
	}
	return output
}

// SaturatingSub is like `lhs - rhs`, but never underflows below 0.
func SaturatingSub[T interface{ int | uint64 }](lhs, rhs T) uint64 {
	if lhs < rhs {
		return 0
	}
	return uint64(lhs - rhs)
}

// AtLeastZero safely converts int values (which often appear in Gophercloud types) to uint64 by clamping negative values to 0.
func AtLeastZero(x int) uint64 {
	if x < 0 {
		return 0
	}
	return uint64(x)
}

// State contains data that is guarded by an RWMutex, such that the data cannot be accessed without using the mutex.
// A zero-initialized State contains a zero-initialized piece of data.
//
// This is provided here for implementations of the Logic interface that compute state during BuildServiceInfo().
// See documentation on type Logic.
type State[T any] struct {
	mutex sync.RWMutex
	data  T
}

// Set replaces the contained value.
func (s *State[T]) Set(value T) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.data = value
}

// Get returns a shallow copy of the contained value.
func (s *State[T]) Get() T {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return s.data
}
