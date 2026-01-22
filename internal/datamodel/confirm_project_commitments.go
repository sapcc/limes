// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"context"
	"fmt"
	"maps"
	"net/http"
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
	// load service info (used to generate liquid.ProjectMetadata)
	maybeServiceInfo, err := cluster.InfoForService(loc.ServiceType)
	if err != nil {
		return nil, err
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		return nil, fmt.Errorf("serviceInfo not found when trying to confirm commitments for %s", loc.ServiceType)
	}

	// load confirmable commitments
	var confirmableCommitments []db.ProjectCommitment
	confirmedCommitmentIDs := make(map[db.ProjectID][]db.ProjectCommitmentID)
	queryArgs := []any{loc.ServiceType, loc.ResourceName, loc.AvailabilityZone}
	_, err = dbi.Select(&confirmableCommitments, getConfirmableCommitmentsQuery, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("while enumerating confirmable commitments for %s: %w", loc.ScopeString(), err)
	}

	// optimization: do not load allocation stats if we do not have anything to confirm
	if len(confirmableCommitments) == 0 {
		return nil, nil
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
	transferableCommitmentCache, err := NewTransferableCommitmentCache(dbi, serviceInfo, loc, now, generateProjectCommitmentUUID, generateTransferToken, transferTemplate)
	if err != nil {
		return nil, err
	}

	// load allocation stats
	statsByAZ, err := collectAZAllocationStats(loc.ServiceType, loc.ResourceName, Some(loc.AvailabilityZone), cluster, dbi)
	if err != nil {
		return nil, err
	}
	stats := statsByAZ[loc.AvailabilityZone]

	// initialize map to hold CCRs for audit events
	ccrs := make(map[liquid.CommitmentUUID]liquid.CommitmentChangeRequest, 0)

	// foreach confirmable commitment in the order to be confirmed
	for _, cc := range confirmableCommitments {
		// ignore commitments that do not fit
		logg.Debug("checking ConfirmPendingCommitments in %s: commitmentID = %d, projectID = %d, amount = %d",
			loc.ShortScopeString(), cc.ID, cc.ProjectID, cc.Amount)
		project := affectedProjectsByID[cc.ProjectID]
		domain := affectedDomainsByID[project.DomainID]

		var capacityAccepted bool
		capacityAccepted, ccr, err := delegateChangeCommitmentsWithShortcut(ctx, cluster, dbi, loc, serviceInfo, project, domain, cc, stats)
		if err != nil {
			return nil, fmt.Errorf("while checking acceptance of commitment ID=%d for %s: %w", cc.ID, loc.ScopeString(), err)
		}
		if !capacityAccepted {
			continue
		}
		ccrs[cc.UUID] = ccr
		// if a commitment was transferred in this iteration already, we do not need to confirm it
		// if partially transferred, the leftover commitment is added to the transferable commitments and considered separately
		if transferableCommitmentCache.CommitmentWasTransferred(cc.ID, cc.ProjectID) {
			continue
		}

		err = transferableCommitmentCache.CheckAndConsume(cc, stats.ProjectStats[cc.ProjectID].Committed)
		if err != nil {
			return nil, err
		}

		// confirm the commitment
		_, err = dbi.Exec(`UPDATE project_commitments SET confirmed_at = $1, status = $2 WHERE id = $3`,
			now, liquid.CommitmentStatusConfirmed, cc.ID)
		if err != nil {
			return nil, fmt.Errorf("while confirming commitment ID=%d for %s: %w", cc.ID, loc.ScopeString(), err)
		}
		transferableCommitmentCache.ConfirmTransferableCommitmentIfExists(cc.ID, now)
		confirmedCommitmentIDs[cc.ProjectID] = append(confirmedCommitmentIDs[cc.ProjectID], cc.ID)

		// block its allocation from being committed again in this loop
		oldStats := stats.ProjectStats[cc.ProjectID]
		stats.ProjectStats[cc.ProjectID] = projectAZAllocationStats{
			Committed: oldStats.Committed + cc.Amount,
			Usage:     oldStats.Usage,
		}
	}

	// gather some prerequisites for the mail notifications
	apiIdentity := cluster.BehaviorForResource(loc.ServiceType, loc.ResourceName).IdentityInV1API

	// generate audit events and mail notifications for commitment transfers
	ae, err := transferableCommitmentCache.GenerateAuditEventsAndMails(apiIdentity, auditContext)
	if err != nil {
		return nil, err
	}
	auditEvents = append(auditEvents, ae...)

	for _, projectID := range slices.Sorted(maps.Keys(confirmedCommitmentIDs)) {
		confirmations := confirmedCommitmentIDs[projectID]
		// for commitments which get confirmed first and then transferred, we remove the mail for confirmation
		// to avoid duplicate notification mails
		notificationsForProject := transferableCommitmentCache.getTransferredCommitmentsForProject(projectID)
		for cID := range notificationsForProject {
			confirmations = slices.DeleteFunc(confirmations, func(id db.ProjectCommitmentID) bool { return id == cID })
		}

		// generate audit events and mail notifications for commitment confirmations
		ae, err = generateAuditEventsAndMails(confirmationTemplate, dbi, loc, apiIdentity, projectID, auditContext, confirmations, ccrs, now)
		if err != nil {
			return nil, err
		}
		auditEvents = append(auditEvents, ae...)
	}

	return auditEvents, nil
}

func generateAuditEventsAndMails(mailTemplate Option[core.MailTemplate], dbi db.Interface, loc core.AZResourceLocation, apiIdentity core.ResourceRef, projectID db.ProjectID, auditContext audit.Context,
	confirmedCommitmentIDs []db.ProjectCommitmentID, ccrs map[liquid.CommitmentUUID]liquid.CommitmentChangeRequest, now time.Time) (auditEvents []audittools.Event, err error) {

	var (
		n           core.CommitmentGroupNotification
		domainUUID  string
		projectUUID liquid.ProjectUUID
	)
	err = dbi.QueryRow("SELECT d.uuid, d.name, p.uuid, p.name FROM domains d JOIN projects p ON d.id = p.domain_id where p.id = $1", projectID).Scan(&domainUUID, &n.DomainName, &projectUUID, &n.ProjectName)
	if err != nil {
		return auditEvents, err
	}

	commitmentsByID, err := db.BuildIndexOfDBResult(dbi, func(c db.ProjectCommitment) db.ProjectCommitmentID { return c.ID }, `SELECT * FROM project_commitments WHERE id = ANY($1)`, pq.Array(confirmedCommitmentIDs))
	if err != nil {
		return auditEvents, err
	}

	for _, cID := range confirmedCommitmentIDs {
		c, exists := commitmentsByID[cID]
		if !exists {
			return auditEvents, fmt.Errorf("tried to generate mail notification for non-existent commitment ID %d", cID)
		}
		confirmedAt := c.ConfirmedAt.UnwrapOr(time.Unix(0, 0)) // the UnwrapOr() is defense in depth, it should never be relevant because we only notify for confirmed commitments here

		// push one confirmation audit event per commitment, because they belong to separate CCRs
		auditEvents = append(auditEvents, audit.CommitmentEventTarget{
			CommitmentChangeRequest: ccrs[c.UUID],
		}.ReplicateForAllProjects(audittools.Event{
			Time:       now,
			Request:    auditContext.Request,
			User:       auditContext.UserIdentity,
			ReasonCode: http.StatusOK,
			Action:     ConfirmAction,
		})...)

		if !c.NotifyOnConfirm {
			continue
		}
		n.Commitments = append(n.Commitments, core.CommitmentNotification{
			Commitment: c,
			DateString: confirmedAt.Format(time.DateOnly),
			// TODO: we actually don't want to have api-named props in AZResourceLocation. Replace the template and the code simultaneously.
			Resource: core.AZResourceLocation{
				ServiceType:      db.ServiceType(apiIdentity.ServiceType),
				ResourceName:     liquid.ResourceName(apiIdentity.Name),
				AvailabilityZone: loc.AvailabilityZone,
			},
		})
	}
	if tpl, exists := mailTemplate.Unpack(); len(n.Commitments) != 0 && exists {
		// push mail notifications
		mail, err := tpl.Render(n, projectID, now)
		if err != nil {
			return auditEvents, err
		}
		err = dbi.Insert(&mail)
		if err != nil {
			return auditEvents, err
		}
	}
	return auditEvents, nil
}

func delegateChangeCommitmentsWithShortcut(ctx context.Context, cluster *core.Cluster, dbi db.Interface, loc core.AZResourceLocation, serviceInfo liquid.ServiceInfo, project db.Project, domain db.Domain, commitment db.ProjectCommitment, stats clusterAZAllocationStats) (accepted bool, ccr liquid.CommitmentChangeRequest, err error) {
	ccr = liquid.CommitmentChangeRequest{
		AZ:          loc.AvailabilityZone,
		InfoVersion: serviceInfo.Version,
		ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
			project.UUID: {
				ProjectMetadata: LiquidProjectMetadataFromDBProject(project, domain, serviceInfo),
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					loc.ResourceName: {
						TotalConfirmedBefore: stats.ProjectStats[commitment.ProjectID].Committed,
						TotalConfirmedAfter:  stats.ProjectStats[commitment.ProjectID].Committed + commitment.Amount,
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
						Commitments: []liquid.Commitment{
							{
								UUID:      commitment.UUID,
								OldStatus: Some(commitment.Status),
								NewStatus: Some(liquid.CommitmentStatusConfirmed),
								Amount:    commitment.Amount,
								ConfirmBy: commitment.ConfirmBy,
								ExpiresAt: commitment.ExpiresAt,
							},
						},
					},
				},
			},
		},
	}

	// optimization: we check locally, when we know that the resource does not manage commitments
	// this avoids having to re-load the stats later in the callchain.
	if !serviceInfo.Resources[loc.ResourceName].HandlesCommitments {
		additions := map[db.ProjectID]uint64{commitment.ProjectID: commitment.Amount}
		behavior := cluster.CommitmentBehaviorForResource(loc.ServiceType, loc.ResourceName)
		accepted = stats.CanAcceptCommitmentChanges(additions, nil, behavior)
	} else {
		commitmentChangeResponse, err := DelegateChangeCommitments(ctx, cluster, ccr, loc, serviceInfo, dbi)
		if err != nil {
			return false, liquid.CommitmentChangeRequest{}, err
		}

		accepted = commitmentChangeResponse.RejectionReason == ""
		if !accepted {
			logg.Info("commitment not accepted for %s: %s", loc.ShortScopeString(), commitmentChangeResponse.RejectionReason)
		}
	}

	// as the totalConfirmed will increase, we don't need to check for RequiresConfirmation() here
	return accepted, audit.EnsureLiquidProjectMetadata(ccr, project, domain, serviceInfo), nil
}
