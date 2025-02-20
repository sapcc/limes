/******************************************************************************
*
*  Copyright 2024 SAP SE
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
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
)

// Add commitments that are about to expire within the next month into the mail queue.
func (c *Collector) AddExpiringCommitmentsAsMailJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.ProducerConsumerJob[ExpiringCommitments]{
		Metadata: jobloop.JobMetadata{
			ReadableName: "add expiring commitments to mail queue",
			CounterOpts: prometheus.CounterOpts{
				Name: "expiring_commitments_to_mail",
				Help: "Counts syncs to the notification queue",
			},
		},
		DiscoverTask: func(ctx context.Context, labels prometheus.Labels) (ExpiringCommitments, error) {
			return c.discoverExpiringCommitments(ctx, labels)
		},
		ProcessTask: c.processExpiringCommitmentTask,
	}).Setup(registerer)
}

type ExpiringCommitments struct {
	Notifications  map[db.ProjectID][]datamodel.CommitmentInfo
	NextSubmission time.Time
}

var (
	findExpiringCommitments = sqlext.SimplifyWhitespace(`
		SELECT ps.project_id, ps.type, pr.name, par.az, pc.id, pc.creator_name, pc.amount, pc.duration, pc.expires_at
		  FROM project_services ps
		  JOIN project_resources pr ON pr.service_id = ps.id
		  JOIN project_az_resources par ON par.resource_id = pr.id
		  JOIN project_commitments pc ON pc.az_resource_id = par.id
		WHERE pc.expires_at BETWEEN NOW() AND DATE_TRUNC('month', NOW()) + Interval '2 month - 1 day';
	`)
)

// TODO: Detect short term commitments. Also add unit tests.
func (c *Collector) discoverExpiringCommitments(_ context.Context, _ prometheus.Labels) (ExpiringCommitments, error) {
	commitments := ExpiringCommitments{
		Notifications:  make(map[db.ProjectID][]datamodel.CommitmentInfo),
		NextSubmission: c.MeasureTime(),
	}

	err := sqlext.ForeachRow(c.DB, findExpiringCommitments, nil, func(rows *sql.Rows) error {
		var pid db.ProjectID
		var info datamodel.CommitmentInfo
		err := rows.Scan(
			&pid,
			&info.Resource.ServiceType, &info.Resource.ResourceName, &info.Resource.AvailabilityZone,
			&info.Commitment.ID, &info.Commitment.CreatorName, &info.Commitment.Amount, &info.Commitment.Duration, &info.Commitment.ExpiresAt,
		)
		commitments.Notifications[pid] = append(commitments.Notifications[pid], info)
		return err
	})
	if err != nil {
		return ExpiringCommitments{}, err
	}

	return commitments, nil
}

func (c *Collector) processExpiringCommitmentTask(ctx context.Context, task ExpiringCommitments, _ prometheus.Labels) error {
	var mailInfo datamodel.MailInfo

	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}

	for projectID, commitments := range task.Notifications {
		err := tx.QueryRow("SELECT d.name, p.name FROM domains d JOIN projects p ON d.id = p.domain_id where p.id = $1", projectID).Scan(&mailInfo.DomainName, &mailInfo.ProjectName)
		if err != nil {
			return err
		}
		mailInfo.Commitments = commitments
		mail, err := mailInfo.CreateMailNotification(c.Cluster.MailTemplates.ExpiringCommitments, "Information about expiring commitments", projectID, task.NextSubmission)
		if err != nil {
			return err
		}

		err = tx.Insert(&mail)
		if err != nil {
			return err
		}

		for _, c := range commitments {
			_, err = tx.Exec("UPDATE project_commitments SET notified_for_expiration = true WHERE commitment_id = $1", c.Commitment.ID)
			if err != nil {
				return err
			}
		}
	}

	err = tx.Commit()
	if err != nil {
		return err
	}
	return nil
}
