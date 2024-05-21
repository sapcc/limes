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
	"testing"

	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/db"
)

// NOTE: Most test coverage for DistributeFairly() is implicit as part of datamodel.ApplyComputedProjectQuota().

func TestDistributeFairlyWithLargeNumbers(t *testing.T) {
	// This tests how DistributeFairly() deals with very large numbers
	// (as can occur e.g. for Swift capacity measured in bytes).
	// We used to have a crash here because of an overflowing uint64 multiplication.
	total := uint64(200000000000000)
	requested := map[db.ProjectServiceID]uint64{
		401: total / 2,
		402: total / 2,
		403: total / 2,
		404: total / 2,
	}
	result := DistributeFairly(total, requested)
	assert.DeepEqual(t, "output of DistributeFairly", result, map[db.ProjectServiceID]uint64{
		401: total / 4,
		402: total / 4,
		403: total / 4,
		404: total / 4,
	})
}
