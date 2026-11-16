// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

// NOTE: Unlike most tests, this needs to be in the same package as the implementation,
//       because we are constructing and comparing private types.

import (
	"testing"

	"go.xyrillian.de/gg/assert"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

func TestCanAcceptCommitmentChanges(t *testing.T) {
	// trivially acceptable because there is plenty of unassigned capacity
	stats := clusterAZAllocationStats{
		Capacity: 420,
		ProjectStats: map[db.ProjectID]projectAZAllocationStats{
			1: {Committed: 5, Usage: 10},
			2: {Committed: 5, Usage: 10},
			3: {Committed: 5, Usage: 10},
		},
	}
	behavior := core.CommitmentBehavior{
		UntilPercent: Some(100.0),
	}
	additions := map[db.ProjectID]uint64{2: 300}
	result := stats.CanAcceptCommitmentChanges(additions, nil, behavior)
	assert.Equal(t, result, true)

	// not acceptable: even though there would be enough capacity to cover this commitment,
	// this addition exceeds the portion of capacity that is allowed to be committed
	restrictiveBehavior := core.CommitmentBehavior{
		UntilPercent: Some(50.0),
	}
	additions = map[db.ProjectID]uint64{2: 300}
	result = stats.CanAcceptCommitmentChanges(additions, nil, restrictiveBehavior)
	assert.Equal(t, result, false)

	// not acceptable because there is not enough spare capacity (30/35 is already covered by allocations)
	stats.Capacity = 35
	additions = map[db.ProjectID]uint64{2: 20}
	result = stats.CanAcceptCommitmentChanges(additions, nil, behavior)
	assert.Equal(t, result, false)

	// acceptable because this does not move allocations (a commitment is made within a project's existing usage)
	stats.Capacity = 35
	additions = map[db.ProjectID]uint64{2: 5}
	result = stats.CanAcceptCommitmentChanges(additions, nil, behavior)
	assert.Equal(t, result, true)

	// acceptable! reported capacity is already way overcommitted,
	// but as a special exception, we always allow commitments that cover existing usage
	stats.Capacity = 20
	additions = map[db.ProjectID]uint64{2: 5}
	result = stats.CanAcceptCommitmentChanges(additions, nil, behavior)
	assert.Equal(t, result, true)

	// acceptable! plain subtractions are always possible, even
	// if the target state has the reported capacity overcommitted
	stats.Capacity = 20
	subtractions := map[db.ProjectID]uint64{2: 3}
	result = stats.CanAcceptCommitmentChanges(nil, subtractions, behavior)
	assert.Equal(t, result, true)

	// acceptable! reported capacity is overcommitted, but moving an unused commitment from one project
	// to another is always allowed because the total amount of allocations does not increase
	stats.Capacity = 50
	stats.ProjectStats[4] = projectAZAllocationStats{Committed: 50, Usage: 10}
	stats.ProjectStats[5] = projectAZAllocationStats{Committed: 0, Usage: 0}
	additions = map[db.ProjectID]uint64{5: 30}
	subtractions = map[db.ProjectID]uint64{4: 30}
	result = stats.CanAcceptCommitmentChanges(additions, subtractions, behavior)
	assert.Equal(t, result, true)
}

func assertReduction(t *testing.T, stats clusterAZAllocationStats, cfg core.AutogrowQuotaDistributionConfiguration, expectedReduction, expectedReductionInAny uint8) {
	t.Helper()
	reduction, reductionInAny := stats.computeQuotaOvercommitReduction(cfg)
	assert.Equal(t, reduction, expectedReduction)
	assert.Equal(t, reductionInAny, expectedReductionInAny)
}

func TestComputeQuotaOvercommitReduction(t *testing.T) {
	stats := clusterAZAllocationStats{
		Capacity: 50,
		ProjectStats: map[db.ProjectID]projectAZAllocationStats{
			1: {Committed: 5, Usage: 15},
			2: {Committed: 10, Usage: 10},
			3: {Committed: 15, Usage: 10},
		},
	}
	cfg := core.AutogrowQuotaDistributionConfiguration{
		AllowQuotaOvercommitUntilAllocatedPercent: None[float64](),
	}

	// no limit - no reduction
	assertReduction(t, stats, cfg, 0, 0)

	// limit high enough - no reduction
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(float64(99))
	assertReduction(t, stats, cfg, 0, 0)

	// astronomically low utilization (no underflows)
	stats.Capacity = 1 << 63
	assertReduction(t, stats, cfg, 0, 0)

	// like the previous one, but with a lower border
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(float64(98))
	assertReduction(t, stats, cfg, 0, 0)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = None[float64]()

	// astronomically high utilization (no overflows)
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(float64(50))
	stats.Capacity = 1
	assertReduction(t, stats, cfg, 100, 100)

	// like the previous one, but with a lower border
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(float64(40))
	assertReduction(t, stats, cfg, 100, 100)
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = None[float64]()

	stats.Capacity = 50

	// exactly on the edge - already reduced
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(float64(40) / float64(50) * 100)
	assertReduction(t, stats, cfg, 100, 100)

	// other case (below) - reduction
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(float64(50))
	assertReduction(t, stats, cfg, 100, 100)

	// below both borders
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(float64(99))
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(float64(95))
	assertReduction(t, stats, cfg, 0, 0)

	// exactly on the edge, first 20% reduction
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(float64(90))
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(float64(80))
	assertReduction(t, stats, cfg, 20, 20)

	// 50% -> 60%
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(float64(90))
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(float64(70))
	assertReduction(t, stats, cfg, 60, 60)

	// 100% --> 100%
	cfg.AllowQuotaOvercommitUntilAllocatedPercent = Some(float64(80))
	cfg.ReduceQuotaOvercommitFromAllocatedPercent = Some(float64(70))
	assertReduction(t, stats, cfg, 100, 100)

	// special case - no capacity and no capacity in the past, but usage
	stats.Capacity = 0
	assertReduction(t, stats, cfg, 100, 0)

	// special case - no capacity and capacity in the past with usage
	stats.ObservedNonzeroCapacityBefore = true
	assertReduction(t, stats, cfg, 100, 100)

	// special case - no capacity and capacity in the past with no usage
	stats.ProjectStats = nil
	assertReduction(t, stats, cfg, 100, 0)
}
