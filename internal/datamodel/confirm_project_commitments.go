// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/lib/pq"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	. "github.com/majewsky/gg/option"

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
	getConfirmableCommitmentsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		SELECT pc.*
		  FROM services cs
		  JOIN resources cr ON cr.service_id = cs.id
		  JOIN az_resources azr ON azr.resource_id = cr.id
		  JOIN project_commitments pc ON pc.az_resource_id = azr.id
		 WHERE cs.type = $1 AND cr.name = $2 AND azr.az = $3 AND pc.status = {{liquid.CommitmentStatusPending}}
		 ORDER BY pc.created_at ASC, pc.confirm_by ASC, pc.id ASC
	`))

	// Before Commitments get confirmed, commitments with transfer_status = public
	// are released. Commitments with transfer_status=public are matched with
	// commitments to be confirmed in the order they were posted in for transfer.
	// The status of a transferable commitment does not matter for this operation.
	//
	// The final `BY pc.id` ordering ensures deterministic behavior in tests, in reality
	// the probability of multiple commitments set to transfer at the exact same time is
	// very small due to the atomicity of the API operation.
	getTransferCommitmentsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		SELECT pc.*
		FROM services cs
		JOIN resources cr ON cr.service_id = cs.id
		JOIN az_resources azr ON azr.resource_id = cr.id
		JOIN project_commitments pc ON pc.az_resource_id = azr.id
		WHERE cs.type = $1 AND cr.name = $2 AND azr.az = $3 AND pc.transfer_status = {{limesresources.CommitmentTransferStatusPublic}}
		ORDER BY pc.transfer_started_at ASC, pc.created_at ASC, pc.id ASC
	`))
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
// them as possible given the currently available capacity. Simultaneously, it
// releases transferable commitments that can be used to satisfy the pending ones.
func ConfirmPendingCommitments(loc core.AZResourceLocation, cluster *core.Cluster, dbi db.Interface, now time.Time, generateProjectCommitmentUUID func() liquid.CommitmentUUID, generateTransferToken func() string) ([]db.MailNotification, error) {
	behavior := cluster.CommitmentBehaviorForResource(loc.ServiceType, loc.ResourceName)

	// load confirmable commitments
	var confirmableCommitments []db.ProjectCommitment
	confirmedCommitmentIDs := make(map[db.ProjectID][]db.ProjectCommitmentID)
	transferredCommitmentIDs := make(map[db.ProjectID]map[db.ProjectCommitmentID]core.CommitmentTransferLeftover)
	queryArgs := []any{loc.ServiceType, loc.ResourceName, loc.AvailabilityZone}
	_, err := dbi.Select(&confirmableCommitments, getConfirmableCommitmentsQuery, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("while enumerating confirmable commitments for %s/%s in %s: %w", loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
	}

	// optimization: do not load allocation stats if we do not have anything to confirm
	if len(confirmableCommitments) == 0 {
		return nil, nil
	}

	// load transferable commitments
	var transferableCommitments []db.ProjectCommitment
	_, err = dbi.Select(&transferableCommitments, getTransferCommitmentsQuery, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("while enumerating transferable commitments for %s/%s in %s: %w", loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
	}
	transferableCommitmentsByID := make(map[db.ProjectCommitmentID]*db.ProjectCommitment, len(transferableCommitments))
	for i := range transferableCommitments {
		transferableCommitmentsByID[transferableCommitments[i].ID] = &transferableCommitments[i]
	}

	statsByAZ, err := collectAZAllocationStats(loc.ServiceType, loc.ResourceName, &loc.AvailabilityZone, cluster, dbi)
	if err != nil {
		return nil, err
	}
	stats := statsByAZ[loc.AvailabilityZone]

	// foreach confirmable commitment in the order to be confirmed
	for _, c := range confirmableCommitments {
		// ignore commitments that do not fit
		additions := map[db.ProjectID]uint64{c.ProjectID: c.Amount}
		logg.Debug("checking ConfirmPendingCommitments in %s/%s/%s: commitmentID = %d, projectID = %d, amount = %d",
			loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, c.ID, c.ProjectID, c.Amount)
		if !stats.CanAcceptCommitmentChanges(additions, nil, behavior) {
			continue
		}

		// if a commitment was transferred in this iteration already, we do not need to confirm it
		// if partially transferred, the leftover commitment is added to the transferable commitments and considered separately
		transfersPerProject, exists := transferredCommitmentIDs[c.ProjectID]
		if _, exists2 := transfersPerProject[c.ID]; exists && exists2 {
			continue
		}

		// Now we try to consume transferable commitments.
		// When confirming a commitment, we consume max. the amount that we confirm.
		// A commitment is only consumed if it's expires_at <= expires_at of the commitment we confirm.
		// The status of a commitment to be consumed does not matter.
		// When a commitment is both pending and transferable, the handling depends on the order:
		// When confirmed first, it might be taken over later anyway.
		// When transferred first, it does not get confirmed later.
		// A partial transfer will lead to a separate mail which contains the new ID, so that the customer
		// can track the whole processing of the transferred commitment.

		overallTransferredAmount := uint64(0)
		for idx, t := range transferableCommitments {
			if overallTransferredAmount == c.Amount {
				break
			}
			transfersPerProject, exists := transferredCommitmentIDs[t.ProjectID]
			if _, exists2 := transfersPerProject[t.ID]; t.ProjectID == c.ProjectID || t.ExpiresAt.After(c.ExpiresAt) || (exists && exists2) {
				continue
			}

			// checks passed, a part of this commitment will be consumed, so we will supersede it in any case
			if !exists {
				transferredCommitmentIDs[t.ProjectID] = make(map[db.ProjectCommitmentID]core.CommitmentTransferLeftover)
			}

			amountToConsume := c.Amount - overallTransferredAmount
			if t.Amount > amountToConsume {
				overallTransferredAmount += amountToConsume
				// the leftover amount to be transferred is not enough to consume the whole commitment
				// we will place a new commitment for the leftover amount
				leftoverCommitment, err := BuildSplitCommitment(t, t.Amount-amountToConsume, now, generateProjectCommitmentUUID)
				if err != nil {
					return nil, err
				}
				leftoverCommitment.TransferStatus = limesresources.CommitmentTransferStatusPublic
				leftoverCommitment.TransferToken = Some(generateTransferToken())
				leftoverCommitment.TransferStartedAt = t.TransferStartedAt
				err = dbi.Insert(&leftoverCommitment)
				if err != nil {
					return nil, err
				}

				transferableCommitments[idx] = leftoverCommitment
				transferableCommitmentsByID[leftoverCommitment.ID] = &leftoverCommitment
				transferredCommitmentIDs[t.ProjectID][t.ID] = core.CommitmentTransferLeftover{
					Amount: t.Amount - amountToConsume,
					ID:     leftoverCommitment.ID,
				}
			} else {
				// the transferable commitment is fully consumed
				overallTransferredAmount += t.Amount
				transferredCommitmentIDs[t.ProjectID][t.ID] = core.CommitmentTransferLeftover{}
			}

			// supersede consumed commitment
			t.TransferStartedAt = None[time.Time]()
			t.TransferStatus = limesresources.CommitmentTransferStatusNone
			t.TransferToken = None[string]()
			t.Status = liquid.CommitmentStatusSuperseded
			t.SupersededAt = Some(now)
			supersedeContext := db.CommitmentWorkflowContext{
				Reason:                 db.CommitmentReasonTransfer,
				RelatedCommitmentIDs:   []db.ProjectCommitmentID{c.ID},
				RelatedCommitmentUUIDs: []liquid.CommitmentUUID{c.UUID},
			}
			buf, err := json.Marshal(supersedeContext)
			if err != nil {
				return nil, err
			}
			t.SupersedeContextJSON = Some(json.RawMessage(buf))
			_, err = dbi.Update(&t)
			if err != nil {
				return nil, err
			}
		}

		// confirm the commitment
		_, err = dbi.Exec(`UPDATE project_commitments SET confirmed_at = $1, status = $2 WHERE id = $3`,
			now, liquid.CommitmentStatusConfirmed, c.ID)
		if err != nil {
			return nil, fmt.Errorf("while confirming commitment ID=%d for %s/%s in %s: %w", c.ID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
		}
		if value, exists := transferableCommitmentsByID[c.ID]; exists {
			// in case the commitment get's taken over later, it should keep the status it gets now
			value.ConfirmedAt = Some(now)
			value.Status = liquid.CommitmentStatusConfirmed
		}

		if c.NotifyOnConfirm {
			confirmedCommitmentIDs[c.ProjectID] = append(confirmedCommitmentIDs[c.ProjectID], c.ID)
		}

		// block its allocation from being committed again in this loop
		oldStats := stats.ProjectStats[c.ProjectID]
		stats.ProjectStats[c.ProjectID] = projectAZAllocationStats{
			Committed: oldStats.Committed + c.Amount,
			Usage:     oldStats.Usage,
		}
	}

	// remove duplicates of multiple consecutive transferred commitments
	transferredCommitmentIDsWithNotification := make(map[db.ProjectID]map[db.ProjectCommitmentID]core.CommitmentTransferLeftover)
	for projectID, transfersPerProject := range transferredCommitmentIDs {
		if _, exists := transferredCommitmentIDsWithNotification[projectID]; !exists {
			transferredCommitmentIDsWithNotification[projectID] = make(map[db.ProjectCommitmentID]core.CommitmentTransferLeftover)
		}
		// we go through the transfers by ID descending, because that enables the linking operation in O(n)
		for _, cID := range slices.Backward(slices.Sorted(maps.Keys(transfersPerProject))) {
			// for commitments which get confirmed first and then transferred, we remove the mail for confirmation
			committedPerProject := confirmedCommitmentIDs[projectID]
			idx := slices.Index(committedPerProject, cID)
			if idx != -1 {
				confirmedCommitmentIDs[projectID] = append(confirmedCommitmentIDs[projectID][:idx], confirmedCommitmentIDs[projectID][idx+1:]...)
			}

			// for transfers which have a leftover, we link the transferCommitment to the last leftover via a new data structure
			transferredLeftover := transfersPerProject[cID]
			if followingLeftover, exists := transferredCommitmentIDsWithNotification[projectID][transferredLeftover.ID]; exists {
				transferredCommitmentIDsWithNotification[projectID][cID] = core.CommitmentTransferLeftover{
					Amount: followingLeftover.Amount,
					ID:     followingLeftover.ID,
				}
				delete(transferredCommitmentIDsWithNotification[projectID], transferredLeftover.ID)
			} else {
				transferredCommitmentIDsWithNotification[projectID][cID] = transferredLeftover
			}
		}
	}

	// prepare mail notifications (this needs to be done in a separate loop because we collate notifications by project)
	var mails []db.MailNotification
	apiIdentity := cluster.BehaviorForResource(loc.ServiceType, loc.ResourceName).IdentityInV1API
	mailLoc := core.AZResourceLocation{ServiceType: db.ServiceType(apiIdentity.ServiceType), ResourceName: liquid.ResourceName(apiIdentity.Name), AvailabilityZone: loc.AvailabilityZone}
	if mailConfig, ok := cluster.Config.MailNotifications.Unpack(); ok {
		for _, projectID := range slices.Compact(slices.Sorted(slices.Values(append(slices.Collect(maps.Keys(confirmedCommitmentIDs)), slices.Collect(maps.Keys(transferredCommitmentIDsWithNotification))...)))) {
			newMails, err := prepareMailsForProject(mailConfig.Templates, dbi, mailLoc, projectID, confirmedCommitmentIDs[projectID], transferredCommitmentIDsWithNotification[projectID], now)
			if err != nil {
				return nil, err
			}
			mails = append(mails, newMails...)
		}
	}

	return mails, nil
}

func prepareMailsForProject(tplConfig core.MailTemplateConfiguration, dbi db.Interface, loc core.AZResourceLocation, projectID db.ProjectID,
	confirmedCommitmentIDs []db.ProjectCommitmentID, transferredCommitmentIDs map[db.ProjectCommitmentID]core.CommitmentTransferLeftover, now time.Time) (result []db.MailNotification, err error) {

	var n core.CommitmentGroupNotification
	err = dbi.QueryRow("SELECT d.name, p.name FROM domains d JOIN projects p ON d.id = p.domain_id where p.id = $1", projectID).Scan(&n.DomainName, &n.ProjectName)
	if err != nil {
		return result, err
	}

	commitmentsByID, err := db.BuildIndexOfDBResult(dbi, func(c db.ProjectCommitment) db.ProjectCommitmentID { return c.ID }, `SELECT * FROM project_commitments WHERE id = ANY($1)`, pq.Array(append(confirmedCommitmentIDs, slices.Collect(maps.Keys(transferredCommitmentIDs))...)))
	if err != nil {
		return result, err
	}
	for _, cID := range confirmedCommitmentIDs {
		c, exists := commitmentsByID[cID]
		if !exists {
			return result, fmt.Errorf("tried to generate mail notification for non-existent commitment ID %d", cID)
		}
		confirmedAt := c.ConfirmedAt.UnwrapOr(time.Unix(0, 0)) // the UnwrapOr() is defense in depth, it should never be relevant because we only notify for confirmed commitments here
		n.Commitments = append(n.Commitments, core.CommitmentNotification{
			Commitment: c,
			DateString: confirmedAt.Format(time.DateOnly),
			Resource:   loc,
		})
	}
	if len(n.Commitments) != 0 {
		newNotification, err := tplConfig.ConfirmedCommitments.Render(n, projectID, now)
		if err != nil {
			return result, err
		}
		result = append(result, newNotification)
	}

	n.Commitments = make([]core.CommitmentNotification, 0, len(transferredCommitmentIDs))
	for cID, leftover := range transferredCommitmentIDs {
		c, exists := commitmentsByID[cID]
		if !exists {
			return result, fmt.Errorf("tried to generate mail notification for non-existent commitment ID %d", cID)
		}
		confirmedAt := c.ConfirmedAt.UnwrapOr(time.Unix(0, 0)) // the UnwrapOr() is defense in depth, it should never be relevant because we only notify for confirmed commitments here
		n.Commitments = append(n.Commitments, core.CommitmentNotification{
			Commitment: c,
			DateString: confirmedAt.Format(time.DateOnly),
			Resource:   loc,
			Leftover:   leftover,
		})
	}
	if len(n.Commitments) == 0 {
		return result, nil
	}
	newNotification, err := tplConfig.TransferredCommitments.Render(n, projectID, now)
	if err != nil {
		return result, err
	}
	result = append(result, newNotification)
	return result, nil
}
