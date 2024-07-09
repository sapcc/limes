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

package datamodel

import (
	"testing"

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
	behavior := core.ResourceBehavior{
		CommitmentUntilPercent: p2f64(100),
	}
	additions := map[db.ProjectResourceID]uint64{2: 300}
	result := stats.CanAcceptCommitmentChanges(additions, nil, behavior)
	assert.DeepEqual(t, "CanAcceptCommitmentChanges", result, true)

	// not acceptable: even though there would be enough capacity to cover this commitment,
	// this addition exceeds the portion of capacity that is allowed to be committed
	restrictiveBehavior := core.ResourceBehavior{
		CommitmentUntilPercent: p2f64(50),
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
}

func p2f64(value float64) *float64 {
	return &value
}
