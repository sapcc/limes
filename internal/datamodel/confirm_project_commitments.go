// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/lib/pq"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	. "github.com/majewsky/gg/option"

	"github.com/sapcc/limes/internal/audit"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// commitmentTransferLeftover describes how much of a commitment is left over after part (or all) of it was consumed
// by a transfer. It is used in the commitment transfer algorithm.
type commitmentTransferLeftover struct {
	Amount uint64
	ID     db.ProjectCommitmentID // currently only being used internally, not published in the mail (use UUID for that!)
}

// Commitments are confirmed in a chronological order, wherein `created_at`
// has a higher priority than `confirm_by` to ensure that commitments created
// at a later date cannot skip the queue when existing customers are already
// waiting for commitments.
//
// The final `BY pc.id` ordering ensures deterministic behavior in tests.
var getConfirmableCommitmentsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		SELECT pc.*
		  FROM services s
		  JOIN resources r ON r.service_id = s.id
		  JOIN az_resources azr ON azr.resource_id = r.id
		  JOIN project_commitments pc ON pc.az_resource_id = azr.id
		 WHERE s.type = $1 AND r.name = $2 AND azr.az = $3 AND pc.status = {{liquid.CommitmentStatusPending}}
		 ORDER BY pc.created_at ASC, pc.confirm_by ASC, pc.id ASC
	`))

const ConfirmAction cadf.Action = "confirm"
const ConsumeAction cadf.Action = "consume"

// CanAcceptCommitmentChangeRequest returns whether the requested moves and creations
// within the liquid.CommitmentChangeRequest can be done from capacity perspective.
func CanAcceptCommitmentChangeRequest(req liquid.CommitmentChangeRequest, loc core.AZResourceLocation, cluster *core.Cluster, dbi db.Interface) (bool, error) {
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
		statsByAZ, err := collectAZAllocationStats(loc.ServiceType, resourceName, Some(req.AZ), cluster, dbi)
		if err != nil {
			return false, err
		}
		stats := statsByAZ[req.AZ]

		behavior := cluster.CommitmentBehaviorForResource(loc.ServiceType, resourceName)
		logg.Debug("checking additions in %s: overall amount %d",
			loc.ShortScopeString(), resourceName, additionSum)
		logg.Debug("checking subtractions in %s: overall amount %d",
			loc.ShortScopeString(), resourceName, subtractionSum)
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
func ConfirmPendingCommitments(ctx context.Context, loc core.AZResourceLocation, cluster *core.Cluster, dbi db.Interface, now time.Time, generateProjectCommitmentUUID func() liquid.CommitmentUUID, generateTransferToken func() string, auditContext audit.Context) (auditEvents []audittools.Event, err error) {
	// load confirmable commitments
	var confirmableCommitments []db.ProjectCommitment
	confirmedCommitmentsByProjectID := make(map[db.ProjectID][]*db.ProjectCommitment)
	queryArgs := []any{loc.ServiceType, loc.ResourceName, loc.AvailabilityZone}
	_, err = dbi.Select(&confirmableCommitments, getConfirmableCommitmentsQuery, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("while enumerating confirmable commitments for %s: %w", loc.ScopeString(), err)
	}

	// optimization: do not do more loading, if we do not have anything to confirm
	if len(confirmableCommitments) == 0 {
		return nil, nil
	}

	// load service info (used to generate liquid.ProjectMetadata)
	maybeServiceInfo, err := cluster.InfoForService(loc.ServiceType)
	if err != nil {
		return nil, err
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		return nil, fmt.Errorf("serviceInfo not found when trying to confirm commitments for %s", loc.ServiceType)
	}

	// load affected projects and domains
	affectedProjectIDs := make(map[db.ProjectID]struct{})
	for _, c := range confirmableCommitments {
		affectedProjectIDs[c.ProjectID] = struct{}{}
	}
	affectedProjectsByID, err := db.BuildIndexOfDBResult(dbi, func(p db.Project) db.ProjectID { return p.ID }, `SELECT * FROM projects WHERE id = ANY($1)`, pq.Array(slices.Collect(maps.Keys(affectedProjectIDs))))
	if err != nil {
		return nil, fmt.Errorf("while loading affected projects for %s: %w", loc.ScopeString(), err)
	}
	affectedDomainsByID, err := db.BuildIndexOfDBResult(dbi, func(d db.Domain) db.DomainID { return d.ID }, `SELECT * FROM domains WHERE id IN (SELECT domain_id FROM projects WHERE id = ANY($1))`, pq.Array(slices.Collect(maps.Keys(affectedProjectIDs))))
	if err != nil {
		return nil, fmt.Errorf("while loading affected domains for %s: %w", loc.ScopeString(), err)
	}

	// load mail templates
	transferTemplate := None[core.MailTemplate]()
	confirmationTemplate := None[core.MailTemplate]()
	if mailConfig, exists := cluster.Config.MailNotifications.Unpack(); exists {
		transferTemplate = Some(mailConfig.Templates.TransferredCommitments)
		confirmationTemplate = Some(mailConfig.Templates.ConfirmedCommitments)
	}

	// initiate cache of transferable commitments
	transferableCommitmentCache, err := NewTransferableCommitmentCache(dbi, cluster, serviceInfo, loc, now, generateProjectCommitmentUUID, generateTransferToken, transferTemplate)
	if err != nil {
		return nil, err
	}

	// foreach confirmable commitment in the order to be confirmed
	for _, cc := range confirmableCommitments {
		// If a commitment was transferred in this iteration already, we do not need to confirm it.
		// If partially transferred, the leftover is added to the transferable commitments instead of the superseded one.
		if transferableCommitmentCache.CommitmentWasTransferred(cc.ID, cc.ProjectID) {
			continue
		}

		// First, we check whether we can consume transferable commitments and have the necessary capacity.
		project := affectedProjectsByID[cc.ProjectID]
		domain := affectedDomainsByID[project.DomainID]
		result, err := transferableCommitmentCache.CanConfirmWithTransfers(ctx, cc, project, domain, false, false, auditContext, ConfirmAction)
		if err != nil {
			return nil, err
		}

		// When we cannot confirm the commitment, we check with the next one. This can lead to
		// smaller but later created commitments to be confirmed earlier, but that is acceptable.
		if result.RejectionReason != "" {
			continue
		}

		// capacity is sufficient --> confirm the commitment
		cc.ConfirmedAt = Some(now)
		cc.Status = liquid.CommitmentStatusConfirmed
		_, err = dbi.Update(&cc)
		if err != nil {
			return nil, fmt.Errorf("while confirming commitment ID=%d for %s: %w", cc.ID, loc.ScopeString(), err)
		}
		transferableCommitmentCache.ConfirmTransferableCommitmentIfExists(cc.ID, now)
		confirmedCommitmentsByProjectID[cc.ProjectID] = append(confirmedCommitmentsByProjectID[cc.ProjectID], &cc)
	}

	// generate mail notifications for commitment transfers
	apiIdentity := cluster.BehaviorForResource(loc.ServiceType, loc.ResourceName).IdentityInV1API
	err = transferableCommitmentCache.GenerateTransferMails(apiIdentity)
	if err != nil {
		return nil, err
	}

	// retrieve audit events for commitment transfers
	auditEvents = append(auditEvents, transferableCommitmentCache.RetrieveAuditEvents()...)

	// generate mail notifications for commitment confirmations
	for _, projectID := range slices.Sorted(maps.Keys(confirmedCommitmentsByProjectID)) {
		confirmedCommitments := confirmedCommitmentsByProjectID[projectID]
		// For commitments which get confirmed first and then transferred, we remove the mail for confirmation
		// to avoid duplicate notification mails.
		notificationsForProject := transferableCommitmentCache.getTransferredCommitmentsForProject(projectID)
		for cID := range notificationsForProject {
			confirmedCommitments = slices.DeleteFunc(confirmedCommitments, func(c *db.ProjectCommitment) bool { return c.ID == cID })
		}

		affectedProject := affectedProjectsByID[projectID]
		affectedDomain := affectedDomainsByID[affectedProject.DomainID]
		err = generateConfirmationMails(confirmationTemplate, dbi, loc, apiIdentity, affectedProject, affectedDomain, confirmedCommitments, now)
		if err != nil {
			return nil, err
		}
	}

	return auditEvents, nil
}

func generateConfirmationMails(mailTemplate Option[core.MailTemplate], dbi db.Interface, loc core.AZResourceLocation, apiIdentity core.ResourceRef, project db.Project, domain db.Domain, confirmedCommitments []*db.ProjectCommitment, now time.Time) error {
	// The system can be configured to not send mails (e.g. for test systems).
	tpl, tplExists := mailTemplate.Unpack()
	if !tplExists {
		return nil
	}

	n := core.CommitmentGroupNotification{
		DomainName:  domain.Name,
		ProjectName: project.Name,
		Commitments: make([]core.CommitmentNotification, 0, len(confirmedCommitments)),
	}

	for _, c := range confirmedCommitments {
		// The user can choose to not be notified on confirmation.
		if !c.NotifyOnConfirm {
			continue
		}

		// also defense in depth: we only generate mails for confirmed commitments
		confirmedAt := c.ConfirmedAt.UnwrapOr(time.Unix(0, 0)) // the UnwrapOr() is defense in depth, it should never be relevant because we only notify for confirmed commitments here
		n.Commitments = append(n.Commitments, core.CommitmentNotification{
			Commitment: *c,
			DateString: confirmedAt.Format(time.DateOnly),
			// TODO: we actually don't want to have api-named props in AZResourceLocation. Replace the template and the code simultaneously.
			Resource: core.AZResourceLocation{
				ServiceType:      db.ServiceType(apiIdentity.ServiceType),
				ResourceName:     liquid.ResourceName(apiIdentity.Name),
				AvailabilityZone: loc.AvailabilityZone,
			},
		})
	}
	if len(n.Commitments) != 0 {
		// push mail notifications
		mail, err := tpl.Render(n, project.ID, now)
		if err != nil {
			return err
		}
		err = dbi.Insert(&mail)
		if err != nil {
			return err
		}
	}
	return nil
}
