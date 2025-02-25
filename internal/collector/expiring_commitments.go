/******************************************************************************
*
*  Copyright 2025 SAP SE
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

package collector

import (
	"context"
	"database/sql"
	"maps"
	"slices"
	"time"

	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

const (
	expiringCommitmentsNoticePeriod = 28 * 24 * time.Hour // 4 weeks
)

// Add commitments that are about to expire within the next month into the mail queue.
func (c *Collector) ExpiringCommitmentNotificationJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.ProducerConsumerJob[[]db.ProjectCommitment]{
		Metadata: jobloop.JobMetadata{
			ReadableName: "add expiring commitments to mail queue",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_expiring_commitments_discoveries",
				Help: "Counts jobs that enqueue mail notifications for expiring commitments.",
			},
		},
		DiscoverTask: c.discoverExpiringCommitments,
		ProcessTask:  c.processExpiringCommitmentTask,
	}).Setup(registerer)
}

var (
	discoverExpiringCommitmentsQuery = `SELECT * FROM project_commitments WHERE expires_at <= $1 AND NOT notified_for_expiration`
	locateExpiringCommitmentsQuery   = sqlext.SimplifyWhitespace(`
		SELECT ps.project_id, ps.type, pr.name, par.az, pc.id
		  FROM project_services ps
		  JOIN project_resources pr ON pr.service_id = ps.id
		  JOIN project_az_resources par ON par.resource_id = pr.id
		  JOIN project_commitments pc ON pc.az_resource_id = par.id
		WHERE pc.id = ANY($1)
		ORDER BY ps.type, pr.name, par.az ASC, pc.amount DESC
	`)
	updateCommitmentAsNotifiedQuery = `UPDATE project_commitments SET notified_for_expiration = true WHERE id = ANY($1)`
)

func (c *Collector) discoverExpiringCommitments(_ context.Context, _ prometheus.Labels) (result []db.ProjectCommitment, err error) {
	now := c.MeasureTime()
	cutoff := now.Add(expiringCommitmentsNoticePeriod)
	_, err = c.DB.Select(&result, discoverExpiringCommitmentsQuery, cutoff)
	switch {
	case err != nil:
		return nil, err
	case len(result) == 0:
		return nil, sql.ErrNoRows // instruct the jobloop to slow down
	default:
		return result, nil
	}
}

func (c *Collector) processExpiringCommitmentTask(ctx context.Context, commitments []db.ProjectCommitment, _ prometheus.Labels) error {
	now := c.MeasureTime()
	cutoff := now.Add(expiringCommitmentsNoticePeriod)
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// find which commitments need a notification
	longTermCommitmentsByID := make(map[db.ProjectCommitmentID]db.ProjectCommitment)
	var shortTermCommitmentIDs []db.ProjectCommitmentID
	for _, c := range commitments {
		if c.Duration.AddTo(now).Before(cutoff) {
			shortTermCommitmentIDs = append(shortTermCommitmentIDs, c.ID)
		} else {
			longTermCommitmentsByID[c.ID] = c
		}
	}

	// mark short-term commitments as notified without queueing them
	_, err = tx.Exec(updateCommitmentAsNotifiedQuery, pq.Array(shortTermCommitmentIDs))
	if err != nil {
		return err
	}

	// sort remaining commitments by project
	notifications := make(map[db.ProjectID][]core.CommitmentNotification)
	err = sqlext.ForeachRow(tx, locateExpiringCommitmentsQuery, []any{pq.Array(slices.Collect(maps.Keys(longTermCommitmentsByID)))}, func(rows *sql.Rows) error {
		var (
			pid  db.ProjectID
			cid  db.ProjectCommitmentID
			info core.CommitmentNotification
		)
		err := rows.Scan(&pid, &info.Resource.ServiceType, &info.Resource.ResourceName, &info.Resource.AvailabilityZone, &cid)
		if err != nil {
			return err
		}

		info.Commitment = longTermCommitmentsByID[cid]
		info.DateString = info.Commitment.ExpiresAt.Format(time.DateOnly)
		notifications[pid] = append(notifications[pid], info)
		return nil
	})
	if err != nil {
		return err
	}

	// generate notifications ordered by project_id for deterministic behavior in unit tests
	template := c.Cluster.Config.MailTemplates.ExpiringCommitments
	for _, projectID := range slices.Sorted(maps.Keys(notifications)) {
		var notification core.CommitmentGroupNotification
		commitments := notifications[projectID]
		err := tx.QueryRow("SELECT d.name, p.name FROM domains d JOIN projects p ON d.id = p.domain_id where p.id = $1", projectID).Scan(&notification.DomainName, &notification.ProjectName)
		if err != nil {
			return err
		}
		notification.Commitments = commitments
		mail, err := template.Render(notification, projectID, now)
		if err != nil {
			return err
		}

		err = tx.Insert(&mail)
		if err != nil {
			return err
		}

		commitmentIDs := make([]db.ProjectCommitmentID, len(commitments))
		for idx, c := range commitments {
			commitmentIDs[idx] = c.Commitment.ID
		}
		_, err = tx.Exec(updateCommitmentAsNotifiedQuery, pq.Array(commitmentIDs))
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}
