// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/lib/pq"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/cadf"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/audit"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// Commitments with transfer_status=public are selected to be matched with confirmable
// commitments in the order they were posted in for transfer. The status of a
// transferable commitment does not matter for this operation.
//
// The final `ORDER BY pc.id` ordering ensures deterministic behavior in tests, in reality
// the probability of multiple commitments set to transfer at the exact same time is
// very small due to the atomicity of the API operation.
var getTransferableCommitmentsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		SELECT pc.*
		FROM services s
		JOIN resources r ON r.service_id = s.id
		JOIN az_resources azr ON azr.resource_id = r.id
		JOIN project_commitments pc ON pc.az_resource_id = azr.id
		WHERE s.type = $1 AND r.name = $2 AND azr.az = $3
			AND pc.transfer_status = {{limesresources.CommitmentTransferStatusPublic}}
			AND pc.status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}})
		ORDER BY pc.transfer_started_at ASC, pc.created_at ASC, pc.id ASC
	`))

// TransferableCommitmentCache handles the consumption of transferable commitments.
// It's main functionality is to cache the state of the these commitments between
// calls of CanConfirmWithTransfers, so that continuous reloading is avoided. Therefore, an
// instance of it should be obtained by NewTransferableCommitmentManager and reused
// between subsequent calls.
type TransferableCommitmentCache struct {
	// transferableCommitments holds the order of the transferableCommitments to consume in (see query).
	transferableCommitments []*db.ProjectCommitment
	// transferableCommitmentsByID allows quick lookup of commitments by their ID (see ReplaceTransferableCommitment).
	transferableCommitmentsByID map[db.ProjectCommitmentID]*db.ProjectCommitment
	// transferredCommitmentIDs holds the IDs of commitments that have already been transferred.
	transferredCommitmentIDs map[db.ProjectID]map[db.ProjectCommitmentID]commitmentTransferLeftover

	// project and domain structures for use in requests, mails etc.
	// NOTE: These get enriched with data of projects/ domains that have confirmable commitments in the process.
	affectedProjectsByID   map[db.ProjectID]db.Project
	affectedProjectsByUUID map[liquid.ProjectUUID]db.Project
	affectedDomainsByID    map[db.DomainID]db.Domain

	// in order to deduplicate audit events between confirmation outside the cache and transfer inside,
	// we keep track of the audit events by confirmed commitment. At the end, RetrieveAuditEvents should be called.
	auditEventsByConfirmedCommitmentUUID map[liquid.CommitmentUUID][]audittools.Event

	// utilities
	dbi                           db.Interface
	cluster                       *core.Cluster
	serviceInfo                   liquid.ServiceInfo
	loc                           core.AZResourceLocation
	now                           time.Time
	generateProjectCommitmentUUID func() liquid.CommitmentUUID
	generateTransferToken         func() string
	mailTemplate                  Option[core.MailTemplate]

	// we have to keep track of stats always, to provide totalConfirmed in CCRs
	liquidHandlesCommitments bool
	stats                    clusterAZAllocationStats
}

// NewTransferableCommitmentCache builds a TransferableCommitmentCache and fills it.
func NewTransferableCommitmentCache(dbi db.Interface, cluster *core.Cluster, serviceInfo liquid.ServiceInfo, loc core.AZResourceLocation, now time.Time, generateProjectCommitmentUUID func() liquid.CommitmentUUID, generateTransferToken func() string, mailTemplate Option[core.MailTemplate]) (t TransferableCommitmentCache, err error) {
	queryArgs := []any{loc.ServiceType, loc.ResourceName, loc.AvailabilityZone}
	_, err = dbi.Select(&t.transferableCommitments, getTransferableCommitmentsQuery, queryArgs...)
	if err != nil {
		return t, fmt.Errorf("while enumerating transferable commitments for %s: %w", loc.ScopeString(), err)
	}
	t.transferableCommitmentsByID = make(map[db.ProjectCommitmentID]*db.ProjectCommitment, len(t.transferableCommitments))
	affectedProjectIDs := make(map[db.ProjectID]struct{})
	for i := range t.transferableCommitments {
		t.transferableCommitmentsByID[t.transferableCommitments[i].ID] = t.transferableCommitments[i]
		affectedProjectIDs[t.transferableCommitments[i].ProjectID] = struct{}{}
	}
	t.transferredCommitmentIDs = make(map[db.ProjectID]map[db.ProjectCommitmentID]commitmentTransferLeftover)

	// project and domain structures
	t.affectedProjectsByID, err = db.BuildIndexOfDBResult(dbi, func(p db.Project) db.ProjectID { return p.ID }, `SELECT * from projects WHERE id = ANY($1)`, pq.Array(slices.Collect(maps.Keys(affectedProjectIDs))))
	if err != nil {
		return t, fmt.Errorf("while loading projects with transferable commitments for %s: %w", loc.ScopeString(), err)
	}
	t.affectedProjectsByUUID = make(map[liquid.ProjectUUID]db.Project)
	for _, project := range t.affectedProjectsByID {
		t.affectedProjectsByUUID[project.UUID] = project
	}
	t.affectedDomainsByID, err = db.BuildIndexOfDBResult(dbi, func(d db.Domain) db.DomainID { return d.ID }, `SELECT * from domains WHERE id IN (SELECT domain_id FROM projects WHERE id = ANY($1))`, pq.Array(slices.Collect(maps.Keys(affectedProjectIDs))))
	if err != nil {
		return t, fmt.Errorf("while loading domains with projects with transferable commitments for %s: %w", loc.ScopeString(), err)
	}

	// prep audit event storage
	t.auditEventsByConfirmedCommitmentUUID = make(map[liquid.CommitmentUUID][]audittools.Event)

	// fill utilities
	t.dbi = dbi
	t.cluster = cluster
	t.serviceInfo = serviceInfo
	t.loc = loc
	t.now = now
	t.generateProjectCommitmentUUID = generateProjectCommitmentUUID
	t.generateTransferToken = generateTransferToken
	t.mailTemplate = mailTemplate

	// determine whether liquid handles commitments for this resource
	t.liquidHandlesCommitments = t.serviceInfo.Resources[t.loc.ResourceName].HandlesCommitments
	statsByAZ, err := collectAZAllocationStats(loc.ServiceType, loc.ResourceName, Some(loc.AvailabilityZone), cluster, dbi)
	if err != nil {
		return t, fmt.Errorf("while collecting AZ stats for %s: %w", loc.ScopeString(), err)
	}
	t.stats = statsByAZ[loc.AvailabilityZone]

	return t, nil
}

// CanConfirmWithTransfers checks whether the given db.ProjectCommitment can take over 1 to n
// of the transferableCommitments and whether the missing amount can be confirmed.
// If so, the transferableCommitments get modified according to the new state. The commitment
// to takeover the transferred amount is not subject to change in this operation, i.e. any
// update to it should be done after calling this function and any errors should cancel the transaction.
// All relations to the consuming commitment are stored in the SupersedeContextJSON of the consumed
// transferableCommitments.
//
// Commitment consumption follows the following rules:
// We consume maximum the amount of the given commitment in one run.
// The status of a transferableCommitment to be consumed does not matter.
// When the missing amount does not fit into the capacity, no commitments are consumed at all.
// When a commitment is both pending and transferable, the handling depends on the order:
// When confirmed first, it might be taken over later anyway. Therefore, when confirming a
// commitment outside the cache, always call ConfirmTransferableCommitmentIfExists. When a
// commitment gets transferred first, it should not get confirmed later. For this,
// CommitmentWasTransferred should be used to check before confirming a commitment outside the cache.
// To enable mail-collation, GenerateTransferMails should be called after all calls to CanConfirmWithTransfers.
func (t *TransferableCommitmentCache) CanConfirmWithTransfers(ctx context.Context, c db.ProjectCommitment, project db.Project, domain db.Domain, isNew, dryRun bool, auditContext audit.Context, auditAction cadf.Action) (result liquid.CommitmentChangeResponse, err error) {
	// We add the project and domain to the affected lists, so that we can refer to them in private functions more easily.
	t.affectedProjectsByID[project.ID] = project
	t.affectedProjectsByUUID[project.UUID] = project
	t.affectedDomainsByID[domain.ID] = domain

	// For checking the capacity we place the new commitment into a CCR, that we will enrich with the transfers below.
	oldStatus := Some(c.Status)
	if isNew {
		oldStatus = None[liquid.CommitmentStatus]()
	}
	ccr := liquid.CommitmentChangeRequest{
		DryRun:      dryRun,
		AZ:          t.loc.AvailabilityZone,
		InfoVersion: t.serviceInfo.Version,
		ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
			project.UUID: {
				ProjectMetadata: LiquidProjectMetadataFromDBProject(project, domain),
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					t.loc.ResourceName: {
						TotalConfirmedBefore: t.stats.ProjectStats[c.ProjectID].Committed,
						TotalConfirmedAfter:  t.stats.ProjectStats[c.ProjectID].Committed + c.Amount,
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
						Commitments: []liquid.Commitment{
							{
								UUID:      c.UUID,
								OldStatus: oldStatus,
								NewStatus: Some(liquid.CommitmentStatusConfirmed),
								Amount:    c.Amount,
								ExpiresAt: c.ExpiresAt,
							},
						},
					},
				},
			},
		},
	}
	cacs := make(map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset)
	var (
		potentiallyTransferredCommitmentIdxs []int
		lastConsumedAmount                   uint64
		leftoverCommitment                   db.ProjectCommitment
		overallTransferredAmount             uint64
	)

	for idx, tc := range t.transferableCommitments {
		// First, we check whether we have already transferred the full amount.
		if overallTransferredAmount == c.Amount {
			break
		}
		// Commitments cannot be consumed within the same project, mostly to avoid
		// easily exploitable loopholes in the commitment confirmation process.
		if tc.ProjectID == c.ProjectID {
			continue
		}
		// A commitment is only consumed if it's expires_at <= expires_at of the commitment we confirm.
		if tc.ExpiresAt.After(c.ExpiresAt) {
			continue
		}
		// do not consume a commitment that has already been fully consumed
		// NOTE: this branch will not be taken for partially consumed commitments, because transferableCommitments
		// contains the newly spawned leftover commitment instead.
		if _, exists := t.transferredCommitmentIDs[tc.ProjectID][tc.ID]; exists {
			continue
		}

		// commitment is considered for transfer - add it to the list
		potentiallyTransferredCommitmentIdxs = append(potentiallyTransferredCommitmentIdxs, idx)

		// prep CCR structures if empty
		tcProject := t.affectedProjectsByID[tc.ProjectID]
		if _, exists := ccr.ByProject[tcProject.UUID]; !exists {
			tcDomain := t.affectedDomainsByID[tcProject.DomainID]
			ccr.ByProject[tcProject.UUID] = liquid.ProjectCommitmentChangeset{
				ProjectMetadata: LiquidProjectMetadataFromDBProject(tcProject, tcDomain),
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					t.loc.ResourceName: {
						TotalConfirmedBefore: t.stats.ProjectStats[tc.ProjectID].Committed,
						TotalConfirmedAfter:  t.stats.ProjectStats[tc.ProjectID].Committed, // will be adjusted below based on how much is consumed
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
					},
				},
			}
		}
		rcc := ccr.ByProject[tcProject.UUID].ByResource[t.loc.ResourceName]

		// modify CCR/ CAC structures
		amountToConsume := c.Amount - overallTransferredAmount
		if tc.Amount > amountToConsume {
			// The leftover amount to be transferred is not enough to consume the whole commitment.
			// We have to create a leftover for the transferable commitment.
			leftoverCommitment, err = BuildSplitCommitment(*tc, tc.Amount-amountToConsume, t.now, t.generateProjectCommitmentUUID)
			if err != nil {
				return result, err
			}
			leftoverCommitment.TransferStatus = limesresources.CommitmentTransferStatusPublic
			leftoverCommitment.TransferToken = Some(t.generateTransferToken())
			leftoverCommitment.TransferStartedAt = tc.TransferStartedAt
			rcc.Commitments = append(rcc.Commitments, liquid.Commitment{
				UUID:      leftoverCommitment.UUID,
				OldStatus: None[liquid.CommitmentStatus](),
				NewStatus: Some(leftoverCommitment.Status),
				Amount:    leftoverCommitment.Amount,
				ConfirmBy: leftoverCommitment.ConfirmBy,
				ExpiresAt: leftoverCommitment.ExpiresAt,
			})

			lastConsumedAmount = amountToConsume
		} else {
			// the transferable commitment is fully consumed
			lastConsumedAmount = tc.Amount
		}
		overallTransferredAmount += lastConsumedAmount

		rcc.Commitments = append(rcc.Commitments, liquid.Commitment{
			UUID:      tc.UUID,
			OldStatus: Some(tc.Status),
			NewStatus: Some(liquid.CommitmentStatusSuperseded),
			Amount:    tc.Amount,
			ConfirmBy: tc.ConfirmBy,
			ExpiresAt: tc.ExpiresAt,
		})
		if tc.Status == liquid.CommitmentStatusConfirmed {
			rcc.TotalConfirmedAfter -= lastConsumedAmount
		}
		ccr.ByProject[tcProject.UUID].ByResource[t.loc.ResourceName] = rcc
		cacs[tc.UUID] = audit.CommitmentAttributeChangeset{
			OldTransferStatus: tc.TransferStatus,
			NewTransferStatus: limesresources.CommitmentTransferStatusNone,
		}
	}

	// check that the ccr is accepted
	logg.Debug("checking CanConfirmWithTransfers in %s: commitmentID = %d, projectID = %d, overall amount = %d, missing amount = %d",
		t.loc.ShortScopeString(), c.ID, c.ProjectID, c.Amount, c.Amount-overallTransferredAmount)
	result, err = t.delegateChangeCommitmentsWithShortcut(ctx, ccr)
	if err != nil {
		return result, err
	}
	if result.RejectionReason != "" || dryRun {
		return result, nil
	}

	// adjust stats locally
	t.updateStats(ccr)

	// add audit events
	t.auditEventsByConfirmedCommitmentUUID[c.UUID] = t.assembleAuditEvents(ccr, cacs, project.UUID, auditAction, auditContext)

	// add commitment changes to database
	for i, idx := range slices.Backward(potentiallyTransferredCommitmentIdxs) {
		tc := t.transferableCommitments[idx]
		if _, exists := t.transferredCommitmentIDs[tc.ProjectID]; !exists {
			t.transferredCommitmentIDs[tc.ProjectID] = make(map[db.ProjectCommitmentID]commitmentTransferLeftover)
		}

		// delete the audit event, if the commitment was confirmed previously
		delete(t.auditEventsByConfirmedCommitmentUUID, tc.UUID)

		// Insert the leftover commitment, if exists.
		if i == 0 && tc.Amount > lastConsumedAmount {
			err = t.dbi.Insert(&leftoverCommitment)
			if err != nil {
				return result, err
			}

			t.transferableCommitments[idx] = &leftoverCommitment
			t.transferableCommitmentsByID[leftoverCommitment.ID] = &leftoverCommitment
			t.transferredCommitmentIDs[tc.ProjectID][tc.ID] = commitmentTransferLeftover{
				Amount: tc.Amount - lastConsumedAmount,
				ID:     leftoverCommitment.ID,
			}
		} else {
			t.transferredCommitmentIDs[tc.ProjectID][tc.ID] = commitmentTransferLeftover{}
		}

		// supersede consumed commitment
		tc.TransferStartedAt = None[time.Time]()
		tc.TransferStatus = limesresources.CommitmentTransferStatusNone
		tc.TransferToken = None[string]()
		tc.Status = liquid.CommitmentStatusSuperseded
		tc.SupersededAt = Some(t.now)
		supersedeContext := db.CommitmentWorkflowContext{
			Reason:                 db.CommitmentReasonConsume,
			RelatedCommitmentIDs:   []db.ProjectCommitmentID{c.ID},
			RelatedCommitmentUUIDs: []liquid.CommitmentUUID{c.UUID},
		}
		buf, err := json.Marshal(supersedeContext)
		if err != nil {
			return result, err
		}
		tc.SupersedeContextJSON = Some(json.RawMessage(buf))
		_, err = t.dbi.Update(tc)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

// ConfirmTransferableCommitmentIfExists should be used between calls to CanConfirmWithTransfers
// when a commitment has been confirmed outside of the cache. The function does nothing,
// when the commitment with the given ID is not in the cache.
func (t *TransferableCommitmentCache) ConfirmTransferableCommitmentIfExists(id db.ProjectCommitmentID, confirmedAt time.Time) {
	if t, exists := t.transferableCommitmentsByID[id]; exists {
		t.Status = liquid.CommitmentStatusConfirmed
		t.ConfirmedAt = Some(confirmedAt)
	}
}

// CommitmentWasTransferred returns true, when the commitment with the given ID
// has already been transferred. This can be used to skip confirmation of already
// transferred commitments.
func (t *TransferableCommitmentCache) CommitmentWasTransferred(id db.ProjectCommitmentID, projectID db.ProjectID) bool {
	_, exists := t.transferredCommitmentIDs[projectID][id]
	return exists
}

// getTransferredCommitmentsForProject returns all commitments that have been transferred
// for the given project via CanConfirmWithTransfers. This can be used to deduplicate
// the transferred with the confirmed commitments when processing outside the cache.
func (t *TransferableCommitmentCache) getTransferredCommitmentsForProject(projectID db.ProjectID) map[db.ProjectCommitmentID]commitmentTransferLeftover {
	return t.transferredCommitmentIDs[projectID]
}

// GenerateTransferMails generates the mail notifications for all transferred commitments
// that were processed via CanConfirmWithTransfers. For that, it collates multiple consecutive partial
// transfers to only generate a mail from the initial to the latest state.
func (t *TransferableCommitmentCache) GenerateTransferMails(apiIdentity core.ResourceRef) error {
	// The system can be configured to not send mails (e.g. for test systems).
	tpl, tplExists := t.mailTemplate.Unpack()
	if !tplExists {
		return nil
	}

	// first, we deduplicate the transfers per project by linking the last leftover to the first transfer commitment
	for _, projectID := range slices.Sorted(maps.Keys(t.transferredCommitmentIDs)) {
		transfers := t.transferredCommitmentIDs[projectID]
		notifiableTransfers := make(map[db.ProjectCommitmentID]commitmentTransferLeftover)
		// we go through the transfers by ID descending, because that enables the linking operation in O(n)
		// (leftover commitments have a higher ID than the superseded commitment that they were split from)
		for _, cID := range slices.Backward(slices.Sorted(maps.Keys(transfers))) {
			// for transfers which have a leftover, we link the transferCommitment to the last leftover via a new data structure
			transferredLeftover := transfers[cID]
			if followingLeftover, exists := notifiableTransfers[transferredLeftover.ID]; exists {
				notifiableTransfers[cID] = commitmentTransferLeftover{
					Amount: followingLeftover.Amount,
					ID:     followingLeftover.ID,
				}
				delete(notifiableTransfers, transferredLeftover.ID)
			} else {
				notifiableTransfers[cID] = transferredLeftover
			}
		}

		// gather mail notifications for this project
		affectedProject := t.affectedProjectsByID[projectID]
		affectedDomain := t.affectedDomainsByID[affectedProject.DomainID]
		n := core.CommitmentGroupNotification{
			DomainName:  affectedDomain.Name,
			ProjectName: affectedProject.Name,
			Commitments: make([]core.CommitmentNotification, 0, len(notifiableTransfers)),
		}

		for _, cID := range slices.Sorted(maps.Keys(notifiableTransfers)) {
			leftover := notifiableTransfers[cID]
			c, exists := t.transferableCommitmentsByID[cID]
			// defense in depth: this should never happen
			if !exists {
				return fmt.Errorf("tried to generate mail notification for non-existent commitment ID %d", cID)
			}

			// also defense in depth, as all transferred commitments are superseded in CanConfirmWithTransfers
			confirmedAt := c.SupersededAt.UnwrapOr(time.Unix(0, 0))
			n.Commitments = append(n.Commitments, core.CommitmentNotification{
				Commitment: *c,
				DateString: confirmedAt.Format(time.DateOnly),
				Resource: core.AZResourceLocation{
					ServiceType:      db.ServiceType(apiIdentity.ServiceType),
					ResourceName:     liquid.ResourceName(apiIdentity.Name),
					AvailabilityZone: t.loc.AvailabilityZone,
				},
				LeftoverAmount: leftover.Amount,
			})
		}
		if len(n.Commitments) != 0 {
			// push mail notifications to database
			mail, err := tpl.Render(n, projectID, t.now)
			if err != nil {
				return err
			}
			err = t.dbi.Insert(&mail)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// RetrieveAuditEvents returns all audit events. It should be called after all calls to CanConfirmWithTransfers
// as they get deduplicated in the process.
func (t *TransferableCommitmentCache) RetrieveAuditEvents() []audittools.Event {
	var events []audittools.Event
	for _, evs := range t.auditEventsByConfirmedCommitmentUUID {
		events = append(events, evs...)
	}
	return events
}

///////////////////////////////////////////////////////////////////////////
// internal functions:

// assembleAuditEvents constructs the audit events for all affected projects
// with the commitments that were processed via CanConfirmWithTransfers.
func (t *TransferableCommitmentCache) assembleAuditEvents(ccr liquid.CommitmentChangeRequest, cacs map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset, consumingProjectUUID liquid.ProjectUUID, consumeAction cadf.Action, auditContext audit.Context) []audittools.Event {
	return audit.CommitmentEventTarget{
		CommitmentChangeRequest:       ccr,
		CommitmentAttributeChangesets: cacs,
	}.ReplicateForAllProjects(audittools.Event{
		Time:       t.now,
		Request:    auditContext.Request,
		User:       auditContext.UserIdentity,
		ReasonCode: http.StatusOK, // value for the transfer commitments
		Action:     ConsumeAction, // value for the transfer commitments
	}, Some(consumeAction), Some(consumingProjectUUID))
}

// delegateChangeCommitmentsWithShortcut calls DelegateChangeCommitments unless we know
// that the resource does not manage commitments, in which case we can shortcut the call
// by checking the capacity locally. This way, only the local stats get used in the process.
func (t *TransferableCommitmentCache) delegateChangeCommitmentsWithShortcut(ctx context.Context, ccr liquid.CommitmentChangeRequest) (result liquid.CommitmentChangeResponse, err error) {
	// optimization: we check locally, when we know that the resource does not manage commitments
	// this avoids having to re-load the stats later in the callchain.
	switch {
	case !ccr.RequiresConfirmation():
		result = liquid.CommitmentChangeResponse{}
	case !t.liquidHandlesCommitments:
		behavior := t.cluster.CommitmentBehaviorForResource(t.loc.ServiceType, t.loc.ResourceName)
		additions := make(map[db.ProjectID]uint64)
		subtractions := make(map[db.ProjectID]uint64)
		for projectUUID, pcc := range ccr.ByProject {
			rcc := pcc.ByResource[t.loc.ResourceName]
			affectedProject := t.affectedProjectsByUUID[projectUUID]
			if rcc.TotalConfirmedAfter > rcc.TotalConfirmedBefore {
				additions[affectedProject.ID] = rcc.TotalConfirmedAfter - rcc.TotalConfirmedBefore
			}
			if rcc.TotalConfirmedBefore > rcc.TotalConfirmedAfter {
				subtractions[affectedProject.ID] = rcc.TotalConfirmedBefore - rcc.TotalConfirmedAfter
			}
		}
		accepted := t.stats.CanAcceptCommitmentChanges(additions, subtractions, behavior)
		if !accepted {
			result = liquid.CommitmentChangeResponse{
				RejectionReason: "not enough capacity!",
				RetryAt:         None[time.Time](),
			}
		}
	default:
		commitmentChangeResponse, err := DelegateChangeCommitments(ctx, t.cluster, ccr, t.loc, t.serviceInfo, t.dbi)
		if err != nil {
			return result, err
		}
		result = commitmentChangeResponse
	}
	if result.RejectionReason != "" {
		logg.Info("commitment not accepted for %s: %s", t.loc.ShortScopeString(), result.RejectionReason)
	}
	return result, nil
}

// updateStats modifies the local stats according to the given CCR.
func (t *TransferableCommitmentCache) updateStats(ccr liquid.CommitmentChangeRequest) {
	for projectUUID, pcc := range ccr.ByProject {
		rcc := pcc.ByResource[t.loc.ResourceName]
		affectedProject := t.affectedProjectsByUUID[projectUUID]
		projectStats := t.stats.ProjectStats[affectedProject.ID]
		if rcc.TotalConfirmedAfter != rcc.TotalConfirmedBefore {
			newProjectStats := projectAZAllocationStats{
				Committed:          rcc.TotalConfirmedAfter,
				Usage:              projectStats.Usage,
				MinHistoricalUsage: projectStats.MinHistoricalUsage,
				MaxHistoricalUsage: projectStats.MaxHistoricalUsage,
			}
			t.stats.ProjectStats[affectedProject.ID] = newProjectStats
		}
	}
}
