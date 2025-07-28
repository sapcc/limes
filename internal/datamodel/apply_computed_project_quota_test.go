// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"encoding/json"
	"reflect"
	"testing"

	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

//NOTE:
//
//- This suite only tests the functional core in acpqComputeQuotas().
//  The full function is covered by the capacity scrape test.
//- Project IDs are chosen in the range 400..499 to make them
//  visually distinctive from other integer literals.

func TestACPQBasicWithoutAZAwareness(t *testing.T) {
	// This basic test for a non-AZ-aware resource does not give out all capacity,
	// so it does not matter whether quota overcommit is allowed or not.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"any": {
			Capacity: 250,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
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
	for _, cfg.AllowQuotaOvercommitUntilAllocatedPercent = range []float64{0, 10000} {
		expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
			"any": {
				401: {Allocated: 36}, // 30 * 1.2 = 36
				402: {Allocated: 54}, // 45 * 1.2 = 54
				403: {Allocated: 20},
				404: {Allocated: 72}, // 60 * 1.2 = 72
				405: {Allocated: 10},
				406: {Allocated: 10},
			},
		}, liquid.ResourceInfo{Topology: liquid.FlatTopology})
	}
}

func TestACPQBasicWithAZAwareness(t *testing.T) {
	// This basic test for an AZ-aware resource does not give out all capacity,
	// so it does not matter whether quota overcommit is allowed or not.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 200,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
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
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(20),
				402: constantUsage(0),
				403: {Usage: 20, MinHistoricalUsage: 19, MaxHistoricalUsage: 20},
				404: {Usage: 0, MinHistoricalUsage: 0, MaxHistoricalUsage: 15},
				405: constantUsage(40),
				406: constantUsage(0),
				407: constantUsage(1),
			},
		},
		// The scraper creates empty fallback entries in project_az_resources for AZ "any", so we will always see those in the input, too.
		"any": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
				403: {},
				404: {},
				405: {},
				406: {},
				407: {},
			},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1.2,
		ProjectBaseQuota: 10,
	}
	for _, cfg.AllowQuotaOvercommitUntilAllocatedPercent = range []float64{0, 10000} {
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
		}, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})
	}
}

func TestACPQBasicWithAZSeparated(t *testing.T) {
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 200,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
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
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(20),
				402: constantUsage(0),
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

	for _, cfg.AllowQuotaOvercommitUntilAllocatedPercent = range []float64{0, 10000} {
		expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
			"az-one": {
				401: {Allocated: 24},
				402: {Allocated: 24},
				403: {Allocated: 33}, // 28 * 1.2 = 33.6
				404: {Allocated: 20},
				405: {Allocated: 72}, // 60 * 1.2 = 72
				406: {Allocated: 10}, // Basequota
				407: {Allocated: 10}, // Basequota
			},
			"az-two": {
				401: {Allocated: 24},
				402: {Allocated: 0},
				403: {Allocated: 22}, // 19 * 1.2 = 22.8
				404: {Allocated: 15},
				405: {Allocated: 48}, // 40 * 1.2 = 48
				406: {Allocated: 10}, // Basequota
				407: {Allocated: 10}, // Basequota
			},
		}, liquid.ResourceInfo{Topology: liquid.AZSeparatedTopology})
	}
}

func TestACPQCapacityLimitsQuotaAllocation(t *testing.T) {
	// This test case checks the priority of capacity allocation.
	// All stages use the same basic scenario, except that capacity will be
	// different in each stage.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"any": {
			Capacity: 0, // set below
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
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
	}, liquid.ResourceInfo{Topology: liquid.FlatTopology})

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
	}, liquid.ResourceInfo{Topology: liquid.FlatTopology})

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
	}, liquid.ResourceInfo{Topology: liquid.FlatTopology})

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
	}, liquid.ResourceInfo{Topology: liquid.FlatTopology})
}

func TestACPQQuotaOvercommitTurnsOffAboveAllocationThreshold(t *testing.T) {
	// This scenario has a resource that has its capacity 85% allocated to usage and commitments.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 100,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(30),
				402: {Committed: 50, Usage: 10, MinHistoricalUsage: 10, MaxHistoricalUsage: 10},
				403: constantUsage(5),
				// some more empty projects to make sure that we try to distribute more than the available capacity
				404: constantUsage(0),
				405: constantUsage(0),
			},
		},
		// The scraper creates empty fallback entries in project_az_resources for AZ "any", so we will always see those in the input, too.
		"any": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
				403: {},
				404: {},
				405: {},
			},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1.2,
		ProjectBaseQuota: 10,
	}

	// test with quota overcommit allowed (85% allocation is below 90%)
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = 90
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 36}, // 30 * 1.2 = 36
			402: {Allocated: 60}, // 50 * 1.2 = 60
			403: {Allocated: 6},  //  5 * 1.2 =  6
			404: {},
			405: {},
		},
		"any": {
			401: {},
			402: {},
			403: {Allocated: 4},
			404: {Allocated: 10},
			405: {Allocated: 10},
		},
	}, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})

	// test with quota overcommit forbidden (85% allocation is above 80%)
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = 80
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 35}, // 30 * 1.2 = 36, but fair distribution gives only 35
			402: {Allocated: 59}, // 50 * 1.2 = 60, but fair distribution gives only 59
			403: {Allocated: 6},  //  5 * 1.2 =  6
			404: {},
			405: {},
		},
		"any": {
			// there is no capacity left over after growth quota, so base quota is not given out
			401: {},
			402: {},
			403: {},
			404: {},
			405: {},
		},
	}, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})
}

func TestACPQWithProjectLocalQuotaConstraints(t *testing.T) {
	// This scenario is shared by all subtests in this test.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 10000, // capacity is not a limiting factor here
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(20),
				402: constantUsage(20),
			},
		},
		"az-two": {
			Capacity: 200,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {Usage: 40, MinHistoricalUsage: 20, MaxHistoricalUsage: 40},
				402: {Usage: 40, MinHistoricalUsage: 40, MaxHistoricalUsage: 60},
			},
		},
		// The scraper creates empty fallback entries in project_az_resources for AZ "any", so we will always see those in the input, too.
		"any": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
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
	}, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})

	// test with MinQuota constraints
	//
	//NOTE: The balance between AZs is really bad here, but I don't see a good
	// way to do better here. The fairest way (as in "fair balance between AZs")
	// would require waiting for the final result and then adjusting that, but if
	// we don't block minimum quota early on, we may not be able to fulfil it in
	// the end if the capacity is tight and not overcommittable.
	constraints := map[db.ProjectID]projectLocalQuotaConstraints{
		401: {MinQuota: Some[uint64](200)},
		402: {MinQuota: Some[uint64](80)},
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
	}, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})

	// test with MaxQuota constraints that constrain the soft minimum (hard minimum is not constrainable)
	constraints = map[db.ProjectID]projectLocalQuotaConstraints{
		401: {MaxQuota: Some[uint64](50)},
		402: {MaxQuota: Some[uint64](70)},
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
	}, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})

	// test with MaxQuota constraints that constrain the base quota
	constraints = map[db.ProjectID]projectLocalQuotaConstraints{
		401: {MaxQuota: Some[uint64](90)},
		402: {MaxQuota: Some[uint64](90)},
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
	}, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})
}

func TestEmptyRegionDoesNotPrecludeQuotaOvercommit(t *testing.T) {
	// This test is based on real-world data in a three-AZ region.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 15,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				// 401-403 have real usage in az-one and az-three
				401: constantUsage(1),
				402: constantUsage(0),
				403: withCommitted(12, constantUsage(12)),
				// 404-405 are empty projects that should get base quota; this will require overcommit to do
				404: constantUsage(0),
				405: constantUsage(0),
			},
		},
		"az-two": {
			Capacity:                      0,
			ObservedNonzeroCapacityBefore: false,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				// az-two is completely devoid of both capacity and usage
				401: constantUsage(0),
				402: constantUsage(0),
				403: constantUsage(0),
				404: constantUsage(0),
				405: constantUsage(0),
			},
		},
		"az-three": {
			Capacity: 14,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(0),
				402: withCommitted(1, constantUsage(1)),
				403: withCommitted(7, constantUsage(7)),
				404: constantUsage(0),
				405: constantUsage(0),
			},
		},
		"any": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
				403: {},
				404: {},
				405: {},
			},
		},
	}
	// Quota overcommit should always be allowed.
	cfg := core.AutogrowQuotaDistributionConfiguration{
		AllowQuotaOvercommitUntilAllocatedPercent: 10000,
		GrowthMultiplier: 1.2,
		ProjectBaseQuota: 5,
	}

	// There used to be a bug where quota overcommit was not applied because az-two
	// has 0 capacity and 0 usage, so calculating the utilization ratio as
	// (usage / capacity) gave a NaN from divide-by-zero and thus blocked quota
	// overcommit for base quota in the "any" AZ.
	expected := acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 2}, // 1 * 1.2 = 1.2 rounded up because of GrowthMinimum
			402: {Allocated: 0},
			403: {Allocated: 14}, // 12 * 1.2 = 14.4 rounded down
			404: {Allocated: 0},
			405: {Allocated: 0},
		},
		"az-two": {
			401: {Allocated: 0},
			402: {Allocated: 0},
			403: {Allocated: 0},
			404: {Allocated: 0},
			405: {Allocated: 0},
		},
		"az-three": {
			401: {Allocated: 0},
			402: {Allocated: 2}, // 1 * 1.2 = 1.2 rounded up because of GrowthMinimum
			403: {Allocated: 8}, // 7 * 1.2 = 8.4 rounded down
			404: {Allocated: 0},
			405: {Allocated: 0},
		},
		"any": {
			401: {Allocated: 3},
			402: {Allocated: 3},
			403: {Allocated: 0},
			404: {Allocated: 5},
			405: {Allocated: 5},
		},
	}
	expectACPQResult(t, input, cfg, nil, expected, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})

	// Same result if we have seen capacity in az-two before, but now it's gone and there is also no usage.
	// We assume that the AZ is in decom, and do not have it block base quota overcommit, either.
	input["az-two"] = clusterAZAllocationStats{
		Capacity:                      0,
		ObservedNonzeroCapacityBefore: true,
		ProjectStats:                  input["az-two"].ProjectStats,
	}
	expectACPQResult(t, input, cfg, nil, expected, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})

	// The situation looks different if we have seen capacity in az-two before, but now it's gone while usage is still reported.
	// We assume that this is because of a bug in the liquid, and will choose the safe option of disallowing base quota overcommit.
	input["az-two"] = clusterAZAllocationStats{
		Capacity:                      0,
		ObservedNonzeroCapacityBefore: true,
		ProjectStats: map[db.ProjectID]projectAZAllocationStats{
			401: constantUsage(0),
			402: constantUsage(0),
			403: constantUsage(1),
			404: constantUsage(0),
			405: constantUsage(0),
		},
	}
	expected["az-two"] = acpqAZTarget{
		401: {Allocated: 0},
		402: {Allocated: 0},
		403: {Allocated: 1}, // because of existing usage
		404: {Allocated: 0},
		405: {Allocated: 0},
	}
	expected["any"] = acpqAZTarget{
		// after assigning AZ-aware quotas, there is 4 unused capacity in az-three, which gets distributed as base quota
		401: {Allocated: 1},
		402: {Allocated: 1},
		403: {Allocated: 0},
		404: {Allocated: 1},
		405: {Allocated: 1},
	}
	expectACPQResult(t, input, cfg, nil, expected, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})
}

func TestAllForbiddenWithAZSeparated(t *testing.T) {
	resourceInfo := liquid.ResourceInfo{Topology: liquid.AZSeparatedTopology}

	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 100,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(0),
				402: constantUsage(0),
			},
		},
		"az-two": {
			Capacity: 100,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(0),
				402: constantUsage(0),
			},
		},
		"az-three": {
			Capacity: 100,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(0),
				402: constantUsage(0),
			},
		},
		// NOTE: no entry for "any" because this resource has AZSeparatedTopology
	}

	cfg := core.AutogrowQuotaDistributionConfiguration{
		AllowQuotaOvercommitUntilAllocatedPercent: 95,
		GrowthMultiplier: 1.0,
		ProjectBaseQuota: 10,
	}

	constraints := map[db.ProjectID]projectLocalQuotaConstraints{
		// all projects set ResourceUsageReport.Forbidden = true
		401: {MaxQuota: Some[uint64](0)},
		402: {MaxQuota: Some[uint64](0)},
	}

	expectACPQResult(t, input, cfg, constraints, acpqGlobalTarget{
		// NOTE: no quota allocated because of MaxQuota = 0
		"az-one": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		"az-two": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		"az-three": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
	}, resourceInfo)
}

func TestMinQuotaConstraintRespectsAZAwareCapacityDistribution(t *testing.T) {
	// This test is based on real behavior observed in the wild for baremetal
	// flavors that only exist in specific AZs. When enforcing MinQuota overrides,
	// quota should preferably be given in those AZs that have capacity.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"az-two": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"az-three": {
			Capacity: 10, // only AZ with capacity > 0
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"any": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1,
		ProjectBaseQuota: 0,
	}
	constraints := map[db.ProjectID]projectLocalQuotaConstraints{
		401: {MinQuota: Some[uint64](3)},
		402: {MinQuota: Some[uint64](5)},
	}

	expectACPQResult(t, input, cfg, constraints, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		"az-two": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		"az-three": {
			// this is the only AZ with capacity, so the MinQuota should all be allocated here
			401: {Allocated: 3},
			402: {Allocated: 5},
		},
		"any": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
	}, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})

	// Multiple AZs with capacity.
	// Sufficient total capacity for quota demand.
	// Distribute quota w.r.t. available capacity
	input = map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"az-two": {
			Capacity: 1, // Capacity available here as well
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"az-three": {
			Capacity: 10,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"any": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
	}

	constraints = map[db.ProjectID]projectLocalQuotaConstraints{
		401: {MinQuota: Some[uint64](3)},
		402: {MinQuota: Some[uint64](5)},
	}

	expectACPQResult(t, input, cfg, constraints, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		"az-two": {
			401: {Allocated: 1},
			402: {Allocated: 1},
		},
		"az-three": {
			401: {Allocated: 2},
			402: {Allocated: 4},
		},
		"any": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
	}, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})

	// Multiple AZs with capacity.
	// Total capacity can not fully satisfy quota demand.
	// Distribute quota to fulfill min quota constraint ignoring capacity limits.
	// Distribute proportional to available capacity.
	input = map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"az-two": {
			Capacity: 1,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"az-three": {
			Capacity: 2,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"any": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
	}

	constraints = map[db.ProjectID]projectLocalQuotaConstraints{
		401: {MinQuota: Some[uint64](3)},
		402: {MinQuota: Some[uint64](6)},
	}

	expectACPQResult(t, input, cfg, constraints, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		"az-two": {
			401: {Allocated: 1},
			402: {Allocated: 2},
		},
		"az-three": {
			401: {Allocated: 2},
			402: {Allocated: 4},
		},
		"any": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
	}, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})
}

func TestMinQuotaConstraintWithLargeNumbers(t *testing.T) {
	// This tests how min quota overrides deals with very large numbers
	// (as can occur e.g. for Swift capacity measured in bytes).
	// This can be problematic since the min quota distribution is proportional to desire / available capacity.
	val := uint64(200000000000000)

	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: val / 2, // Potential overflow due to capacity scaling
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"az-two": {
			Capacity: val / 6, // Potential overflow due to capacity scaling
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"az-three": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"any": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1,
		ProjectBaseQuota: 0,
	}
	constraints := map[db.ProjectID]projectLocalQuotaConstraints{
		401: {MinQuota: Some[uint64](val)},
	}

	expectACPQResult(t, input, cfg, constraints, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: val / 4 * 3},
			402: {Allocated: 0},
		},
		"az-two": {
			401: {Allocated: val / 4},
			402: {Allocated: 0},
		},
		"az-three": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		"any": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
	}, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})

	input = map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: val,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				// Potential overflow due to desire scaling
				401: {Usage: 0, MinHistoricalUsage: 0, MaxHistoricalUsage: val / 2},
				402: {},
			},
		},
		"az-two": {
			Capacity: val,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				// Potential overflow due to desire scaling
				401: {Usage: 0, MinHistoricalUsage: 0, MaxHistoricalUsage: val / 6},
				402: {},
			},
		},
		"az-three": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
		"any": {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
	}

	constraints = map[db.ProjectID]projectLocalQuotaConstraints{
		401: {MinQuota: Some[uint64](val)},
	}

	expectACPQResult(t, input, cfg, constraints, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: val / 4 * 3},
			402: {Allocated: 0},
		},
		"az-two": {
			401: {Allocated: val / 4},
			402: {Allocated: 0},
		},
		"az-three": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		"any": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
	}, liquid.ResourceInfo{Topology: liquid.AZAwareTopology})
}

// Shortcut to avoid repetition in projectAZAllocationStats literals.
func constantUsage(usage uint64) projectAZAllocationStats {
	return projectAZAllocationStats{
		Usage:              usage,
		MinHistoricalUsage: usage,
		MaxHistoricalUsage: usage,
	}
}

func withCommitted(committed uint64, stats projectAZAllocationStats) projectAZAllocationStats {
	stats.Committed = committed
	return stats
}

func expectACPQResult(t *testing.T, input map[limes.AvailabilityZone]clusterAZAllocationStats, cfg core.AutogrowQuotaDistributionConfiguration, constraints map[db.ProjectID]projectLocalQuotaConstraints, expected acpqGlobalTarget, resourceInfo liquid.ResourceInfo) {
	t.Helper()
	actual, _ := acpqComputeQuotas(input, cfg, constraints, resourceInfo)
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
