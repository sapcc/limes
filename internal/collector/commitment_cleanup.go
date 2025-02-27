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

package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/db"
)

// CleanupOldCommitmentsJob is a jobloop.CronJob.
//
// It moves expired commitments to state "expired" and cleans up old expired commitments
// that do not have any non-expired predecessors.
func (c *Collector) CleanupOldCommitmentsJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.CronJob{
		Metadata: jobloop.JobMetadata{
			ReadableName: "cleanup old commitments",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_commitment_cleanups",
				Help: "Counts garbage collection runs for old commitments.",
			},
		},
		Interval: 3 * time.Minute,
		Task:     c.cleanupOldCommitments,
	}).Setup(registerer)
}

func (c *Collector) cleanupOldCommitments(_ context.Context, _ prometheus.Labels) error {
	now := c.MeasureTime()

	// step 1: move commitments to state "expired" if expires_at <= NOW()
	query := fmt.Sprintf(
		`UPDATE project_commitments SET state = '%s' WHERE state != '%s' AND expires_at <= $1`,
		db.CommitmentStateExpired, db.CommitmentStateSuperseded)
	_, err := c.DB.Exec(query, now)
	if err != nil {
		return fmt.Errorf("while moving commitments to state %q: %w", db.CommitmentStateExpired, err)
	}

	// step 2: delete expired commitments after a grace period
	//
	// NOTE: Expired commitments do not contribute to any calculations, so it would
	// be fine to delete them immediately from a technical perspective. However,
	// they don't take up that much space in the short run, and having them stick
	// around in the DB for a little bit (in this case, one month) can
	// potentially help in investigations when customers complain about
	// commitments expiring unexpectedly.
	query = sqlext.SimplifyWhitespace(`
		DELETE FROM project_commitments pc WHERE expires_at + interval '1 month' <= $1
	`)
	_, err = c.DB.Exec(query, now)
	if err != nil {
		return fmt.Errorf("while deleting expired commitments without undeleted successors: %w", err)
	}

	return nil
}
