/******************************************************************************
*
*  Copyright 2023 SAP SE
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

package datamodel

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

var (
	// Commitments are confirmed in a chronological order, wherein `created_at`
	// has a higher priority than `confirm_by` to ensure that commitments created
	// at a later date cannot skip the queue when existing customers are already
	// waiting for commitments.
	//
	// The final `BY pc.id` ordering ensures deterministic behavior in tests.
	getConfirmableCommitmentsQuery = sqlext.SimplifyWhitespace(`
		SELECT ps.project_id, pr.id, pc.id, pc.amount, pc.notify_on_confirm
		  FROM project_services ps
		  JOIN project_resources pr ON pr.service_id = ps.id
		  JOIN project_az_resources par ON par.resource_id = pr.id
		  JOIN project_commitments pc ON pc.az_resource_id = par.id
		 WHERE ps.type = $1 AND pr.name = $2 AND par.az = $3 AND pc.state = 'pending'
		 ORDER BY pc.created_at ASC, pc.confirm_by ASC, pc.id ASC
	`)
)

// CanConfirmNewCommitment returns whether the given commitment request can be
// confirmed immediately upon creation in the given project.
func CanConfirmNewCommitment(loc core.AZResourceLocation, resourceID db.ProjectResourceID, amount uint64, cluster *core.Cluster, dbi db.Interface) (bool, error) {
	statsByAZ, err := collectAZAllocationStats(loc.ServiceType, loc.ResourceName, &loc.AvailabilityZone, cluster, dbi)
	if err != nil {
		return false, err
	}
	stats := statsByAZ[loc.AvailabilityZone]

	additions := map[db.ProjectResourceID]uint64{resourceID: amount}
	behavior := cluster.BehaviorForResource(loc.ServiceType, loc.ResourceName)
	logg.Debug("checking CanConfirmNewCommitment in %s/%s/%s: resourceID = %d, amount = %d",
		loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, resourceID, amount)
	return stats.CanAcceptCommitmentChanges(additions, nil, behavior), nil
}

// CanMoveExistingCommitment returns whether a commitment of the given amount
// at the given AZ resource location can be moved from one project to another.
// The projects are identified by their resource IDs.
func CanMoveExistingCommitment(amount uint64, loc core.AZResourceLocation, sourceResourceID, targetResourceID db.ProjectResourceID, cluster *core.Cluster, dbi db.Interface) (bool, error) {
	statsByAZ, err := collectAZAllocationStats(loc.ServiceType, loc.ResourceName, &loc.AvailabilityZone, cluster, dbi)
	if err != nil {
		return false, err
	}
	stats := statsByAZ[loc.AvailabilityZone]

	additions := map[db.ProjectResourceID]uint64{targetResourceID: amount}
	subtractions := map[db.ProjectResourceID]uint64{sourceResourceID: amount}
	behavior := cluster.BehaviorForResource(loc.ServiceType, loc.ResourceName)
	logg.Debug("checking CanMoveExistingCommitment in %s/%s/%s: resourceID = %d -> %d, amount = %d",
		loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, sourceResourceID, targetResourceID, amount)
	return stats.CanAcceptCommitmentChanges(additions, subtractions, behavior), nil
}

// ConfirmPendingCommitments goes through all unconfirmed commitments that
// could be confirmed, in chronological creation order, and confirms as many of
// them as possible given the currently available capacity.
func ConfirmPendingCommitments(loc core.AZResourceLocation, cluster *core.Cluster, dbi db.Interface, now time.Time) ([]db.MailNotification, error) {
	behavior := cluster.BehaviorForResource(loc.ServiceType, loc.ResourceName)

	statsByAZ, err := collectAZAllocationStats(loc.ServiceType, loc.ResourceName, &loc.AvailabilityZone, cluster, dbi)
	if err != nil {
		return nil, err
	}
	stats := statsByAZ[loc.AvailabilityZone]

	// load confirmable commitments (we need to load them into a buffer first, since
	// lib/pq cannot do UPDATE while a SELECT targeting the same rows is still going)
	type confirmableCommitment struct {
		ProjectID         db.ProjectID
		ProjectResourceID db.ProjectResourceID
		CommitmentID      db.ProjectCommitmentID
		Amount            uint64
		NotifyOnConfirm   bool
	}
	var confirmableCommitments []confirmableCommitment
	confirmedCommitmentIDs := make(map[db.ProjectID][]db.ProjectCommitmentID)
	queryArgs := []any{loc.ServiceType, loc.ResourceName, loc.AvailabilityZone}
	err = sqlext.ForeachRow(dbi, getConfirmableCommitmentsQuery, queryArgs, func(rows *sql.Rows) error {
		var c confirmableCommitment
		err := rows.Scan(&c.ProjectID, &c.ProjectResourceID, &c.CommitmentID, &c.Amount, &c.NotifyOnConfirm)
		confirmableCommitments = append(confirmableCommitments, c)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("while enumerating confirmable commitments for %s/%s in %s: %w", loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
	}

	// foreach confirmable commitment...
	for _, c := range confirmableCommitments {
		// ignore commitments that do not fit
		additions := map[db.ProjectResourceID]uint64{c.ProjectResourceID: c.Amount}
		logg.Debug("checking ConfirmPendingCommitments in %s/%s/%s: resourceID = %d, amount = %d",
			loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, c.ProjectResourceID, c.Amount)
		if !stats.CanAcceptCommitmentChanges(additions, nil, behavior) {
			continue
		}

		// confirm the commitment
		_, err = dbi.Exec(`UPDATE project_commitments SET confirmed_at = $1, state = $2 WHERE id = $3`,
			now, db.CommitmentStateActive, c.CommitmentID)
		if err != nil {
			return nil, fmt.Errorf("while confirming commitment ID=%d for %s/%s in %s: %w", c.CommitmentID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
		}

		if c.NotifyOnConfirm {
			confirmedCommitmentIDs[c.ProjectID] = append(confirmedCommitmentIDs[c.ProjectID], c.CommitmentID)
		}

		// block its allocation from being committed again in this loop
		oldStats := stats.ProjectStats[c.ProjectResourceID]
		stats.ProjectStats[c.ProjectResourceID] = projectAZAllocationStats{
			Committed: oldStats.Committed + c.Amount,
			Usage:     oldStats.Usage,
		}
	}

	// prepare mail notifications (this needs to be done in a separate loop because we collate notifications by project)
	var mails []db.MailNotification
	for projectID := range confirmedCommitmentIDs {
		mail, err := prepareConfirmationMail(cluster, dbi, loc, projectID, confirmedCommitmentIDs[projectID], now)
		if err != nil {
			return nil, err
		}
		mails = append(mails, mail)
	}

	return mails, nil
}

func prepareConfirmationMail(cluster *core.Cluster, dbi db.Interface, loc core.AZResourceLocation, projectID db.ProjectID, confirmedCommitmentIDs []db.ProjectCommitmentID, now time.Time) (db.MailNotification, error) {
	var n core.CommitmentGroupNotification
	err := dbi.QueryRow("SELECT d.name, p.name FROM domains d JOIN projects p ON d.id = p.domain_id where p.id = $1", projectID).Scan(&n.DomainName, &n.ProjectName)
	if err != nil {
		return db.MailNotification{}, err
	}

	var commitments []db.ProjectCommitment
	_, err = dbi.Select(&commitments, `SELECT * FROM project_commitments WHERE id = ANY($1)`, pq.Array(confirmedCommitmentIDs))
	if err != nil {
		return db.MailNotification{}, err
	}
	for _, c := range commitments {
		n.Commitments = append(n.Commitments, core.CommitmentNotification{
			Commitment: c,
			DateString: c.ConfirmedAt.Format(time.DateOnly),
			Resource:   loc,
		})
	}

	return cluster.Config.MailNotifications.Templates.ConfirmedCommitments.Render(n, projectID, now)
}
