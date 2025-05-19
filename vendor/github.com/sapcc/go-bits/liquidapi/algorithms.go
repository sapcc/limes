// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package liquidapi

import (
	"encoding/json"
	"math"
	"slices"

	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/go-bits/logg"
)

// DistributeFairly takes a number of resource requests, as well as a total
// available capacity, and tries to fulfil all requests as fairly as possible.
//
// If the sum of all requests exceeds the available total, this uses the
// <https://en.wikipedia.org/wiki/Largest_remainder_method>.
func DistributeFairly[K comparable](total uint64, requested map[K]uint64) map[K]uint64 {
	// easy case: all requests can be granted
	sumOfRequests := 0.0
	for _, request := range requested {
		sumOfRequests += float64(request)
	}
	if sumOfRequests <= float64(total) {
		return requested
	}

	// a completely fair distribution would require using these floating-point values...
	exact := make(map[K]float64, len(requested))
	for key, request := range requested {
		exact[key] = float64(total) * float64(request) / sumOfRequests
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
	if missing > uint64(len(keys)) {
		// the algorithm ought to guarantee that the number of `missing`
		// allocations is smaller than the number of keys, but we had this fail in the past,
		// so this crash message is here to generate test cases as necessary
		logg.Fatal("too many missing allocations in DistributeFairly for input: total = %d, requested = %#v", total, requested)
	}
	for _, key := range keys[len(keys)-int(missing):] { //nolint:gosec // algorithm ensures that no overflow happens on uint64 -> int cast
		fair[key] += 1
	}
	return fair
}

// DistributeDemandFairly is used to distribute cluster capacity or cluster-wide usage between different resources.
// Each tier of demand is distributed fairly (while supplies last).
//
// Then anything not yet distributed is split according to the given balance numbers.
// For example, if balance = { "foo": 3, "bar": 1 }, then "foo" gets 3/4 of the remaining capacity, "bar" gets 1/4, and all other resources do not get anything extra.
func DistributeDemandFairly[K comparable](total uint64, demands map[K]liquid.ResourceDemandInAZ, balance map[K]float64) map[K]uint64 {
	// setup phase to make each of the paragraphs below as identical as possible (for clarity)
	requests := make(map[K]uint64)
	result := make(map[K]uint64)
	remaining := total

	// tier 1: usage
	for k, demand := range demands {
		requests[k] = demand.Usage
	}
	grantedAmount := DistributeFairly(remaining, requests)
	for k := range demands {
		remaining -= grantedAmount[k]
		result[k] += grantedAmount[k]
	}
	if logg.ShowDebug {
		resultJSON, err := json.Marshal(result)
		if err == nil {
			logg.Debug("DistributeDemandFairly after phase 1: " + string(resultJSON))
		}
	}

	// tier 2: unused commitments
	for k, demand := range demands {
		requests[k] = demand.UnusedCommitments
	}
	grantedAmount = DistributeFairly(remaining, requests)
	for k := range demands {
		remaining -= grantedAmount[k]
		result[k] += grantedAmount[k]
	}
	if logg.ShowDebug {
		resultJSON, err := json.Marshal(result)
		if err == nil {
			logg.Debug("DistributeDemandFairly after phase 2: " + string(resultJSON))
		}
	}

	// tier 3: pending commitments
	for k, demand := range demands {
		requests[k] = demand.PendingCommitments
	}
	grantedAmount = DistributeFairly(remaining, requests)
	for k := range demands {
		remaining -= grantedAmount[k]
		result[k] += grantedAmount[k]
	}
	if logg.ShowDebug {
		resultJSON, err := json.Marshal(result)
		if err == nil {
			logg.Debug("DistributeDemandFairly after phase 3: " + string(resultJSON))
		}
	}

	// final phase: distribute remainder according to the given balance
	if remaining == 0 {
		return result
	}
	for k := range demands {
		// This requests incorrect ratios if `remaining` and `balance[k]` are so
		// large that `balance[k] * remaining` falls outside the range of uint64.
		//
		// I'm accepting this since this scenario is very unlikely, and only made
		// sure that there are no weird overflows, truncations and such.
		requests[k] = clampFloatToUint64(balance[k] * float64(remaining))
	}
	grantedAmount = DistributeFairly(remaining, requests)
	for k := range demands {
		remaining -= grantedAmount[k]
		result[k] += grantedAmount[k]
	}
	if logg.ShowDebug {
		resultJSON, err := json.Marshal(result)
		if err == nil {
			logg.Debug("DistributeDemandFairly after balance: " + string(resultJSON))
		}
	}

	return result
}

func clampFloatToUint64(x float64) uint64 {
	x = max(x, 0)
	x = min(x, math.MaxUint64)
	return uint64(x)
}
