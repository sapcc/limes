// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"testing"

	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

func TestCanAcceptCommitmentChanges(t *testing.T) {
	// trivially acceptable because there is plenty of unassigned capacity
	stats := clusterAZAllocationStats{
		Capacity: 420,
		ProjectStats: map[db.ProjectResourceID]projectAZAllocationStats{
			1: {Committed: 5, Usage: 10},
			2: {Committed: 5, Usage: 10},
			3: {Committed: 5, Usage: 10},
		},
	}
	behavior := core.CommitmentBehavior{
		UntilPercent: Some(100.0),
	}
	additions := map[db.ProjectResourceID]uint64{2: 300}
	result := stats.CanAcceptCommitmentChanges(additions, nil, behavior)
	assert.DeepEqual(t, "CanAcceptCommitmentChanges", result, true)

	// not acceptable: even though there would be enough capacity to cover this commitment,
	// this addition exceeds the portion of capacity that is allowed to be committed
	restrictiveBehavior := core.CommitmentBehavior{
		UntilPercent: Some(50.0),
	}
	additions = map[db.ProjectResourceID]uint64{2: 300}
	result = stats.CanAcceptCommitmentChanges(additions, nil, restrictiveBehavior)
	assert.DeepEqual(t, "CanAcceptCommitmentChanges", result, false)

	// not acceptable because there is not enough spare capacity (30/35 is already covered by allocations)
	stats.Capacity = 35
	additions = map[db.ProjectResourceID]uint64{2: 20}
	result = stats.CanAcceptCommitmentChanges(additions, nil, behavior)
	assert.DeepEqual(t, "CanAcceptCommitmentChanges", result, false)

	// acceptable because this does not move allocations (a commitment is made within a project's existing usage)
	stats.Capacity = 35
	additions = map[db.ProjectResourceID]uint64{2: 5}
	result = stats.CanAcceptCommitmentChanges(additions, nil, behavior)
	assert.DeepEqual(t, "CanAcceptCommitmentChanges", result, true)

	// acceptable! reported capacity is already way overcommitted,
	// but as a special exception, we always allow commitments that cover existing usage
	stats.Capacity = 20
	additions = map[db.ProjectResourceID]uint64{2: 5}
	result = stats.CanAcceptCommitmentChanges(additions, nil, behavior)
	assert.DeepEqual(t, "CanAcceptCommitmentChanges", result, true)

	// acceptable! plain subtractions are always possible, even
	// if the target state has the reported capacity overcommitted
	stats.Capacity = 20
	subtractions := map[db.ProjectResourceID]uint64{2: 3}
	result = stats.CanAcceptCommitmentChanges(nil, subtractions, behavior)
	assert.DeepEqual(t, "CanAcceptCommitmentChanges", result, true)

	// acceptable! reported capacity is overcommitted, but moving an unused commitment from one project
	// to another is always allowed because the total amount of allocations does not increase
	stats.Capacity = 50
	stats.ProjectStats[4] = projectAZAllocationStats{Committed: 50, Usage: 10}
	stats.ProjectStats[5] = projectAZAllocationStats{Committed: 0, Usage: 0}
	additions = map[db.ProjectResourceID]uint64{5: 30}
	subtractions = map[db.ProjectResourceID]uint64{4: 30}
	result = stats.CanAcceptCommitmentChanges(additions, subtractions, behavior)
	assert.DeepEqual(t, "CanAcceptCommitmentChanges", result, true)
}
