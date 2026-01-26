// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/lib/pq"
	. "github.com/majewsky/gg/option"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
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
// calls of CheckAndConsume, so that continuous reloading is avoided. Therefore, an
// instance of it should be obtained by NewTransferableCommitmentManager and reused
// between subsequent calls.
type TransferableCommitmentCache struct {
	// transferableCommitments holds the order of the transferableCommitments to consume in (see query).
	transferableCommitments []db.ProjectCommitment
	// transferableCommitmentsByID allows quick lookup of commitments by their ID (see ReplaceTransferableCommitment).
	transferableCommitmentsByID map[db.ProjectCommitmentID]*db.ProjectCommitment
	// transferredCommitmentIDs holds the IDs of commitments that have already been transferred.
	transferredCommitmentIDs map[db.ProjectID]map[db.ProjectCommitmentID]commitmentTransferLeftover
	// affectedProjectsByID hold all projects that have transferable commitments
	affectedProjectsByID map[db.ProjectID]db.Project
	// affectedDomainsByID hold all domains that have projects with transferable commitments
	affectedDomainsByID map[db.DomainID]db.Domain

	// utilities
	dbi                           db.Interface
	serviceInfo                   liquid.ServiceInfo
	loc                           core.AZResourceLocation
	now                           time.Time
	generateProjectCommitmentUUID func() liquid.CommitmentUUID
	generateTransferToken         func() string
	mailTemplate                  Option[core.MailTemplate]

	// the following fields are used for caching between CheckAndConsume() and GenerateAuditEventsAndMails()
	ccrs map[liquid.CommitmentUUID]liquid.CommitmentChangeRequest
	cacs map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset
}

// NewTransferableCommitmentCache builds a TransferableCommitmentCache and fills it.
func NewTransferableCommitmentCache(dbi db.Interface, serviceInfo liquid.ServiceInfo, loc core.AZResourceLocation, now time.Time, generateProjectCommitmentUUID func() liquid.CommitmentUUID, generateTransferToken func() string, mailTemplate Option[core.MailTemplate]) (t TransferableCommitmentCache, err error) {
	queryArgs := []any{loc.ServiceType, loc.ResourceName, loc.AvailabilityZone}
	_, err = dbi.Select(&t.transferableCommitments, getTransferableCommitmentsQuery, queryArgs...)
	if err != nil {
		return t, fmt.Errorf("while enumerating transferable commitments for %s: %w", loc.ScopeString(), err)
	}
	t.transferableCommitmentsByID = make(map[db.ProjectCommitmentID]*db.ProjectCommitment, len(t.transferableCommitments))
	affectedProjectIDs := make(map[db.ProjectID]struct{})
	for i := range t.transferableCommitments {
		t.transferableCommitmentsByID[t.transferableCommitments[i].ID] = &t.transferableCommitments[i]
		affectedProjectIDs[t.transferableCommitments[i].ProjectID] = struct{}{}
	}

	t.transferredCommitmentIDs = make(map[db.ProjectID]map[db.ProjectCommitmentID]commitmentTransferLeftover)

	t.affectedProjectsByID, err = db.BuildIndexOfDBResult(dbi, func(p db.Project) db.ProjectID { return p.ID }, `SELECT * from projects WHERE id = ANY($1)`, pq.Array(slices.Collect(maps.Keys(affectedProjectIDs))))
	if err != nil {
		return t, fmt.Errorf("while loading projects with transferable commitments for %s: %w", loc.ScopeString(), err)
	}
	t.affectedDomainsByID, err = db.BuildIndexOfDBResult(dbi, func(d db.Domain) db.DomainID { return d.ID }, `SELECT * from domains WHERE id IN (SELECT domain_id FROM projects WHERE id = ANY($1))`, pq.Array(slices.Collect(maps.Keys(affectedProjectIDs))))
	if err != nil {
		return t, fmt.Errorf("while loading domains with projects with transferable commitments for %s: %w", loc.ScopeString(), err)
	}

	// fill utilities
	t.dbi = dbi
	t.serviceInfo = serviceInfo
	t.loc = loc
	t.now = now
	t.generateProjectCommitmentUUID = generateProjectCommitmentUUID
	t.generateTransferToken = generateTransferToken
	t.mailTemplate = mailTemplate

	// initialize caching fields
	t.ccrs = make(map[liquid.CommitmentUUID]liquid.CommitmentChangeRequest)
	t.cacs = make(map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset)

	return t, nil
}

// CheckAndConsume checks whether the given db.ProjectCommitment can take over 1 to n
// of the transferableCommitments. If so, the transferableCommitments get modified
// according to the new state. The commitment to takeover the transferred amount is
// not subject to any change in this operation. All relations to the consuming commitment
// are stored in the SupersedeContextJSON of the consumed transferableCommitments.
//
// Commitment consumption follows the following rules:
// We consume maximum the amount of the given commitment in one run.
// The status of a transferableCommitment to be consumed does not matter.
// When a commitment is both pending and transferable, the handling depends on the order:
// When confirmed first, it might be taken over later anyway. Therefore, when confirming a
// commitment while using the cache, always call ConfirmTransferableCommitmentIfExists
// when a commitment gets confirmed outside of the cache.
// When a commitment gets transferred first, it should not get confirmed later. For this,
// CommitmentWasTransferred should be used to check before confirming a commitment.
// All transfers will lead to a mail which contains the leftover amount, so that the customer
// can track the whole processing of the transferred commitment over time.
func (t *TransferableCommitmentCache) CheckAndConsume(c db.ProjectCommitment, currentTotalConfirmed uint64) (err error) {
	overallTransferredAmount := uint64(0)
	for idx, tc := range t.transferableCommitments {
		if overallTransferredAmount == c.Amount {
			break
		}
		// commitments cannot be consumed within the same project, mostly to avoid
		// easily exploitable loopholes in the commitment confirmation process
		if tc.ProjectID == c.ProjectID {
			continue
		}
		// A commitment is only consumed if it's expires_at <= expires_at of the commitment we confirm.
		if tc.ExpiresAt.After(c.ExpiresAt) {
			continue
		}
		// do not consume a commitment that has already been fully consumed
		// NOTE: this branch will not be taken for partially consumed commitments, because `transferableCommitments`
		// contains the newly spawned leftover commitment instead
		if _, exists := t.transferredCommitmentIDs[tc.ProjectID][tc.ID]; exists {
			continue
		}
		// all checks passed, so this project gets at least one transfer
		if _, exists := t.transferredCommitmentIDs[tc.ProjectID]; !exists {
			t.transferredCommitmentIDs[tc.ProjectID] = make(map[db.ProjectCommitmentID]commitmentTransferLeftover)
		}

		// prepare audit event data
		project := t.affectedProjectsByID[tc.ProjectID]
		domain := t.affectedDomainsByID[project.DomainID]
		auditResource := liquid.ResourceCommitmentChangeset{
			TotalConfirmedBefore: currentTotalConfirmed,
			TotalConfirmedAfter:  currentTotalConfirmed, // will be adjusted below based on how much is consumed
			// TODO: change when introducing "guaranteed" commitments
			TotalGuaranteedBefore: 0,
			TotalGuaranteedAfter:  0,
			Commitments: []liquid.Commitment{
				{
					UUID:      tc.UUID,
					OldStatus: Some(tc.Status),
					NewStatus: Some(liquid.CommitmentStatusSuperseded),
					Amount:    tc.Amount,
					ConfirmBy: tc.ConfirmBy,
					ExpiresAt: tc.ExpiresAt,
				},
			},
		}
		t.cacs[tc.UUID] = audit.CommitmentAttributeChangeset{
			OldTransferStatus: Some(tc.TransferStatus),
			NewTransferStatus: Some(limesresources.CommitmentTransferStatusNone),
		}

		// at least a part of this commitment will be consumed, so we will supersede it in any case
		amountToConsume := c.Amount - overallTransferredAmount
		if tc.Amount > amountToConsume {
			// the leftover amount to be transferred is not enough to consume the whole commitment
			// we will place a new commitment for the leftover amount
			overallTransferredAmount += amountToConsume
			leftoverCommitment, err := BuildSplitCommitment(tc, tc.Amount-amountToConsume, t.now, t.generateProjectCommitmentUUID)
			if err != nil {
				return err
			}
			leftoverCommitment.TransferStatus = limesresources.CommitmentTransferStatusPublic
			leftoverCommitment.TransferToken = Some(t.generateTransferToken())
			leftoverCommitment.TransferStartedAt = tc.TransferStartedAt
			err = t.dbi.Insert(&leftoverCommitment)
			if err != nil {
				return err
			}

			t.transferableCommitments[idx] = leftoverCommitment
			t.transferableCommitmentsByID[leftoverCommitment.ID] = &leftoverCommitment
			t.transferredCommitmentIDs[tc.ProjectID][tc.ID] = commitmentTransferLeftover{
				Amount: tc.Amount - amountToConsume,
				ID:     leftoverCommitment.ID,
			}

			auditResource.Commitments = append(auditResource.Commitments, liquid.Commitment{
				UUID:      leftoverCommitment.UUID,
				OldStatus: None[liquid.CommitmentStatus](),
				NewStatus: Some(leftoverCommitment.Status),
				Amount:    leftoverCommitment.Amount,
				ConfirmBy: leftoverCommitment.ConfirmBy,
				ExpiresAt: leftoverCommitment.ExpiresAt,
			})
			if tc.Status == liquid.CommitmentStatusConfirmed {
				auditResource.TotalConfirmedAfter -= amountToConsume
			}
		} else {
			// the transferable commitment is fully consumed
			overallTransferredAmount += tc.Amount
			t.transferredCommitmentIDs[tc.ProjectID][tc.ID] = commitmentTransferLeftover{}

			if tc.Status == liquid.CommitmentStatusConfirmed {
				auditResource.TotalConfirmedAfter -= tc.Amount
			}
		}

		// retain ccr for audit event
		t.ccrs[tc.UUID] = liquid.CommitmentChangeRequest{
			AZ:          t.loc.AvailabilityZone,
			InfoVersion: t.serviceInfo.Version,
			ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
				project.UUID: {
					ProjectMetadata: Some(core.KeystoneProjectFromDB(project, core.KeystoneDomain{UUID: domain.UUID, Name: domain.Name}).ForLiquid()),
					ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
						t.loc.ResourceName: auditResource,
					},
				},
			},
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
			return err
		}
		tc.SupersedeContextJSON = Some(json.RawMessage(buf))
		_, err = t.dbi.Update(&tc)
		if err != nil {
			return err
		}
	}
	return nil
}

// ConfirmTransferableCommitmentIfExists should be used between calls to CheckAndConsume
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

func (t *TransferableCommitmentCache) getTransferredCommitmentsForProject(projectID db.ProjectID) map[db.ProjectCommitmentID]commitmentTransferLeftover {
	return t.transferredCommitmentIDs[projectID]
}

// GenerateAuditEventsAndMails generates the audit events and mail notifications
// for all transferred commitments that were processed via CheckAndConsume.
func (t *TransferableCommitmentCache) GenerateAuditEventsAndMails(apiIdentity core.ResourceRef, auditContext audit.Context) (auditEvents []audittools.Event, err error) {
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

		// gather the audit events and mail notifications for this project
		var (
			n           core.CommitmentGroupNotification
			domainUUID  string
			projectUUID liquid.ProjectUUID
		)
		err = t.dbi.QueryRow("SELECT d.uuid, d.name, p.uuid, p.name FROM domains d JOIN projects p ON d.id = p.domain_id where p.id = $1", projectID).Scan(&domainUUID, &n.DomainName, &projectUUID, &n.ProjectName)
		if err != nil {
			return auditEvents, err
		}

		commitmentsByID, err := db.BuildIndexOfDBResult(t.dbi, func(c db.ProjectCommitment) db.ProjectCommitmentID { return c.ID }, `SELECT * FROM project_commitments WHERE id = ANY($1)`, pq.Array(slices.Collect(maps.Keys(notifiableTransfers))))
		if err != nil {
			return auditEvents, err
		}

		n.Commitments = make([]core.CommitmentNotification, 0, len(notifiableTransfers))
		for _, cID := range slices.Sorted(maps.Keys(notifiableTransfers)) {
			leftover := notifiableTransfers[cID]
			c, exists := commitmentsByID[cID]
			if !exists {
				return auditEvents, fmt.Errorf("tried to generate mail notification for non-existent commitment ID %d", cID)
			}
			confirmedAt := c.ConfirmedAt.UnwrapOr(time.Unix(0, 0)) // the UnwrapOr() is defense in depth, it should never be relevant because we only notify for confirmed commitments here
			n.Commitments = append(n.Commitments, core.CommitmentNotification{
				Commitment: c,
				DateString: confirmedAt.Format(time.DateOnly),
				Resource: core.AZResourceLocation{
					ServiceType:      db.ServiceType(apiIdentity.ServiceType),
					ResourceName:     liquid.ResourceName(apiIdentity.Name),
					AvailabilityZone: t.loc.AvailabilityZone,
				},
				LeftoverAmount: leftover.Amount,
			})

			// push one transfer audit event per commitment, because they belong to separate CCRs
			auditEvents = append(auditEvents, audit.CommitmentEventTarget{
				CommitmentChangeRequest:      t.ccrs[c.UUID],
				CommitmentAttributeChangeset: map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset{c.UUID: t.cacs[c.UUID]},
			}.ReplicateForAllProjects(audittools.Event{
				Time:       t.now,
				Request:    auditContext.Request,
				User:       auditContext.UserIdentity,
				ReasonCode: http.StatusOK,
				Action:     ConsumeAction,
			})...)
		}
		if tpl, exists := t.mailTemplate.Unpack(); len(n.Commitments) != 0 && exists {
			// push mail notifications
			mail, err := tpl.Render(n, projectID, t.now)
			if err != nil {
				return auditEvents, err
			}
			err = t.dbi.Insert(&mail)
			if err != nil {
				return auditEvents, err
			}
		}
	}
	return auditEvents, nil
}
