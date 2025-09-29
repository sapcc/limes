// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"database/sql"
	"maps"
	"slices"
	"time"

	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

const (
	expiringCommitmentsNoticePeriod = 28 * 24 * time.Hour // 4 weeks
)

// ExpiringCommitmentNotificationJob is a jobloop.Job. A task scrapes commitments that are or are about to expire.
// For all applicable commitments within a project the mail content to inform customers will be prepared and added to a queue.
// Long-term commitments will be queued while short-term commitments will only be marked as notified.
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
	discoverExpiringCommitmentsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		SELECT * FROM project_commitments
		 WHERE expires_at <= $1 AND status = {{liquid.CommitmentStatusConfirmed}} AND renew_context_json IS NULL AND NOT notified_for_expiration
	`))
	locateExpiringCommitmentsQuery = sqlext.SimplifyWhitespace(`
		SELECT pc.project_id, s.type, r.name, azr.az, pc.id
		  FROM services s
		  JOIN resources r ON r.service_id = s.id
		  JOIN az_resources azr ON azr.resource_id = r.id
		  JOIN project_commitments pc ON pc.az_resource_id = azr.id
		WHERE pc.id = ANY($1)
		ORDER BY s.type, r.name, azr.az ASC, pc.amount DESC
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

		apiIdentity := c.Cluster.BehaviorForResource(info.Resource.ServiceType, info.Resource.ResourceName).IdentityInV1API
		info.Resource.ServiceType = db.ServiceType(apiIdentity.ServiceType)
		info.Resource.ResourceName = liquid.ResourceName(apiIdentity.Name)
		info.Commitment = longTermCommitmentsByID[cid]
		info.DateString = info.Commitment.ExpiresAt.Format(time.DateOnly)
		notifications[pid] = append(notifications[pid], info)
		return nil
	})
	if err != nil {
		return err
	}

	// generate notifications ordered by project_id for deterministic behavior in unit tests
	mailConfig := c.Cluster.Config.MailNotifications.UnwrapOrPanic("this task should not have been called if mail notifications are not configured")
	template := mailConfig.Templates.ExpiringCommitments
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
