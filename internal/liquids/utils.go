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

package liquids

import (
	"fmt"
	"slices"
	"sync"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/tokens"
	"github.com/sapcc/go-api-declarations/liquid"
)

// GetProjectIDFromTokenScope returns the project ID from the client's token scope.
//
// This is useful if a request in a non-project-scoped method like Init() or
// BuildServiceInfo() needs to make a request using some project ID.
func GetProjectIDFromTokenScope(provider *gophercloud.ProviderClient) (string, error) {
	result, ok := provider.GetAuthResult().(tokens.CreateResult)
	if !ok {
		return "", fmt.Errorf("%T is not a %T", provider.GetAuthResult(), tokens.CreateResult{})
	}
	project, err := result.ExtractProject()
	if err != nil {
		return "", err
	}
	if project == nil || project.ID == "" {
		return "", fmt.Errorf(`expected "id" attribute in "project" section, but got %#v`, project)
	}
	return project.ID, nil
}

// RestrictToKnownAZs takes a mapping of objects sorted by AZ, and moves all
// objects in unknown AZs into the pseudo-AZ "unknown".
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

// PointerTo casts T into *T without going through a named variable.
func PointerTo[T any](value T) *T {
	return &value
}

// State contains data that is guarded by an RWMutex,
// such that the data cannot be accessed without using the mutex.
// A zero-initialized State contains a zero-initialized piece of data.
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
