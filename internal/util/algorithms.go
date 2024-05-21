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

package util

import (
	"math"
	"slices"
)

// DistributeFairly takes a number of resource requests, as well as a total
// available capacity, and tries to fulfil all requests as fairly as possible.
//
// If the sum of all requests exceeds the available total, this uses the
// <https://en.wikipedia.org/wiki/Largest_remainder_method>.
func DistributeFairly[K comparable](total uint64, requested map[K]uint64) map[K]uint64 {
	// easy case: all requests can be granted
	sumOfRequests := uint64(0)
	for _, request := range requested {
		sumOfRequests += request
	}
	if sumOfRequests <= total {
		return requested
	}

	// a completely fair distribution would require using these floating-point values...
	exact := make(map[K]float64, len(requested))
	for key, request := range requested {
		exact[key] = float64(total) * float64(request) / float64(sumOfRequests)
	}

	// ...but we have to round to uint64
	fair := make(map[K]uint64, len(requested))
	keys := make([]K, 0, len(requested))
	totalOfFair := uint64(0)
	for key := range requested {
		floor := uint64(math.Floor(exact[key]))
		fair[key] = floor
		totalOfFair += floor
		keys = append(keys, key)
	}

	// now we have `sum(fair) <= total` because the fractional parts were ignored;
	// to fix this, we distribute one more to the highest fractional parts, e.g.
	//
	//    total = 15
	//    requested = [ 4, 6, 7 ]
	//    exact = [ 3.529..., 5.294..., 6.176... ]
	//    fair before adjustment = [ 3, 5, 6 ]
	//    missing = 1
	//    fair after adjustment = [ 4, 5, 6 ] -> because exact[0] had the largest fractional part
	//
	missing := total - totalOfFair
	slices.SortFunc(keys, func(lhs, rhs K) int {
		leftRemainder := exact[lhs] - math.Floor(exact[lhs])
		rightRemainder := exact[rhs] - math.Floor(exact[rhs])
		switch {
		case leftRemainder < rightRemainder:
			return -1
		case leftRemainder > rightRemainder:
			return +1
		default:
			return 0
		}
	})
	for _, key := range keys[len(keys)-int(missing):] {
		fair[key] += 1
	}
	return fair
}
