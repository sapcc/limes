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
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
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
func ConfirmPendingCommitments(ctx context.Context, loc core.AZResourceLocation, unit limes.Unit, cluster *core.Cluster, dbi db.Interface, now time.Time, generateProjectCommitmentUUID func() liquid.CommitmentUUID, generateTransferToken func() string, auditContext audit.Context) (auditEvents []audittools.Event, err error) {
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
		return nil, fmt.Errorf("while enumerating confirmable commitments for %s/%s in %s: %w", loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
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
		return nil, fmt.Errorf("while loading affected projects for %s/%s in %s: %w", loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
	}
	affectedDomainsByID, err := db.BuildIndexOfDBResult(dbi, func(d db.Domain) db.DomainID { return d.ID }, `SELECT * FROM domains WHERE id IN (SELECT domain_id FROM projects WHERE id = ANY($1))`, pq.Array(slices.Collect(maps.Keys(affectedProjectIDs))))
	if err != nil {
		return nil, fmt.Errorf("while loading affected domains for %s/%s in %s: %w", loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
	}

	// initiate cache of transferable commitments
	transferableCommitmentCache, err := NewTransferableCommitmentCache(dbi, loc, now, generateProjectCommitmentUUID, generateTransferToken)
	if err != nil {
		return nil, err
	}

	// load allocation stats
	statsByAZ, err := collectAZAllocationStats(loc.ServiceType, loc.ResourceName, &loc.AvailabilityZone, cluster, dbi)
	if err != nil {
		return nil, err
	}
	stats := statsByAZ[loc.AvailabilityZone]

	// foreach confirmable commitment in the order to be confirmed
	for _, c := range confirmableCommitments {
		// ignore commitments that do not fit
		logg.Debug("checking ConfirmPendingCommitments in %s/%s/%s: commitmentID = %d, projectID = %d, amount = %d",
			loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, c.ID, c.ProjectID, c.Amount)
		project := affectedProjectsByID[c.ProjectID]
		domain := affectedDomainsByID[project.DomainID]

		capacityAccepted, err := checkCommitmentAcceptance(ctx, cluster, dbi, loc, serviceInfo, project, domain, c, stats)
		if err != nil {
			return nil, fmt.Errorf("while checking acceptance of commitment ID=%d for %s/%s in %s: %w", c.ID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
		}
		if !capacityAccepted {
			continue
		}
		// if a commitment was transferred in this iteration already, we do not need to confirm it
		// if partially transferred, the leftover commitment is added to the transferable commitments and considered separately
		if transferableCommitmentCache.CommitmentWasTransferred(c.ID, c.ProjectID) {
			continue
		}

		err = transferableCommitmentCache.CheckAndConsume(c)
		if err != nil {
			return nil, err
		}

		// confirm the commitment
		_, err = dbi.Exec(`UPDATE project_commitments SET confirmed_at = $1, status = $2 WHERE id = $3`,
			now, liquid.CommitmentStatusConfirmed, c.ID)
		if err != nil {
			return nil, fmt.Errorf("while confirming commitment ID=%d for %s/%s in %s: %w", c.ID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
		}
		transferableCommitmentCache.ConfirmTransferableCommitmentIfExists(c.ID, now)
		confirmedCommitmentIDs[c.ProjectID] = append(confirmedCommitmentIDs[c.ProjectID], c.ID)

		// block its allocation from being committed again in this loop
		oldStats := stats.ProjectStats[c.ProjectID]
		stats.ProjectStats[c.ProjectID] = projectAZAllocationStats{
			Committed: oldStats.Committed + c.Amount,
			Usage:     oldStats.Usage,
		}
	}

	// gather some prerequisites for the mail notifications
	apiIdentity := cluster.BehaviorForResource(loc.ServiceType, loc.ResourceName).IdentityInV1API
	transferTemplate := None[core.MailTemplate]()
	confirmationTemplate := None[core.MailTemplate]()
	if mailConfig, exists := cluster.Config.MailNotifications.Unpack(); exists {
		transferTemplate = Some(mailConfig.Templates.TransferredCommitments)
		confirmationTemplate = Some(mailConfig.Templates.ConfirmedCommitments)
	}

	// generate audit events and mail notifications for commitment transfers
	ae, err := transferableCommitmentCache.GenerateAuditEventsAndMails(apiIdentity, unit, auditContext, transferTemplate)
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
		ae, err = generateAuditEventsAndMails(confirmationTemplate, dbi, loc, unit, apiIdentity, projectID, auditContext, confirmations, now)
		if err != nil {
			return nil, err
		}
		auditEvents = append(auditEvents, ae...)
	}

	return auditEvents, nil
}

func generateAuditEventsAndMails(mailTemplate Option[core.MailTemplate], dbi db.Interface, loc core.AZResourceLocation, unit limes.Unit, apiIdentity core.ResourceRef, projectID db.ProjectID, auditContext audit.Context,
	confirmedCommitmentIDs []db.ProjectCommitmentID, now time.Time) (auditEvents []audittools.Event, err error) {

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

	var (
		auditEventCommitments     []limesresources.Commitment
		auditEventCommitmentIDs   []db.ProjectCommitmentID
		auditEventCommitmentUUIDs []liquid.CommitmentUUID
	)
	for _, cID := range confirmedCommitmentIDs {
		c, exists := commitmentsByID[cID]
		if !exists {
			return auditEvents, fmt.Errorf("tried to generate mail notification for non-existent commitment ID %d", cID)
		}
		confirmedAt := c.ConfirmedAt.UnwrapOr(time.Unix(0, 0)) // the UnwrapOr() is defense in depth, it should never be relevant because we only notify for confirmed commitments here
		auditEventCommitments = append(auditEventCommitments, ConvertCommitmentToDisplayForm(c, loc, apiIdentity, false, unit))
		auditEventCommitmentIDs = append(auditEventCommitmentIDs, c.ID)
		auditEventCommitmentUUIDs = append(auditEventCommitmentUUIDs, c.UUID)

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
	if len(auditEventCommitments) != 0 {
		// push confirmation event
		auditEvents = append(auditEvents, audittools.Event{
			Time:       now,
			Request:    auditContext.Request,
			User:       auditContext.UserIdentity,
			ReasonCode: http.StatusOK,
			Action:     ConfirmAction,
			Target: audit.CommitmentEventTarget{
				DomainID:    domainUUID,
				DomainName:  n.DomainName,
				ProjectID:   projectUUID,
				ProjectName: n.ProjectName,
				WorkflowContext: Some(db.CommitmentWorkflowContext{
					Reason:                 db.CommitmentReasonConfirm,
					RelatedCommitmentIDs:   auditEventCommitmentIDs,
					RelatedCommitmentUUIDs: auditEventCommitmentUUIDs,
				}),
				Commitments: auditEventCommitments,
			},
		})
	}

	return auditEvents, nil
}

func checkCommitmentAcceptance(ctx context.Context, cluster *core.Cluster, dbi db.Interface, loc core.AZResourceLocation, serviceInfo liquid.ServiceInfo, project db.Project, domain db.Domain, commitment db.ProjectCommitment, stats clusterAZAllocationStats) (bool, error) {
	// optimization: we check locally, when we know that the resource does not manage commitments
	// this avoids having to re-load the stats later in the callchain.
	if !serviceInfo.Resources[loc.ResourceName].HandlesCommitments {
		additions := map[db.ProjectID]uint64{commitment.ProjectID: commitment.Amount}
		behavior := cluster.CommitmentBehaviorForResource(loc.ServiceType, loc.ResourceName)
		return stats.CanAcceptCommitmentChanges(additions, nil, behavior), nil
	}

	commitmentChangeRequest := liquid.CommitmentChangeRequest{
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
	commitmentChangeResponse, err := DelegateChangeCommitments(ctx, cluster, commitmentChangeRequest, loc.ServiceType, serviceInfo, dbi)
	if err != nil {
		return false, err
	}

	accepted := commitmentChangeResponse.RejectionReason == ""
	if !accepted {
		logg.Info("commitment not accepted for %s/%s/%s: %s", loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, commitmentChangeResponse.RejectionReason)
	}

	// as the totalConfirmed will increase, we don't need to check for RequiresConfirmation() here
	return accepted, nil
}
