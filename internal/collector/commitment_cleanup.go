// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/sqlext"
)

// CleanupOldCommitmentsJob is a jobloop.CronJob.
//
// It moves expired commitments to status "expired" and cleans up old expired commitments
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

var (
	expiredCommitmentsUpdateStatusQuery = sqlext.SimplifyWhitespace(fmt.Sprintf(`
		UPDATE project_commitments
		   SET status = '%s'
		 WHERE status != '%s' AND expires_at <= $1
	`, liquid.CommitmentStatusExpired, liquid.CommitmentStatusSuperseded))
	expiredCommitmentsCleanupQuery = sqlext.SimplifyWhitespace(`
		DELETE FROM project_commitments pc
		 WHERE expires_at + interval '1 month' <= $1
	`)
)

func (c *Collector) cleanupOldCommitments(_ context.Context, _ prometheus.Labels) error {
	now := c.MeasureTime()

	// step 1: move commitments to status "expired" if expires_at <= NOW()
	_, err := c.DB.Exec(expiredCommitmentsUpdateStatusQuery, now)
	if err != nil {
		return fmt.Errorf("while moving commitments to status %q: %w", liquid.CommitmentStatusExpired, err)
	}

	// step 2: delete expired commitments after a grace period
	//
	// NOTE: Expired commitments do not contribute to any calculations, so it would
	// be fine to delete them immediately from a technical perspective. However,
	// they don't take up that much space in the short run, and having them stick
	// around in the DB for a little bit (in this case, one month) can
	// potentially help in investigations when customers complain about
	// commitments expiring unexpectedly.
	_, err = c.DB.Exec(expiredCommitmentsCleanupQuery, now)
	if err != nil {
		return fmt.Errorf("while deleting expired commitments without undeleted successors: %w", err)
	}

	return nil
}
