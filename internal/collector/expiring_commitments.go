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
	nextSumbissionInterval          = 2 * time.Minute
)

// Add commitments that are about to expire within the next month into the mail queue.
func (c *Collector) ExpiringCommitmentNotificationJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.ProducerConsumerJob[ExpiringCommitments]{
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

type ExpiringCommitments struct {
	Notifications        map[db.ProjectID][]core.CommitmentNotification
	NextSubmission       time.Time
	ShortTermCommitments []db.ProjectCommitmentID // to be excluded from mail notifications.
}

var (
	findExpiringCommitmentsQuery = sqlext.SimplifyWhitespace(`
		SELECT ps.project_id, ps.type, pr.name, par.az, pc.id, pc.creator_name, pc.amount, pc.duration, pc.expires_at
		  FROM project_services ps
		  JOIN project_resources pr ON pr.service_id = ps.id
		  JOIN project_az_resources par ON par.resource_id = pr.id
		  JOIN project_commitments pc ON pc.az_resource_id = par.id
		WHERE pc.expires_at <= $1 AND NOT pc.notified_for_expiration
		ORDER BY ps.type, pr.name, par.az ASC, pc.amount DESC;
	`)
	updateCommitmentAsNotifiedQuery = `UPDATE project_commitments SET notified_for_expiration = true WHERE id = ANY($1)`
)

func (c *Collector) discoverExpiringCommitments(_ context.Context, _ prometheus.Labels) (ExpiringCommitments, error) {
	now := c.MeasureTime()
	cutoff := now.Add(expiringCommitmentsNoticePeriod)
	commitments := ExpiringCommitments{
		Notifications:  make(map[db.ProjectID][]core.CommitmentNotification),
		NextSubmission: now.Add(c.AddJitter(nextSumbissionInterval)),
	}

	var shortTermCommitments []db.ProjectCommitmentID
	err := sqlext.ForeachRow(c.DB, findExpiringCommitmentsQuery, []any{cutoff}, func(rows *sql.Rows) error {
		var pid db.ProjectID
		var info core.CommitmentNotification
		err := rows.Scan(
			&pid,
			&info.Resource.ServiceType, &info.Resource.ResourceName, &info.Resource.AvailabilityZone,
			&info.Commitment.ID, &info.Commitment.CreatorName, &info.Commitment.Amount, &info.Commitment.Duration, &info.Commitment.ExpiresAt,
		)
		if err != nil {
			return err
		}
		info.DateString = info.Commitment.ExpiresAt.Format(time.DateOnly)
		if info.Commitment.Duration.AddTo(now).Before(cutoff) {
			shortTermCommitments = append(shortTermCommitments, info.Commitment.ID)
		} else {
			commitments.Notifications[pid] = append(commitments.Notifications[pid], info)
		}
		return nil
	})
	if err != nil {
		return ExpiringCommitments{}, err
	}

	commitments.ShortTermCommitments = shortTermCommitments

	return commitments, nil
}

func (c *Collector) processExpiringCommitmentTask(ctx context.Context, task ExpiringCommitments, _ prometheus.Labels) error {
	template := c.Cluster.Config.MailTemplates.ExpiringCommitments
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// mark short-term commitments as notified without queueing them.
	_, err = tx.Exec(updateCommitmentAsNotifiedQuery, pq.Array(task.ShortTermCommitments))
	if err != nil {
		return err
	}

	// sort notifications per project_id in order to have consistent unit tests
	for _, projectID := range slices.Sorted(maps.Keys(task.Notifications)) {
		var notification core.CommitmentGroupNotification
		commitments := task.Notifications[projectID]
		err := tx.QueryRow("SELECT d.name, p.name FROM domains d JOIN projects p ON d.id = p.domain_id where p.id = $1", projectID).Scan(&notification.DomainName, &notification.ProjectName)
		if err != nil {
			return err
		}
		notification.Commitments = commitments
		mail, err := template.Render(notification, projectID, task.NextSubmission)
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
