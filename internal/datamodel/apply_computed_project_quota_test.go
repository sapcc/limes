/******************************************************************************
*
*  Copyright 2024 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package datamodel

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/sapcc/go-api-declarations/limes"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

//NOTE:
//
//- This suite only tests the functional core in acpqComputeQuotas().
//  The full function is covered by the capacity scrape test.
//- Project service IDs are chosen in the range 400..499 to make them
//  visually distinctive from other integer literals.

func TestACPQBasicWithoutAZAwareness(t *testing.T) {
	// This basic test for a non-AZ-aware resource does not give out all capacity,
	// so it does not matter whether AllowQuotaOvercommit is enabled or not.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"any": {
			Capacity: 250,
			ProjectStats: map[db.ProjectServiceID]projectAZAllocationStats{
				// 401 is a boring base case
				401: constantUsage(30),
				// 402 tests how growth multiplier follows historical usage
				402: {Usage: 50, MinHistoricalUsage: 45, MaxHistoricalUsage: 50},
				// 403 tests how historical usage limits quota shrinking
				403: {Usage: 0, MinHistoricalUsage: 0, MaxHistoricalUsage: 20},
				// 404 tests how commitment guarantees quota even with low usage
				404: {Committed: 60, Usage: 10, MinHistoricalUsage: 8, MaxHistoricalUsage: 12},
				// 405 and 406 test the application of base quota
				405: constantUsage(0),
				406: constantUsage(2),
			},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1.2,
		ProjectBaseQuota: 10,
	}
	for _, cfg.AllowQuotaOvercommit = range []bool{false, true} {
		expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
			"any": {
				401: {Allocated: 36}, // 30 * 1.2 = 36
				402: {Allocated: 54}, // 45 * 1.2 = 54
				403: {Allocated: 20},
				404: {Allocated: 72}, // 60 * 1.2 = 72
				405: {Allocated: 10},
				406: {Allocated: 10},
			},
		})
	}
}

func TestACPQBasicWithAZAwareness(t *testing.T) {
	// This basic test for a non-AZ-aware resource does not give out all capacity,
	// so it does not matter whether AllowQuotaOvercommit is enabled or not.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 200,
			ProjectStats: map[db.ProjectServiceID]projectAZAllocationStats{
				// 401 and 402 are boring base cases with usage only in one AZ or both AZs, respectively
				401: constantUsage(20),
				402: constantUsage(20),
				// 403 tests how growth multiplier follows historical usage
				403: {Usage: 30, MinHistoricalUsage: 28, MaxHistoricalUsage: 30},
				// 404 tests how historical usage limits quota shrinking
				404: {Usage: 5, MinHistoricalUsage: 5, MaxHistoricalUsage: 20},
				// 405 tests how commitment guarantees quota even with low usage,
				// and also that usage in one AZ does not reflect commitments in another
				405: {Committed: 60, Usage: 10, MinHistoricalUsage: 8, MaxHistoricalUsage: 12},
				// 406 and 407 test the application of base quota in "any"
				406: constantUsage(0),
				407: constantUsage(2),
			},
		},
		"az-two": {
			Capacity: 200,
			ProjectStats: map[db.ProjectServiceID]projectAZAllocationStats{
				401: constantUsage(20),
				403: {Usage: 20, MinHistoricalUsage: 19, MaxHistoricalUsage: 20},
				404: {Usage: 0, MinHistoricalUsage: 0, MaxHistoricalUsage: 15},
				405: constantUsage(40),
				406: constantUsage(0),
				407: constantUsage(1),
			},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1.2,
		ProjectBaseQuota: 10,
	}
	for _, cfg.AllowQuotaOvercommit = range []bool{false, true} {
		expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
			"az-one": {
				401: {Allocated: 24},
				402: {Allocated: 24},
				403: {Allocated: 33}, // 28 * 1.2 = 33.6
				404: {Allocated: 20},
				405: {Allocated: 72}, // 60 * 1.2 = 72
				406: {Allocated: 0},
				407: {Allocated: 3}, // 2 * 1.2 = 2.4 rounded to 3 (guaranteed minimum growth)
			},
			"az-two": {
				401: {Allocated: 24},
				402: {Allocated: 0},
				403: {Allocated: 22}, // 19 * 1.2 = 22.8
				404: {Allocated: 15},
				405: {Allocated: 48}, // 40 * 1.2 = 48
				406: {Allocated: 0},
				407: {Allocated: 2}, // 1 * 1.2 = 1.2 rounded to 2 (guaranteed minimum growth)
			},
			"any": {
				401: {Allocated: 0},
				402: {Allocated: 0},
				403: {Allocated: 0},
				404: {Allocated: 0},
				405: {Allocated: 0},
				406: {Allocated: 10},
				407: {Allocated: 5},
			},
		})
	}
}

func TestACPQCapacityLimitsQuotaAllocation(t *testing.T) {
	// This test case checks the priority of capacity allocation.
	// All stages use the same basic scenario, except that capacity will be
	// different in each stage.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"any": {
			Capacity: 0, // set below
			ProjectStats: map[db.ProjectServiceID]projectAZAllocationStats{
				// explained below
				401: constantUsage(20),
				402: {Usage: 50, MinHistoricalUsage: 50, MaxHistoricalUsage: 70},
				403: constantUsage(0),
				404: constantUsage(0),
				405: constantUsage(0),
			},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1.8,
		ProjectBaseQuota: 10,
	}

	// Stage 1: There is enough capacity for the minimum quotas and the desired
	// quotas, but not for the base quotas.
	input["any"] = clusterAZAllocationStats{
		Capacity:     141,
		ProjectStats: input["any"].ProjectStats,
	}
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"any": {
			// 401 and 402 have existing usage and thus are allowed to grow first
			401: {Allocated: 36}, // 20 * 1.8 = 36
			402: {Allocated: 90}, // 50 * 1.8 = 90
			// 403 through 405 have their base quota deprioritized; only 15 capacity
			// is left unallocated, which is distributed fairly among them
			403: {Allocated: 5},
			404: {Allocated: 5},
			405: {Allocated: 5},
		},
	})

	// Stage 2: There is enough capacity for the minimum quotas, but not for the
	// desired quotas.
	input["any"] = clusterAZAllocationStats{
		Capacity:     100,
		ProjectStats: input["any"].ProjectStats,
	}
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"any": {
			// 401 and 402 have minimum quotas of 20 and 70, respectively;
			// the rest is distributed fairly
			401: {Allocated: 24}, // 20 * 1.8 = 36 desired (16 more than minimum) -> +4 granted
			402: {Allocated: 76}, // 50 * 1.8 = 90 desired (20 more than minimum) -> +6 granted
			// we cannot even think about giving out base quotas
			403: {Allocated: 0},
			404: {Allocated: 0},
			405: {Allocated: 0},
		},
	})

	// Stage 3: There is enough capacity for the hard minimum quotas, but not for
	// the soft minimum quotas.
	input["any"] = clusterAZAllocationStats{
		Capacity:     80,
		ProjectStats: input["any"].ProjectStats,
	}
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"any": {
			// 401 and 402 have hard minimum quotas of 20 and 50, respectively;
			// the rest is distributed fairly
			401: {Allocated: 20}, // 20 soft minimum (0 more than hard minimum) -> +0 granted
			402: {Allocated: 60}, // 70 soft minimum (20 more than hard minimum) -> +10 granted
			// we cannot even think about giving out base quotas
			403: {Allocated: 0},
			404: {Allocated: 0},
			405: {Allocated: 0},
		},
	})

	// Stage 4: Capacity is SOMEHOW not even enough for the hard minimum quotas.
	input["any"] = clusterAZAllocationStats{
		Capacity:     20,
		ProjectStats: input["any"].ProjectStats,
	}
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"any": {
			// 401 and 402 have hard minimum quotas of 20 and 50, respectively;
			// those are always granted, even if we overrun the capacity
			401: {Allocated: 20},
			402: {Allocated: 50},
			// we cannot even think about giving out base quotas
			403: {Allocated: 0},
			404: {Allocated: 0},
			405: {Allocated: 0},
		},
	})
}

func TestACPQWithProjectLocalQuotaConstraints(t *testing.T) {
	// This scenario is shared by all subtests in this test.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 10000, // capacity is not a limiting factor here
			ProjectStats: map[db.ProjectServiceID]projectAZAllocationStats{
				401: constantUsage(20),
				402: constantUsage(20),
			},
		},
		"az-two": {
			Capacity: 200,
			ProjectStats: map[db.ProjectServiceID]projectAZAllocationStats{
				401: {Usage: 40, MinHistoricalUsage: 20, MaxHistoricalUsage: 40},
				402: {Usage: 40, MinHistoricalUsage: 40, MaxHistoricalUsage: 60},
			},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1.2,
		ProjectBaseQuota: 100,
	}

	// This baseline does not have project-local quota constraints (for comparison).
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 24},
			402: {Allocated: 24},
		},
		"az-two": {
			401: {Allocated: 40},
			402: {Allocated: 60},
		},
		"any": {
			401: {Allocated: 36},
			402: {Allocated: 16},
		},
	})

	// test with MinQuota constraints
	//
	//NOTE: The balance between AZs is really bad here, but I don't see a good
	// way to do better here. The fairest way (as in "fair balance between AZs")
	// would require waiting for the final result and then adjusting that, but if
	// we don't block minimum quota early on, we may not be able to fulfil it in
	// the end if the capacity is tight and not overcommittable.
	constraints := map[db.ProjectServiceID]projectLocalQuotaConstraints{
		401: {MinQuota: p2u64(200)},
		402: {MinQuota: p2u64(80)},
	}
	expectACPQResult(t, input, cfg, constraints, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 90}, // hard minimum 20, soft minimum 20 -> hard minimum adjusted to 90
			402: {Allocated: 24}, // hard minimum 20, soft minimum 20 -> hard minimum adjusted to 21; then final desired quota 24
		},
		"az-two": {
			401: {Allocated: 110}, // hard minimum 40, soft minimum 40 -> hard minimum adjusted to 110
			402: {Allocated: 60},  // hard minimum 40, soft minimum 60 -> hard minimum adjusted to 59; then final desired quota 60
		},
		"any": {
			401: {Allocated: 0},
			402: {Allocated: 16},
		},
	})

	// test with MaxQuota constraints that constrain the soft minimum (hard minimum is not constrainable)
	constraints = map[db.ProjectServiceID]projectLocalQuotaConstraints{
		401: {MaxQuota: p2u64(50)},
		402: {MaxQuota: p2u64(70)},
	}
	expectACPQResult(t, input, cfg, constraints, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 20}, // hard minimum 20, soft minimum 20 -> unchanged (cannot go below hard minimum)
			402: {Allocated: 20}, // hard minimum 20, soft minimum 20 -> unchanged
		},
		"az-two": {
			401: {Allocated: 40}, // hard minimum 40, soft minimum 40 -> unchanged (cannot go below hard minimum)
			402: {Allocated: 50}, // hard minimum 40, soft minimum 60 -> 50
		},
		"any": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
	})

	// test with MaxQuota constraints that constrain the base quota
	constraints = map[db.ProjectServiceID]projectLocalQuotaConstraints{
		401: {MaxQuota: p2u64(90)},
		402: {MaxQuota: p2u64(90)},
	}
	expectACPQResult(t, input, cfg, constraints, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 24},
			402: {Allocated: 24},
		},
		"az-two": {
			401: {Allocated: 40},
			402: {Allocated: 60},
		},
		"any": {
			401: {Allocated: 26},
			402: {Allocated: 6},
		},
	})
}

// Shortcut to avoid repetition in projectAZAllocationStats literals.
func constantUsage(usage uint64) projectAZAllocationStats {
	return projectAZAllocationStats{
		Usage:              usage,
		MinHistoricalUsage: usage,
		MaxHistoricalUsage: usage,
	}
}

func expectACPQResult(t *testing.T, input map[limes.AvailabilityZone]clusterAZAllocationStats, cfg core.AutogrowQuotaDistributionConfiguration, constraints map[db.ProjectServiceID]projectLocalQuotaConstraints, expected acpqGlobalTarget) {
	t.Helper()
	actual := acpqComputeQuotas(input, cfg, constraints)
	// normalize away any left-over intermediate values
	for _, azTarget := range actual {
		for _, projectTarget := range azTarget {
			projectTarget.Desired = 0
		}
	}

	// We could just assert.DeepEqual() at this point, but the output of that
	// would not be really helpful because fmt.Printf("%#v", ...) stops at
	// pointer boundaries.
	if reflect.DeepEqual(actual, expected) {
		return
	}

	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err.Error())
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil {
		t.Fatal(err.Error())
	}
	actualJSON, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err.Error())
	}
	t.Error("ExpectACPQResult failed")
	t.Logf("    config = %#v", cfg)
	t.Logf("     input = %s", inputJSON)
	t.Logf("  expected = %s", expectedJSON)
	t.Logf("    actual = %s", actualJSON)
}

func p2u64(x uint64) *uint64 {
	return &x
}
