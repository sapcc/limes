// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/lib/pq"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"

	. "github.com/majewsky/gg/option"
)

var (
	// Commitments are confirmed in a chronological order, wherein `created_at`
	// has a higher priority than `confirm_by` to ensure that commitments created
	// at a later date cannot skip the queue when existing customers are already
	// waiting for commitments.
	//
	// The final `BY pc.id` ordering ensures deterministic behavior in tests.
	getConfirmableCommitmentsQuery = sqlext.SimplifyWhitespace(`
		SELECT pc.project_id, pc.id, pc.amount, pc.notify_on_confirm
		  FROM services cs
		  JOIN resources cr ON cr.service_id = cs.id
		  JOIN az_resources cazr ON cazr.resource_id = cr.id
		  JOIN project_commitments pc ON pc.az_resource_id = cazr.id
		 WHERE cs.type = $1 AND cr.name = $2 AND cazr.az = $3 AND pc.state = 'pending'
		 ORDER BY pc.created_at ASC, pc.confirm_by ASC, pc.id ASC
	`)
)

// CanAcceptCommitmentChangeRequest returns whether the requested moves and creations
// within the liquid.CommitmentChangeRequest can be done from capacity perspective.
func CanAcceptCommitmentChangeRequest(req liquid.CommitmentChangeRequest, serviceType db.ServiceType, cluster *core.Cluster, dbi db.Interface) (bool, error) {
	var distinctResources = make(map[liquid.ResourceName]struct{})
	for _, projectCommitmentChangeset := range req.ByProject {
		for resourceName := range projectCommitmentChangeset.ByResource {
			distinctResources[resourceName] = struct{}{}
		}
	}
	// internally, we only work with projectIDs, so we have to have a conversion ready
	projectByUUID, err := db.BuildIndexOfDBResult(
		dbi,
		func(project db.Project) liquid.ProjectUUID { return project.UUID },
		`SELECT * FROM projects WHERE uuid = ANY($1)`,
		pq.Array(slices.Collect(maps.Keys(req.ByProject))))
	if err != nil {
		return false, fmt.Errorf("while building project index: %w", err)
	}

	for resourceName := range distinctResources {
		additions := map[db.ProjectID]uint64{}
		subtractions := map[db.ProjectID]uint64{}
		additionSum := uint64(0)
		subtractionSum := uint64(0)
		for projectUUID, projectCommitmentChangeset := range req.ByProject {
			project, exists := projectByUUID[projectUUID]
			// defense in depth: technically, the request has been validated before, so this does not happen.
			if !exists {
				return false, fmt.Errorf("project %s not found in database", projectUUID)
			}
			for _, commitment := range projectCommitmentChangeset.ByResource[resourceName].Commitments {
				if commitment.NewStatus == Some(liquid.CommitmentStatusConfirmed) && (commitment.OldStatus != Some(liquid.CommitmentStatusConfirmed)) {
					additions[project.ID] += commitment.Amount
					additionSum += commitment.Amount
				}
				if commitment.OldStatus == Some(liquid.CommitmentStatusConfirmed) && (commitment.NewStatus != Some(liquid.CommitmentStatusConfirmed)) {
					subtractions[project.ID] += commitment.Amount
					subtractionSum += commitment.Amount
				}
			}
		}

		// 0 additions means we can accept, no matter how many subtractions there are.
		if len(additions) == 0 {
			continue
		}
		statsByAZ, err := collectAZAllocationStats(serviceType, resourceName, &req.AZ, cluster, dbi)
		if err != nil {
			return false, err
		}
		stats := statsByAZ[req.AZ]

		behavior := cluster.CommitmentBehaviorForResource(serviceType, resourceName)
		logg.Debug("checking additions in %s/%s/%s: overall amount %d",
			serviceType, resourceName, req.AZ, resourceName, additionSum)
		logg.Debug("checking subtractions in %s/%s/%s: overall amount %d",
			serviceType, resourceName, req.AZ, resourceName, subtractionSum)
		result := stats.CanAcceptCommitmentChanges(additions, subtractions, behavior)
		if !result {
			return false, nil
		}
	}
	return true, nil
}

// ConfirmPendingCommitments goes through all unconfirmed commitments that
// could be confirmed, in chronological creation order, and confirms as many of
// them as possible given the currently available capacity.
func ConfirmPendingCommitments(loc core.AZResourceLocation, cluster *core.Cluster, dbi db.Interface, now time.Time) ([]db.MailNotification, error) {
	behavior := cluster.CommitmentBehaviorForResource(loc.ServiceType, loc.ResourceName)

	// load confirmable commitments (we need to load them into a buffer first, since
	// lib/pq cannot do UPDATE while a SELECT targeting the same rows is still going)
	type confirmableCommitment struct {
		ProjectID       db.ProjectID
		CommitmentID    db.ProjectCommitmentID
		Amount          uint64
		NotifyOnConfirm bool
	}
	var confirmableCommitments []confirmableCommitment
	confirmedCommitmentIDs := make(map[db.ProjectID][]db.ProjectCommitmentID)
	queryArgs := []any{loc.ServiceType, loc.ResourceName, loc.AvailabilityZone}
	err := sqlext.ForeachRow(dbi, getConfirmableCommitmentsQuery, queryArgs, func(rows *sql.Rows) error {
		var c confirmableCommitment
		err := rows.Scan(&c.ProjectID, &c.CommitmentID, &c.Amount, &c.NotifyOnConfirm)
		confirmableCommitments = append(confirmableCommitments, c)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("while enumerating confirmable commitments for %s/%s in %s: %w", loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
	}

	// optimization: do not load allocation stats if we do not have anything to confirm
	if len(confirmableCommitments) == 0 {
		return nil, nil
	}

	statsByAZ, err := collectAZAllocationStats(loc.ServiceType, loc.ResourceName, &loc.AvailabilityZone, cluster, dbi)
	if err != nil {
		return nil, err
	}
	stats := statsByAZ[loc.AvailabilityZone]

	// foreach confirmable commitment...
	for _, c := range confirmableCommitments {
		// ignore commitments that do not fit
		additions := map[db.ProjectID]uint64{c.ProjectID: c.Amount}
		logg.Debug("checking ConfirmPendingCommitments in %s/%s/%s: commitmentID = %d, projectID = %d, amount = %d",
			loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, c.CommitmentID, c.ProjectID, c.Amount)
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
		oldStats := stats.ProjectStats[c.ProjectID]
		stats.ProjectStats[c.ProjectID] = projectAZAllocationStats{
			Committed: oldStats.Committed + c.Amount,
			Usage:     oldStats.Usage,
		}
	}

	// prepare mail notifications (this needs to be done in a separate loop because we collate notifications by project)
	var mails []db.MailNotification
	apiIdentity := cluster.BehaviorForResource(loc.ServiceType, loc.ResourceName).IdentityInV1API
	mailLoc := core.AZResourceLocation{ServiceType: db.ServiceType(apiIdentity.ServiceType), ResourceName: liquid.ResourceName(apiIdentity.Name), AvailabilityZone: loc.AvailabilityZone}
	if mailConfig, ok := cluster.Config.MailNotifications.Unpack(); ok {
		for projectID := range confirmedCommitmentIDs {
			mail, err := prepareConfirmationMail(mailConfig.Templates.ConfirmedCommitments, dbi, mailLoc, projectID, confirmedCommitmentIDs[projectID], now)
			if err != nil {
				return nil, err
			}
			mails = append(mails, mail)
		}
	}

	return mails, nil
}

func prepareConfirmationMail(tpl core.MailTemplate, dbi db.Interface, loc core.AZResourceLocation, projectID db.ProjectID, confirmedCommitmentIDs []db.ProjectCommitmentID, now time.Time) (db.MailNotification, error) {
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
		confirmedAt := c.ConfirmedAt.UnwrapOr(time.Unix(0, 0)) // the UnwrapOr() is defense in depth, it should never be relevant because we only notify for confirmed commitments here
		n.Commitments = append(n.Commitments, core.CommitmentNotification{
			Commitment: c,
			DateString: confirmedAt.Format(time.DateOnly),
			Resource:   loc,
		})
	}

	return tpl.Render(n, projectID, now)
}
