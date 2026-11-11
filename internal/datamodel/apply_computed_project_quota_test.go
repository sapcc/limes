// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

// NOTE: Unlike most tests, this needs to be in the same package as the implementation,
//       because we are constructing and comparing private types.

import (
	"encoding/json"
	"testing"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// NOTE:
//
// - This suite only tests the functional core in acpqComputeQuotas().
//  The full function is covered by the capacity scrape test.
// - Project IDs are chosen in the range 400..499 to make them
//  visually distinctive from other integer literals.

type acpqOvercommitTestConfig struct {
	fromAllocatedPercent  Option[float64]
	untilAllocatedPercent Option[float64]
	expectedReductions    map[limes.AvailabilityZone]uint8
}

func TestACPQBasicWithoutAZAwareness(t *testing.T) {
	// This basic test for a non-AZ-aware resource does not give out all capacity,
	// so it does not matter whether quota overcommit is allowed or not.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		liquid.AvailabilityZoneAny: {
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
	for _, ocCfg := range []acpqOvercommitTestConfig{
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: None[float64](), expectedReductions: map[limes.AvailabilityZone]uint8{limes.AvailabilityZoneAny: 0}},
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: Some(0.0), expectedReductions: map[limes.AvailabilityZone]uint8{limes.AvailabilityZoneAny: 100}},
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: Some(99.0), expectedReductions: map[limes.AvailabilityZone]uint8{limes.AvailabilityZoneAny: 0}},
		{fromAllocatedPercent: Some(0.0), untilAllocatedPercent: Some(99.0), expectedReductions: map[limes.AvailabilityZone]uint8{limes.AvailabilityZoneAny: 60}},
	} {
		cfg.ReduceQuotaOvercommitFromAllocatedPercent = ocCfg.fromAllocatedPercent
		cfg.AllowQuotaOvercommitUntilAllocatedPercent = ocCfg.untilAllocatedPercent
		expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
			liquid.AvailabilityZoneAny: {
				401: {Allocated: 36}, // 30 * 1.2 = 36
				402: {Allocated: 54}, // 45 * 1.2 = 54
				403: {Allocated: 20},
				404: {Allocated: 72}, // 60 * 1.2 = 72
				405: {Allocated: 10},
				406: {Allocated: 10},
			},
			liquid.AvailabilityZoneTotal: {
				401: {Allocated: 36}, // 30 * 1.2 = 36
				402: {Allocated: 54}, // 45 * 1.2 = 54
				403: {Allocated: 20},
				404: {Allocated: 72}, // 60 * 1.2 = 72
				405: {Allocated: 10},
				406: {Allocated: 10},
			},
		},
			db.Resource{Topology: liquid.FlatTopology},
			ocCfg.expectedReductions)
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
				// 408 tests that unknown usage causes equally distributed quota in a "real" AZ --> never ever, we want quota in unknown
				408: constantUsage(10),
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
				408: constantUsage(10),
			},
		},
		// The scraper creates empty fallback entries in project_az_resources for AZ "any", so we will always see those in the input, too.
		liquid.AvailabilityZoneAny: {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
				403: {},
				404: {},
				405: {},
				406: {},
				407: {},
				408: {},
			},
		},
		// unknown only appears, when it exists
		liquid.AvailabilityZoneUnknown: {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				408: constantUsage(20),
			},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1.2,
		ProjectBaseQuota: 10,
	}
	for _, ocCfg := range []acpqOvercommitTestConfig{
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: None[float64](), expectedReductions: map[limes.AvailabilityZone]uint8{limes.AvailabilityZoneAny: 0, "az-one": 0, "az-two": 0}},
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: Some(0.0), expectedReductions: map[limes.AvailabilityZone]uint8{limes.AvailabilityZoneAny: 100, "az-one": 100, "az-two": 100}},
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: Some(99.0), expectedReductions: map[limes.AvailabilityZone]uint8{limes.AvailabilityZoneAny: 0, "az-one": 0, "az-two": 0}},
		{fromAllocatedPercent: Some(0.0), untilAllocatedPercent: Some(99.0), expectedReductions: map[limes.AvailabilityZone]uint8{limes.AvailabilityZoneAny: 60, "az-one": 60, "az-two": 40}},
	} {
		cfg.ReduceQuotaOvercommitFromAllocatedPercent = ocCfg.fromAllocatedPercent
		cfg.AllowQuotaOvercommitUntilAllocatedPercent = ocCfg.untilAllocatedPercent
		expectACPQResult(t, input, cfg, map[db.ProjectID]projectLocalQuotaConstraints{408: {MinQuota: Some(uint64(12))}}, acpqGlobalTarget{
			"az-one": {
				401: {Allocated: 24},
				402: {Allocated: 24},
				403: {Allocated: 33}, // 28 * 1.2 = 33.6
				404: {Allocated: 20},
				405: {Allocated: 72}, // 60 * 1.2 = 72
				406: {Allocated: 0},
				407: {Allocated: 3}, // 2 * 1.2 = 2.4 rounded to 3 (guaranteed minimum growth)
				408: {Allocated: 20},
			},
			"az-two": {
				401: {Allocated: 24},
				402: {Allocated: 0},
				403: {Allocated: 22}, // 19 * 1.2 = 22.8
				404: {Allocated: 15},
				405: {Allocated: 48}, // 40 * 1.2 = 48
				406: {Allocated: 0},
				407: {Allocated: 2}, // 1 * 1.2 = 1.2 rounded to 2 (guaranteed minimum growth)
				408: {Allocated: 20},
			},
			liquid.AvailabilityZoneAny: {
				401: {Allocated: 0},
				402: {Allocated: 0},
				403: {Allocated: 0},
				404: {Allocated: 0},
				405: {Allocated: 0},
				406: {Allocated: 10},
				407: {Allocated: 5},
				408: {Allocated: 0},
			},
			liquid.AvailabilityZoneTotal: {
				401: {Allocated: 48},  // 24 + 24
				402: {Allocated: 24},  // 24 + 0
				403: {Allocated: 55},  // 33 + 22
				404: {Allocated: 35},  // 20 + 15
				405: {Allocated: 120}, // 72 + 48
				406: {Allocated: 10},  // 0 + 0 + 10
				407: {Allocated: 10},  // 3 + 2 + 5
				408: {Allocated: 40},  // 20 + 20 + 0
			},
		},
			db.Resource{Topology: liquid.AZAwareTopology},
			ocCfg.expectedReductions)
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

	for _, ocCfg := range []acpqOvercommitTestConfig{
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: None[float64](), expectedReductions: map[limes.AvailabilityZone]uint8{"az-one": 0, "az-two": 0}},
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: Some(0.0), expectedReductions: map[limes.AvailabilityZone]uint8{"az-one": 100, "az-two": 100}},
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: Some(99.0), expectedReductions: map[limes.AvailabilityZone]uint8{"az-one": 0, "az-two": 0}},
		{fromAllocatedPercent: Some(0.0), untilAllocatedPercent: Some(99.0), expectedReductions: map[limes.AvailabilityZone]uint8{"az-one": 60, "az-two": 40}},
	} {
		cfg.ReduceQuotaOvercommitFromAllocatedPercent = ocCfg.fromAllocatedPercent
		cfg.AllowQuotaOvercommitUntilAllocatedPercent = ocCfg.untilAllocatedPercent
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
			liquid.AvailabilityZoneTotal: {
				401: {Allocated: 48}, // 24 + 24
				402: {Allocated: 24},
				403: {Allocated: 55},  // 33 + 22
				404: {Allocated: 35},  // 20 + 15
				405: {Allocated: 120}, // 72 + 48
				406: {Allocated: 20},  // 10 + 10
				407: {Allocated: 20},  // 10 + 10
			},
		},
			db.Resource{Topology: liquid.AZSeparatedTopology},
			ocCfg.expectedReductions)
	}
}

func TestACPQCapacityLimitsQuotaAllocation(t *testing.T) {
	// This test case checks the priority of capacity allocation.
	// All stages use the same basic scenario, except that capacity will be
	// different in each stage.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		liquid.AvailabilityZoneAny: {
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
		AllowQuotaOvercommitUntilAllocatedPercent: Some(0.0), // this test specifically covers how quota is distributed fairly when overcommit is not possible
		GrowthMultiplier: 1.8,
		ProjectBaseQuota: 10,
	}

	// Stage 1: There is enough capacity for the minimum quotas and the desired
	// quotas, but not for the base quotas.
	input[liquid.AvailabilityZoneAny] = clusterAZAllocationStats{
		Capacity:     141,
		ProjectStats: input[liquid.AvailabilityZoneAny].ProjectStats,
	}
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		liquid.AvailabilityZoneAny: {
			// 401 and 402 have existing usage and thus are allowed to grow first
			401: {Allocated: 36}, // 20 * 1.8 = 36
			402: {Allocated: 90}, // 50 * 1.8 = 90
			// 403 through 405 have their base quota deprioritized; only 15 capacity
			// is left unallocated, which is distributed fairly among them
			403: {Allocated: 5},
			404: {Allocated: 5},
			405: {Allocated: 5},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 36},
			402: {Allocated: 90},
			403: {Allocated: 5},
			404: {Allocated: 5},
			405: {Allocated: 5},
		},
	},
		db.Resource{Topology: liquid.FlatTopology},
		map[liquid.AvailabilityZone]uint8{liquid.AvailabilityZoneAny: 100})

	// Stage 1b: We simulate quota overcommit is limited by 40% (25%-50% in the window)
	// allocation is 70/141 ~ 50%
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(65.0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(40.0)
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		liquid.AvailabilityZoneAny: {
			// 401 and 402 are not limited, so these values are the same!
			401: {Allocated: 36}, // 20 * 1.8 = 36
			402: {Allocated: 90}, // 50 * 1.8 = 90
			// 403 through 405, they would have 5 when limited fully and 10 when not:
			// 5 + ((10-5)*60%) = 5 + 3 = 8
			403: {Allocated: 8},
			404: {Allocated: 8},
			405: {Allocated: 8},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 36},
			402: {Allocated: 90},
			403: {Allocated: 8},
			404: {Allocated: 8},
			405: {Allocated: 8},
		},
	},
		db.Resource{Topology: liquid.FlatTopology},
		map[liquid.AvailabilityZone]uint8{liquid.AvailabilityZoneAny: 40})

	// Stage 2: There is enough capacity for the minimum quotas, but not for the
	// desired quotas.
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(0.0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = None[float64]()
	input[liquid.AvailabilityZoneAny] = clusterAZAllocationStats{
		Capacity:     100,
		ProjectStats: input[liquid.AvailabilityZoneAny].ProjectStats,
	}
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		liquid.AvailabilityZoneAny: {
			// 401 and 402 have minimum quotas of 20 and 70, respectively;
			// the rest is distributed fairly
			401: {Allocated: 24}, // 20 * 1.8 = 36 desired (16 more than minimum) -> +4 granted
			402: {Allocated: 76}, // 50 * 1.8 = 90 desired (20 more than minimum) -> +6 granted
			// we cannot even think about giving out base quotas
			403: {Allocated: 0},
			404: {Allocated: 0},
			405: {Allocated: 0},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 24},
			402: {Allocated: 76},
			403: {Allocated: 0},
			404: {Allocated: 0},
			405: {Allocated: 0},
		},
	},
		db.Resource{Topology: liquid.FlatTopology},
		map[liquid.AvailabilityZone]uint8{liquid.AvailabilityZoneAny: 100})

	// Stage 2b: We simulate quota overcommit is limited by 60% (50%-75% in the window)
	// allocation is 70/100 = 70%
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(80.0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(60.0)
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		liquid.AvailabilityZoneAny: {
			// 401 and 402 would get 36 and 90 with no limiting and 24 and 76 with limiting
			401: {Allocated: 29}, // 24 + ((36-24)*40%) = 24 + 5 = 29
			402: {Allocated: 82}, // 76 + ((90-76)*40%) = 76 + 6 = 82
			// for these, nothing would be left (0), and they want to be at 10
			// 10 * 0,4 = 4
			403: {Allocated: 4},
			404: {Allocated: 4},
			405: {Allocated: 4},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 29},
			402: {Allocated: 82},
			403: {Allocated: 4},
			404: {Allocated: 4},
			405: {Allocated: 4},
		},
	},
		db.Resource{Topology: liquid.FlatTopology},
		map[liquid.AvailabilityZone]uint8{liquid.AvailabilityZoneAny: 60})

	// Stage 3: There is enough capacity for the hard minimum quotas, but not for
	// the soft minimum quotas.
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(0.0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = None[float64]()
	input[liquid.AvailabilityZoneAny] = clusterAZAllocationStats{
		Capacity:     80,
		ProjectStats: input[liquid.AvailabilityZoneAny].ProjectStats,
	}
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		liquid.AvailabilityZoneAny: {
			// 401 and 402 have hard minimum quotas of 20 and 50, respectively;
			// the rest is distributed fairly
			401: {Allocated: 20}, // 20 soft minimum (0 more than hard minimum) -> +0 granted
			402: {Allocated: 60}, // 70 soft minimum (20 more than hard minimum) -> +10 granted
			// we cannot even think about giving out base quotas
			403: {Allocated: 0},
			404: {Allocated: 0},
			405: {Allocated: 0},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 20},
			402: {Allocated: 60},
			403: {Allocated: 0},
			404: {Allocated: 0},
			405: {Allocated: 0},
		},
	},
		db.Resource{Topology: liquid.FlatTopology},
		map[liquid.AvailabilityZone]uint8{liquid.AvailabilityZoneAny: 100})

	// Stage 3b: We simulate quota overcommit is limited by 20% (0%-25% in the window)
	// allocation is 70/80 = 87,5%
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(95.0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(87.0)
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		liquid.AvailabilityZoneAny: {
			// 401 and 402 would get 36 and 90 with no limiting and 20 and 60 with limiting
			// 402 happens in two stages because we are at hard minimum - this is special!
			401: {Allocated: 33}, // 20 + ((36-20)*80%) = 20 + 13 = 33
			402: {Allocated: 86}, // 60 + ((70-60)*80%) + ((90-68)*80%) = 60 + 8 + 18 = 86
			// for these, nothing would be left (0), and they want to be at 10
			// 10 * 0,8 = 8
			403: {Allocated: 8},
			404: {Allocated: 8},
			405: {Allocated: 8},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 33},
			402: {Allocated: 86},
			403: {Allocated: 8},
			404: {Allocated: 8},
			405: {Allocated: 8},
		},
	},
		db.Resource{Topology: liquid.FlatTopology},
		map[liquid.AvailabilityZone]uint8{liquid.AvailabilityZoneAny: 20})

	// Stage 4: Capacity is SOMEHOW not even enough for the hard minimum quotas.
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(0.0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = None[float64]()
	input[liquid.AvailabilityZoneAny] = clusterAZAllocationStats{
		Capacity:     20,
		ProjectStats: input[liquid.AvailabilityZoneAny].ProjectStats,
	}
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		liquid.AvailabilityZoneAny: {
			// 401 and 402 have hard minimum quotas of 20 and 50, respectively;
			// those are always granted, even if we overrun the capacity
			401: {Allocated: 20},
			402: {Allocated: 50},
			// we cannot even think about giving out base quotas
			403: {Allocated: 0},
			404: {Allocated: 0},
			405: {Allocated: 0},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 20},
			402: {Allocated: 50},
			403: {Allocated: 0},
			404: {Allocated: 0},
			405: {Allocated: 0},
		},
	},
		db.Resource{Topology: liquid.FlatTopology},
		map[liquid.AvailabilityZone]uint8{liquid.AvailabilityZoneAny: 100})

	// Realistically, there cannot be a case where we are not at 100% overcommit reduction
}

func TestACPQSoftMinPhaseExhaustsCapacity(t *testing.T) {
	// This test covers the regression where the soft-minimum phase (phase 2)
	// drains all remaining capacity in a real AZ. With partial overcommit
	// reduction active, the subsequent desired and base phases must still
	// apply the reduction-scaled interpolation even though availableCapacity
	// is zero.
	//
	// Other tests cover this in topology=flat, so we only test the others here.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 100,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				// 401 has a large commitment that dominates the hard minimum;
				// its soft minimum (MaxHistoricalUsage 40) is already below the committed amount,
				// so it has no soft-minimum demand
				401: {Committed: 60, Usage: 20, MinHistoricalUsage: 20, MaxHistoricalUsage: 40},
				// 402 drives the soft-minimum exhaustion: hard minimum 28, soft minimum 45,
				// demand 17 against only 12 remaining capacity after hard minimums (100 - 88 = 12)
				402: {Usage: 28, MinHistoricalUsage: 30, MaxHistoricalUsage: 45},
			},
		},
		"az-two": {
			Capacity: 20,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {Usage: 2, MinHistoricalUsage: 2, MaxHistoricalUsage: 4},
			},
		},
		liquid.AvailabilityZoneAny: {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1.3,
		ProjectBaseQuota: 5,
	}

	// az-one utilization is 88% (hard mins 60+28=88 of 100) -> 40% reduction in az-one.
	// az-two utilization is 10% (2 of 20) -> below the window -> 0% reduction.
	//
	// Phase 2 (soft minimum): az-one has 12 remaining, 402 requests 17 -> granted 12 -> 40.
	//   Interpolation: upper 45, lower 40, 40 + ((45-40)*0.6) = 40 + 3 = 43.
	//   az-one is now full (60+43 = 103 > 100).
	//   az-two has reduction 0 -> 402 gets full soft minimum 4.
	//
	// Phase 3 (desired): az-one availableCapacity = 0.
	//   401: desired 78 (60*1.3), 60 + ((78-60)*0.6) = 60 + 11 = 71.
	//   402: desired 39 (30*1.3) < allocated (43) -> no change.
	//   az-two: 402 desired 3 (max(2*1.3, 2+1)) < allocated 4 -> no change.
	//
	// Phase 4 (base quota 5): both projects exceed 5 in localized AZs -> no "any" demand.
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(85.0)
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(95.0)
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 71}, // 60 -> 71 (phase 3, interpolation)
			402: {Allocated: 43}, // 28 -> 40 (grant) -> 43 (phase 2, interpolation)
		},
		"az-two": {
			401: {Allocated: 0},
			402: {Allocated: 4}, // full soft minimum (no reduction in az-two)
		},
		liquid.AvailabilityZoneAny: {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 71},
			402: {Allocated: 47},
		},
	},
		db.Resource{Topology: liquid.AZAwareTopology},
		map[limes.AvailabilityZone]uint8{"az-one": 40, "az-two": 0, limes.AvailabilityZoneAny: 40})
}

func TestCapacityLimitsBaseQuotaAllocation(t *testing.T) {
	// first, we assemble a flat case where the capacity is lower than the base quota
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		liquid.AvailabilityZoneAny: {
			Capacity: 4,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(2),
				402: constantUsage(0),
			},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1.5,
		ProjectBaseQuota: 6,
		AllowQuotaOvercommitUntilAllocatedPercent: Some(90.0),
	}
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		liquid.AvailabilityZoneAny: {
			401: {Allocated: 4},
			402: {Allocated: 4},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 4},
			402: {Allocated: 4},
		},
	},
		db.Resource{Topology: liquid.FlatTopology},
		map[liquid.AvailabilityZone]uint8{liquid.AvailabilityZoneAny: 0})

	// flat with 40% reduction
	// allocation is 2/4 = 50%
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(60.0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(45.0)
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		liquid.AvailabilityZoneAny: {
			// 401 gets 2 + ((3-2)*60%) = 2 + 1 = 3 in the growth round and then 3 + ((4-3)*60%) = 3 + 1 = 4 in the base round
			401: {Allocated: 4},
			// 402 gets nothing in the growth round, 1 through fair distribution and 1 + ((4-1)*60%) = 1 + 2 = 3 in the base round
			402: {Allocated: 3},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 4},
			402: {Allocated: 3},
		},
	},
		db.Resource{Topology: liquid.FlatTopology},
		map[liquid.AvailabilityZone]uint8{liquid.AvailabilityZoneAny: 40})

	// az-aware
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(90.0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = None[float64]()
	input = map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 4,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(2),
				402: constantUsage(0),
			},
		},
		"az-two": {
			Capacity: 5,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(0),
				402: constantUsage(0),
			},
		},
		liquid.AvailabilityZoneAny: {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
	}
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 3},
			402: {Allocated: 0},
		},
		"az-two": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		liquid.AvailabilityZoneAny: {
			401: {Allocated: 3},
			402: {Allocated: 6},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 6},
			402: {Allocated: 6},
		},
	},
		db.Resource{Topology: liquid.AZAwareTopology},
		getZeroOvercommitReduction("az-one", "az-two", limes.AvailabilityZoneAny))

	// az-aware with 40% reduction in az-one (and any)
	// allocation is 2/4 = 50% in az-one and 0/5 = 0% in az-two
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(60.0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(45.0)
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"az-one": {
			// 401's growth is fully covered by own free capacity
			401: {Allocated: 3},
			402: {Allocated: 0},
		},
		"az-two": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		liquid.AvailabilityZoneAny: {
			// 401 has 2 units backed by real capacity, so 2 + ((3-2)*60% = 2 + 1 = 3 --> no change
			401: {Allocated: 3},
			// 402 has 4 units backed by real capacity, so 4 + ((6-4)*60% = 4 + 1 = 5
			402: {Allocated: 5},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 6},
			402: {Allocated: 5},
		},
	},
		db.Resource{Topology: liquid.AZAwareTopology},
		map[limes.AvailabilityZone]uint8{"az-one": 40, "az-two": 0, limes.AvailabilityZoneAny: 40})

	// to illustrate the above example more, let's reduce by 60%
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(55.0)
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"az-one": {
			// 401's growth is fully covered by own free capacity
			401: {Allocated: 3},
			402: {Allocated: 0},
		},
		"az-two": {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		liquid.AvailabilityZoneAny: {
			// 401 has 2 units backed by real capacity, so 2 + ((3-2)*40% = 2 + 0 = 2
			401: {Allocated: 2},
			// 402 has 4 units backed by real capacity, so 4 + ((6-4)*40% = 4 + 1 = 5
			402: {Allocated: 5},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 5},
			402: {Allocated: 5},
		},
	},
		db.Resource{Topology: liquid.AZAwareTopology},
		map[limes.AvailabilityZone]uint8{"az-one": 60, "az-two": 0, limes.AvailabilityZoneAny: 60})

	// az-separated
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(90.0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = None[float64]()
	input = map[limes.AvailabilityZone]clusterAZAllocationStats{
		"az-one": {
			Capacity: 4,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(2),
				402: constantUsage(0),
			},
		},
		"az-two": {
			Capacity: 5,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(0),
				402: constantUsage(0),
			},
		},
	}
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 4},
			402: {Allocated: 4},
		},
		"az-two": {
			401: {Allocated: 5},
			402: {Allocated: 5},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 9},
			402: {Allocated: 9},
		},
	},
		db.Resource{Topology: liquid.AZSeparatedTopology},
		getZeroOvercommitReduction("az-one", "az-two"))

	// az-separated with 40% reduction in az-one
	// allocation is 2/4 = 50% in az-one and 0/5 = 0% in az-two
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(60.0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(45.0)
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"az-one": {
			// base quota target is min(6, az-one capacity 4) = 4 for both projects;
			// 1 real unit remains after 401's growth (cap 4 - alloc 3 = 1), fair distribution
			// (requests 1 vs 4) gives it to 402, then 60% of the uncovered gap:
			401: {Allocated: 4}, // 3 + 0 real + (4-3)*60% = 3 + 1 = 4
			402: {Allocated: 3}, // 0 + 1 real + (4-1)*60% = 1 + 2 = 3
		},
		"az-two": {
			// az-two has 0% utilization -> no reduction, full base quota min(6, 5) = 5
			401: {Allocated: 5},
			402: {Allocated: 5},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 9}, // 4 + 5
			402: {Allocated: 8}, // 3 + 5
		},
	},
		db.Resource{Topology: liquid.AZSeparatedTopology},
		map[limes.AvailabilityZone]uint8{"az-one": 40, "az-two": 0})
}

func TestACPQQuotaOvercommitTurnsOffAboveAllocationThreshold(t *testing.T) {
	// This scenario has a resource that has its capacity 85% allocated to usage and commitments in az-one.
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
		// The second az has much lower utilization to show that they behave independently for all topologies
		"az-two": {
			Capacity: 100,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: constantUsage(0),
				402: constantUsage(0),
				403: constantUsage(1),
				// some more empty projects to make sure that we try to distribute more than the available capacity
				404: constantUsage(0),
				405: constantUsage(0),
			},
		},
		// The scraper creates empty fallback entries in project_az_resources for AZ "any", so we will always see those in the input, too.
		liquid.AvailabilityZoneAny: {
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
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(90.0)
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 36}, // 30 * 1.2 = 36
			402: {Allocated: 60}, // 50 * 1.2 = 60
			403: {Allocated: 6},  //  5 * 1.2 =  6
			404: {},
			405: {},
		},
		"az-two": {
			401: {Allocated: 0},
			402: {Allocated: 0},
			403: {Allocated: 2}, // 1  * 1.2 = 1.2 rounded to 2 (guaranteed minimum growth)
			404: {},
			405: {},
		},
		liquid.AvailabilityZoneAny: {
			401: {},
			402: {},
			403: {Allocated: 2},
			404: {Allocated: 10},
			405: {Allocated: 10},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 36}, // 36 + 0
			402: {Allocated: 60}, // 60 + 0
			403: {Allocated: 10}, // 6 + 4
			404: {Allocated: 10}, // 10 + 0
			405: {Allocated: 10}, // 10 + 0
		},
	},
		db.Resource{Topology: liquid.AZAwareTopology},
		getZeroOvercommitReduction("az-one", "az-two", limes.AvailabilityZoneAny))

	// test with quota overcommit forbidden = no "allow_quota_overcommit" in az-one (85% allocation is above 80%)
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(80.0)
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 35}, // 30 * 1.2 = 36, but fair distribution gives only 35
			402: {Allocated: 59}, // 50 * 1.2 = 60, but fair distribution gives only 59
			403: {Allocated: 6},  //  5 * 1.2 =  6
			404: {},
			405: {},
		},
		"az-two": {
			401: {Allocated: 0},
			402: {Allocated: 0},
			403: {Allocated: 2}, // 1  * 1.2 = 1.2 rounded to 2 (guaranteed minimum growth)
			404: {},
			405: {},
		},
		liquid.AvailabilityZoneAny: {
			// we get base quota in sum, because az-two still has capacity
			401: {},
			402: {},
			403: {Allocated: 2},
			404: {Allocated: 10},
			405: {Allocated: 10},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 35},
			402: {Allocated: 59},
			403: {Allocated: 10},
			404: {Allocated: 10},
			405: {Allocated: 10},
		},
	},
		db.Resource{Topology: liquid.AZAwareTopology},
		map[limes.AvailabilityZone]uint8{"az-one": 100, "az-two": 0, liquid.AvailabilityZoneAny: 100})

	// now with 40% reduction - which basically has no impact, as the reduction is only by 1 unit
	// allocation is 85% in az-one and 2% in az-two, so az-one is reduced by 40% and az-two is not reduced
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(91.0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(80.0)
	expectACPQResult(t, input, cfg, nil, acpqGlobalTarget{
		"az-one": {
			401: {Allocated: 36}, // 35 is backed by real capacity --> 35 + ((36-35)*0,6 = 35 + 1
			402: {Allocated: 60}, // 59 is backed by real capacity --> 59 + ((60-59)*0,6 = 59 + 1
			403: {Allocated: 6},  //  5 * 1.2 =  6
			404: {},
			405: {},
		},
		"az-two": {
			401: {Allocated: 0},
			402: {Allocated: 0},
			403: {Allocated: 2}, // 1  * 1.2 = 1.2 rounded to 2 (guaranteed minimum growth)
			404: {},
			405: {},
		},
		liquid.AvailabilityZoneAny: {
			// we get base quota in sum, because az-two still has capacity
			401: {},
			402: {},
			403: {Allocated: 2},
			404: {Allocated: 10},
			405: {Allocated: 10},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 36},
			402: {Allocated: 60},
			403: {Allocated: 10},
			404: {Allocated: 10},
			405: {Allocated: 10},
		},
	},
		db.Resource{Topology: liquid.AZAwareTopology},
		map[limes.AvailabilityZone]uint8{"az-one": 40, "az-two": 0, liquid.AvailabilityZoneAny: 40})
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
		liquid.AvailabilityZoneAny: {
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

	// We run all constraint scenarios with two overcommit configs to show that
	// the reduction has no impact here: capacity is so abundant (az-one: 10000,
	// az-two: 200) that every grant is fully backed by real capacity, leaving
	// gaps of 0 for the reduction to act on.
	for _, ocCfg := range []acpqOvercommitTestConfig{
		// no reduction configured
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: None[float64](), expectedReductions: getZeroOvercommitReduction("az-one", "az-two", limes.AvailabilityZoneAny)},
		// az-two is at 40% utilization -> 40% reduction; az-one is at 0.4% -> no reduction; any mirrors the max (40%)
		{fromAllocatedPercent: Some(35.0), untilAllocatedPercent: Some(50.0), expectedReductions: map[limes.AvailabilityZone]uint8{"az-one": 0, "az-two": 40, limes.AvailabilityZoneAny: 40}},
	} {
		cfg.ReduceQuotaOvercommitFromAllocatedPercent = ocCfg.fromAllocatedPercent
		cfg.AllowQuotaOvercommitUntilAllocatedPercent = ocCfg.untilAllocatedPercent

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
			liquid.AvailabilityZoneAny: {
				401: {Allocated: 36},
				402: {Allocated: 16},
			},
			liquid.AvailabilityZoneTotal: {
				401: {Allocated: 100}, // 24 + 40 + 36
				402: {Allocated: 100}, // 24 + 60 + 16
			},
		},
			db.Resource{Topology: liquid.AZAwareTopology},
			ocCfg.expectedReductions)

		// test with MinQuota constraints
		//
		// NOTE: The balance between AZs is really bad here, but I don't see a good
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
			liquid.AvailabilityZoneAny: {
				401: {Allocated: 0},
				402: {Allocated: 16},
			},
			liquid.AvailabilityZoneTotal: {
				401: {Allocated: 200}, // 90 + 110 + 0
				402: {Allocated: 100}, // 24 + 60 + 16
			},
		},
			db.Resource{Topology: liquid.AZAwareTopology},
			ocCfg.expectedReductions)

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
			liquid.AvailabilityZoneAny: {
				401: {Allocated: 0},
				402: {Allocated: 0},
			},
			liquid.AvailabilityZoneTotal: {
				401: {Allocated: 60}, // 20 + 40 + 0
				402: {Allocated: 70}, // 20 + 50 + 0
			},
		},
			db.Resource{Topology: liquid.AZAwareTopology},
			ocCfg.expectedReductions)

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
			liquid.AvailabilityZoneAny: {
				401: {Allocated: 26},
				402: {Allocated: 6},
			},
			liquid.AvailabilityZoneTotal: {
				401: {Allocated: 90}, // 24 + 40 + 26
				402: {Allocated: 90}, // 24 + 60 + 6
			},
		},
			db.Resource{Topology: liquid.AZAwareTopology},
			ocCfg.expectedReductions)
	}
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
		liquid.AvailabilityZoneAny: {
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
		AllowQuotaOvercommitUntilAllocatedPercent: None[float64](),
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
		liquid.AvailabilityZoneAny: {
			401: {Allocated: 3},
			402: {Allocated: 3},
			403: {Allocated: 0},
			404: {Allocated: 5},
			405: {Allocated: 5},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 5},  // 2 + 0 + 0 + 3
			402: {Allocated: 5},  // 0 + 0 + 2 + 3
			403: {Allocated: 22}, // 14 + 0 + 8 + 0
			404: {Allocated: 5},  // 0 + 0 + 0 + 5
			405: {Allocated: 5},  // 0 + 0 + 0 + 5
		},
	}
	expectACPQResult(t, input, cfg, nil, expected, db.Resource{Topology: liquid.AZAwareTopology}, map[limes.AvailabilityZone]uint8{"az-one": 0, "az-two": 100, "az-three": 0, liquid.AvailabilityZoneAny: 0})

	// Same result if we have seen capacity in az-two before, but now it's gone and there is also no usage.
	// We assume that the AZ is in decom, and do not have it block base quota overcommit, either.
	input["az-two"] = clusterAZAllocationStats{
		Capacity:                      0,
		ObservedNonzeroCapacityBefore: true,
		ProjectStats:                  input["az-two"].ProjectStats,
	}
	expectACPQResult(t, input, cfg, nil, expected, db.Resource{Topology: liquid.AZAwareTopology}, map[limes.AvailabilityZone]uint8{"az-one": 0, "az-two": 100, "az-three": 0, liquid.AvailabilityZoneAny: 0})

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
	expected[liquid.AvailabilityZoneAny] = acpqAZTarget{
		// after assigning AZ-aware quotas, there is 4 unused capacity in az-three, which gets distributed as base quota
		401: {Allocated: 1},
		402: {Allocated: 1},
		403: {Allocated: 0},
		404: {Allocated: 1},
		405: {Allocated: 1},
	}
	expected[liquid.AvailabilityZoneTotal] = acpqAZTarget{
		401: {Allocated: 3},  // 2 + 0 + 0 + 1
		402: {Allocated: 3},  // 0 + 0 + 2 + 1
		403: {Allocated: 23}, // 14 + 1 + 8 + 0
		404: {Allocated: 1},  // 0 + 0 + 0 + 1
		405: {Allocated: 1},  // 0 + 0 + 0 + 1
	}
	expectACPQResult(t, input, cfg, nil, expected, db.Resource{Topology: liquid.AZAwareTopology}, map[limes.AvailabilityZone]uint8{"az-one": 0, "az-two": 100, "az-three": 0, liquid.AvailabilityZoneAny: 100})
}

func TestAllForbiddenWithAZSeparated(t *testing.T) {
	resource := db.Resource{Topology: liquid.AZSeparatedTopology}

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
		AllowQuotaOvercommitUntilAllocatedPercent: Some(95.0),
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
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
	}, resource,
		getZeroOvercommitReduction("az-one", "az-two", "az-three"))
}

func TestMinQuotaConstraintRespectsAZAwareCapacityDistribution(t *testing.T) {
	// This test is based on real behavior observed in the wild for baremetal
	// flavors that only exist in specific AZs. When enforcing MinQuota overrides,
	// quota should preferably be given in those AZs that have capacity.
	//
	// MinQuota is enforced by EnforceConstraints(), which directly adjusts
	// Allocated. Since GrowthMultiplier is 1 and ProjectBaseQuota is 0, there
	// is no growth demand and no base quota demand, so TryFulfillDesired() sees
	// no outstanding requests and the overcommit reduction has nothing to act on.
	// We run each scenario with two overcommit configs to demonstrate this.
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1,
		ProjectBaseQuota: 0,
	}

	// Scenario 1: single AZ with capacity
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
		liquid.AvailabilityZoneAny: {
			Capacity: 0,
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				401: {},
				402: {},
			},
		},
	}
	constraints := map[db.ProjectID]projectLocalQuotaConstraints{
		401: {MinQuota: Some[uint64](3)},
		402: {MinQuota: Some[uint64](5)},
	}

	for _, ocCfg := range []acpqOvercommitTestConfig{
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: None[float64](), expectedReductions: map[liquid.AvailabilityZone]uint8{"az-one": 100, "az-two": 100, "az-three": 0, liquid.AvailabilityZoneAny: 0}},
		{fromAllocatedPercent: Some(0.0), untilAllocatedPercent: Some(1.0), expectedReductions: map[liquid.AvailabilityZone]uint8{"az-one": 100, "az-two": 100, "az-three": 20, liquid.AvailabilityZoneAny: 20}},
	} {
		cfg.ReduceQuotaOvercommitFromAllocatedPercent = ocCfg.fromAllocatedPercent
		cfg.AllowQuotaOvercommitUntilAllocatedPercent = ocCfg.untilAllocatedPercent
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
			liquid.AvailabilityZoneAny: {
				401: {Allocated: 0},
				402: {Allocated: 0},
			},
			liquid.AvailabilityZoneTotal: {
				401: {Allocated: 3},
				402: {Allocated: 5},
			},
		},
			db.Resource{Topology: liquid.AZAwareTopology},
			ocCfg.expectedReductions)
	}

	// Scenario 2: Multiple AZs with capacity.
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
		liquid.AvailabilityZoneAny: {
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

	for _, ocCfg := range []acpqOvercommitTestConfig{
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: None[float64](), expectedReductions: map[liquid.AvailabilityZone]uint8{"az-one": 100, "az-two": 0, "az-three": 0, liquid.AvailabilityZoneAny: 0}},
		{fromAllocatedPercent: Some(0.0), untilAllocatedPercent: Some(1.0), expectedReductions: map[liquid.AvailabilityZone]uint8{"az-one": 100, "az-two": 20, "az-three": 20, liquid.AvailabilityZoneAny: 20}},
	} {
		cfg.ReduceQuotaOvercommitFromAllocatedPercent = ocCfg.fromAllocatedPercent
		cfg.AllowQuotaOvercommitUntilAllocatedPercent = ocCfg.untilAllocatedPercent
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
			liquid.AvailabilityZoneAny: {
				401: {Allocated: 0},
				402: {Allocated: 0},
			},
			liquid.AvailabilityZoneTotal: {
				401: {Allocated: 3}, // 0 + 1 + 2 + 0
				402: {Allocated: 5}, // 0 + 1 + 4 + 0
			},
		},
			db.Resource{Topology: liquid.AZAwareTopology},
			ocCfg.expectedReductions)
	}

	// Scenario 3: Multiple AZs with capacity.
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
		liquid.AvailabilityZoneAny: {
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

	for _, ocCfg := range []acpqOvercommitTestConfig{
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: None[float64](), expectedReductions: map[liquid.AvailabilityZone]uint8{"az-one": 100, "az-two": 0, "az-three": 0, liquid.AvailabilityZoneAny: 0}},
		{fromAllocatedPercent: Some(0.0), untilAllocatedPercent: Some(1.0), expectedReductions: map[liquid.AvailabilityZone]uint8{"az-one": 100, "az-two": 20, "az-three": 20, liquid.AvailabilityZoneAny: 20}},
	} {
		cfg.ReduceQuotaOvercommitFromAllocatedPercent = ocCfg.fromAllocatedPercent
		cfg.AllowQuotaOvercommitUntilAllocatedPercent = ocCfg.untilAllocatedPercent
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
			liquid.AvailabilityZoneAny: {
				401: {Allocated: 0},
				402: {Allocated: 0},
			},
			liquid.AvailabilityZoneTotal: {
				401: {Allocated: 3}, // 0 + 1 + 2 + 0
				402: {Allocated: 6}, // 0 + 2 + 4 + 0
			},
		},
			db.Resource{Topology: liquid.AZAwareTopology},
			ocCfg.expectedReductions)
	}
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
		liquid.AvailabilityZoneAny: {
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
		liquid.AvailabilityZoneAny: {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: val},
			402: {Allocated: 0},
		},
	},
		db.Resource{Topology: liquid.AZAwareTopology},
		map[liquid.AvailabilityZone]uint8{"az-one": 0, "az-two": 0, "az-three": 100, liquid.AvailabilityZoneAny: 0})

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
		liquid.AvailabilityZoneAny: {
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
		liquid.AvailabilityZoneAny: {
			401: {Allocated: 0},
			402: {Allocated: 0},
		},
		liquid.AvailabilityZoneTotal: {
			401: {Allocated: val},
			402: {Allocated: 0},
		},
	},
		db.Resource{Topology: liquid.AZAwareTopology},
		map[liquid.AvailabilityZone]uint8{"az-one": 0, "az-two": 0, "az-three": 100, liquid.AvailabilityZoneAny: 0})
}

func TestACPQFlatTopologyWithOverrideQuotaAndUsage(t *testing.T) {
	// This test reproduces a production scenario for a flat-topology resource where:
	// - there is existing usage on az=any (e.g. 844)
	// - a quota override is configured that is larger than the base quota (1200 > 1000)
	// - growth multiplier is 1.0 (no growth)
	//
	// The override should be the sole determinant of the total quota.
	// In particular, usage on az=any must NOT be added on top of the override.
	//
	// We run with two overcommit configs to show that the reduction has no impact:
	// capacity (10000) dwarfs total quota demand (3400), so every grant is fully
	// backed by real capacity and the reduction has nothing to act on.
	input := map[limes.AvailabilityZone]clusterAZAllocationStats{
		liquid.AvailabilityZoneAny: {
			Capacity: 10000, // capacity is not a limiting factor
			ProjectStats: map[db.ProjectID]projectAZAllocationStats{
				// 401 has significant usage and an override quota larger than base quota
				401: constantUsage(844),
				// 402 has no usage but also gets the override
				402: constantUsage(0),
				// 403 has usage but no override (just base quota)
				403: constantUsage(500),
			},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		GrowthMultiplier: 1.0,
		ProjectBaseQuota: 1000,
	}
	constraints := map[db.ProjectID]projectLocalQuotaConstraints{
		// override_quota_from_config = 1200 sets both MinQuota and MaxQuota to 1200
		401: {MinQuota: Some[uint64](1200), MaxQuota: Some[uint64](1200)},
		402: {MinQuota: Some[uint64](1200), MaxQuota: Some[uint64](1200)},
	}

	for _, ocCfg := range []acpqOvercommitTestConfig{
		{fromAllocatedPercent: None[float64](), untilAllocatedPercent: None[float64](), expectedReductions: map[limes.AvailabilityZone]uint8{limes.AvailabilityZoneAny: 0}},
		// utilization is ~13.4% (1344/10000) -> second quarter of [10,20] -> 40% reduction
		{fromAllocatedPercent: Some(10.0), untilAllocatedPercent: Some(20.0), expectedReductions: map[limes.AvailabilityZone]uint8{limes.AvailabilityZoneAny: 40}},
	} {
		cfg.ReduceQuotaOvercommitFromAllocatedPercent = ocCfg.fromAllocatedPercent
		cfg.AllowQuotaOvercommitUntilAllocatedPercent = ocCfg.untilAllocatedPercent
		expectACPQResult(t, input, cfg, constraints, acpqGlobalTarget{
			liquid.AvailabilityZoneAny: {
				401: {Allocated: 1200}, // override quota, NOT usage+override
				402: {Allocated: 1200}, // override quota
				403: {Allocated: 1000}, // base quota (usage 500 < base quota 1000)
			},
			liquid.AvailabilityZoneTotal: {
				401: {Allocated: 1200}, // must equal az=any, NOT 1200+844
				402: {Allocated: 1200},
				403: {Allocated: 1000},
			},
		},
			db.Resource{Topology: liquid.FlatTopology},
			ocCfg.expectedReductions)
	}
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

func expectACPQResult(t *testing.T, input map[limes.AvailabilityZone]clusterAZAllocationStats, cfg core.AutogrowQuotaDistributionConfiguration, constraints map[db.ProjectID]projectLocalQuotaConstraints, expected acpqGlobalTarget, resource db.Resource, expectedAllowsQuotaOvercommit map[limes.AvailabilityZone]uint8) {
	t.Helper()
	actual, actualAllowsQuotaOvercommit := acpqComputeQuotas(input, cfg, constraints, resource.Topology)
	// normalize away any left-over intermediate values
	for _, azTarget := range actual {
		for _, projectTarget := range azTarget {
			projectTarget.Desired = 0
		}
	}

	if !assert.Equal(t, actual, expected) || !assert.Equal(t, actualAllowsQuotaOvercommit, expectedAllowsQuotaOvercommit) {
		t.Logf("config was %#v", cfg)
		t.Logf("input was %s", must.ReturnT(json.Marshal(input))(t))
	}
}

func getZeroOvercommitReduction(zones ...limes.AvailabilityZone) map[limes.AvailabilityZone]uint8 {
	result := make(map[limes.AvailabilityZone]uint8, len(zones))
	for _, zone := range zones {
		result[zone] = 0
	}
	return result
}
