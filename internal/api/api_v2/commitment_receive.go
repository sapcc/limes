// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/respondwith"
	. "go.xyrillian.de/gg/option"

	"github.com/gorilla/mux"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"go.xyrillian.de/gg/gsql"

	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/audit"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
)

func (p *v2Provider) handleReceiveCommitment(r *http.Request, token *gopherpolicy.Token) (result resourcesv2.Commitment, _ error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/commitments/:commitment_uuid/receive")
	var (
		none resourcesv2.Commitment
		ctx  = r.Context()
		sis  = p.Cluster.SIC.GetSnapshot()
		now  = p.timeNow()
		ccr  liquid.CommitmentChangeRequest
		cacs = make(map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset)
	)

	// validate request contents
	req, err := parseRequestBodyAs[resourcesv2.CommitmentReceiveRequest](r)
	if err != nil {
		return none, err
	}
	err = p.DB.WithinTransaction(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead}, func(tx *gsql.Tx) error {
		cUUID := liquid.CommitmentUUID(mux.Vars(r)["commitment_uuid"])
		commitments, azRes, err := p.selectCommitmentsIfAlive(ctx, tx, sis, []liquid.CommitmentUUID{cUUID})
		if err != nil {
			return err
		}
		c := commitments[0]

		// do this validation here, so that the next error is more specific
		if c.TransferStatus == limesresources.CommitmentTransferStatusNone {
			return respondwith.CustomStatus(http.StatusBadRequest, errReceiveNotInTransfer)
		}

		// special logic: user can either have commitment_get on the source project (e.g. domain admin/ cluster admin on unlisted transfer)
		// OR commitment_get_public (project admin) and know the transfer_token
		// we can check commitment_get_public against the source project, because that rule should never require a scope.
		sourceScope, err := p.checkProjectAccessByID(ctx, token, c.ProjectID, "v2:project:commitment_get")
		if err != nil {
			sourceScope, err = p.checkProjectAccessByID(ctx, token, c.ProjectID, "v2:project:commitment_get_public")
			if err != nil {
				return err
			}
			if token, ok := c.TransferToken.Unpack(); !ok || req.TransferToken != token {
				return respondwith.CustomStatus(http.StatusUnauthorized, errReceiveTransferTokenNotMatching)
			}
		}
		// additionally, we need permission to create a commitment in the target project
		targetScope, err := p.checkProjectAccess(ctx, token, req.TargetProjectUUID, "v2:project:commitment_create")
		if err != nil {
			return err
		}

		// more checks
		if sourceScope.Project.ID == targetScope.Project.ID {
			return respondwith.CustomStatus(http.StatusBadRequest, errReceiveSourceTargetEqual)
		}
		receivedAmount, amountIsSpecified := req.Amount.Unpack()
		if amountIsSpecified && receivedAmount > c.Amount {
			return respondwith.CustomStatus(http.StatusBadRequest, errReceiveAmountTooHigh)
		}
		if amountIsSpecified && receivedAmount == 0 {
			return respondwith.CustomStatus(http.StatusBadRequest, errReceiveAmountTooLow)
		}
		needsSplit := amountIsSpecified && receivedAmount < c.Amount
		if !needsSplit {
			receivedAmount = c.Amount
		}
		_, _, err = p.validateCommittability(azRes.Path, targetScope, c.Duration, sis)
		if err != nil {
			return err
		}

		// setup new commitments (1 if fully transferred, 2 if partially transferred)
		receiveContext := db.CommitmentWorkflowContext{
			Reason:                 db.CommitmentReasonReceive,
			RelatedCommitmentIDs:   []db.ProjectCommitmentID{c.ID},
			RelatedCommitmentUUIDs: []liquid.CommitmentUUID{c.UUID},
		}
		buf, err := json.Marshal(receiveContext)
		if err != nil {
			return err
		}
		receivedCommitment := db.ProjectCommitment{
			UUID:                  datamodel.GenerateProjectCommitmentUUID(),
			ProjectID:             targetScope.Project.ID,
			AZResourceID:          azRes.ID,
			Amount:                receivedAmount,
			Duration:              c.Duration,
			CreatedAt:             c.CreatedAt, // we keep the original creation time, to enable isDeletable check
			UpdatedAt:             now,
			CreatorUUID:           token.UserUUID(),
			CreatorName:           fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
			ConfirmBy:             c.ConfirmBy,
			ConfirmedAt:           c.ConfirmedAt,
			ExpiresAt:             c.ExpiresAt,
			CreationContextJSON:   buf,
			Status:                c.Status,
			NotifyOnConfirm:       c.NotifyOnConfirm,
			NotifiedForExpiration: false, // we reset this, so that in the new project, a new notification would happen
		}
		receivedCommitmentForCCR := liquid.Commitment{
			UUID:      receivedCommitment.UUID,
			NewStatus: Some(receivedCommitment.Status),
			Amount:    receivedCommitment.Amount,
			ConfirmBy: receivedCommitment.ConfirmBy,
			ExpiresAt: receivedCommitment.ExpiresAt,
		}
		sourceCommitmentsForCCR := []liquid.Commitment{{
			UUID:      c.UUID,
			OldStatus: Some(c.Status),
			NewStatus: Some(liquid.CommitmentStatusSuperseded),
			Amount:    c.Amount,
			ConfirmBy: c.ConfirmBy,
			ExpiresAt: c.ExpiresAt,
		}}
		cacs[c.UUID] = audit.CommitmentAttributeChangeset{
			OldTransferStatus: c.TransferStatus,
			NewTransferStatus: limesresources.CommitmentTransferStatusNone,
		}
		var splitCommitment db.ProjectCommitment
		if needsSplit {
			// leftover
			splitContext := db.CommitmentWorkflowContext{
				Reason:                 db.CommitmentReasonSplit,
				RelatedCommitmentIDs:   []db.ProjectCommitmentID{c.ID},
				RelatedCommitmentUUIDs: []liquid.CommitmentUUID{c.UUID},
			}
			buf, err = json.Marshal(splitContext)
			if err != nil {
				return err
			}
			splitCommitment = db.ProjectCommitment{
				UUID:                  datamodel.GenerateProjectCommitmentUUID(),
				ProjectID:             c.ProjectID,
				AZResourceID:          azRes.ID,
				Amount:                c.Amount - receivedAmount,
				Duration:              c.Duration,
				CreatedAt:             now,
				UpdatedAt:             now,
				CreatorUUID:           token.UserUUID(),
				CreatorName:           fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
				ConfirmBy:             c.ConfirmBy,
				ConfirmedAt:           c.ConfirmedAt,
				ExpiresAt:             c.ExpiresAt,
				CreationContextJSON:   buf,
				Status:                c.Status,
				NotifyOnConfirm:       c.NotifyOnConfirm,
				NotifiedForExpiration: c.NotifiedForExpiration,
				TransferStatus:        c.TransferStatus,
				TransferToken:         Some(datamodel.GenerateTransferToken()),
				TransferStartedAt:     Some(now),
			}
			sourceCommitmentsForCCR = append(sourceCommitmentsForCCR, liquid.Commitment{
				UUID:      splitCommitment.UUID,
				NewStatus: Some(splitCommitment.Status),
				Amount:    splitCommitment.Amount,
				ConfirmBy: splitCommitment.ConfirmBy,
				ExpiresAt: splitCommitment.ExpiresAt,
			})
			cacs[splitCommitment.UUID] = audit.CommitmentAttributeChangeset{
				OldTransferStatus: limesresources.CommitmentTransferStatusNone,
				NewTransferStatus: splitCommitment.TransferStatus,
			}
		}

		// inform liquid
		sourceStats, err := getCommitmentStats(p.DB, sourceScope.Project.ID, c.AZResourceID)
		if err != nil {
			return err
		}
		targetStats, err := getCommitmentStats(p.DB, targetScope.Project.ID, c.AZResourceID)
		if err != nil {
			return err
		}
		confirmedChange := uint64(0)
		if c.Status == liquid.CommitmentStatusConfirmed {
			confirmedChange = receivedAmount
		}
		ccr = liquid.CommitmentChangeRequest{
			AZ:          azRes.Path.AvailabilityZone,
			InfoVersion: must.BeOK(sis.GetServiceForType(azRes.Path.ServiceType)).LiquidVersion,
			ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
				// source
				sourceScope.Project.UUID: {
					ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(sourceScope.Project, sourceScope.Domain),
					ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
						azRes.Path.ResourceName: {
							TotalConfirmedBefore:  sourceStats.TotalConfirmed,
							TotalConfirmedAfter:   sourceStats.TotalConfirmed - confirmedChange,
							TotalGuaranteedBefore: 0,
							TotalGuaranteedAfter:  0,
							Commitments:           sourceCommitmentsForCCR,
						},
					},
				},
				// target
				targetScope.Project.UUID: {
					ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(targetScope.Project, targetScope.Domain),
					ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
						azRes.Path.ResourceName: {
							TotalConfirmedBefore:  targetStats.TotalConfirmed,
							TotalConfirmedAfter:   targetStats.TotalConfirmed + confirmedChange,
							TotalGuaranteedBefore: 0,
							TotalGuaranteedAfter:  0,
							Commitments:           []liquid.Commitment{receivedCommitmentForCCR},
						},
					},
				},
			},
		}
		resp, err := datamodel.DelegateChangeCommitments(ctx, p.Cluster, ccr, sis, azRes.Path.ServiceType, tx)
		if err != nil {
			return err
		}
		if ccr.RequiresConfirmation() {
			err = analyzeCommitmentChangeResponse(resp)
			if err != nil {
				return err
			}
		}

		// Supersede the source commitment first, clearing transfer fields.
		// This must happen before inserting the split commitment, because the
		// split commitment inherits the source's transfer_token and the UNIQUE
		// constraint on transfer_token would otherwise be violated.
		c.Status = liquid.CommitmentStatusSuperseded
		c.SupersededAt = Some(now)
		c.UpdatedAt = now
		c.TransferStatus = limesresources.CommitmentTransferStatusNone
		c.TransferStartedAt = None[time.Time]()
		c.TransferToken = None[string]()
		err = db.ProjectCommitmentStore.Update(ctx, tx, c)
		if err != nil {
			return err
		}

		// do inserts and then extract the related IDs for the supersede context
		err = db.ProjectCommitmentStore.Insert(ctx, tx, &receivedCommitment)
		if err != nil {
			return err
		}
		supersedeCommitmentIDs := []db.ProjectCommitmentID{receivedCommitment.ID}
		supersedeCommitmentUUIDs := []liquid.CommitmentUUID{receivedCommitment.UUID}
		if needsSplit {
			err = db.ProjectCommitmentStore.Insert(ctx, tx, &splitCommitment)
			if err != nil {
				return err
			}
			supersedeCommitmentIDs = append(supersedeCommitmentIDs, splitCommitment.ID)
			supersedeCommitmentUUIDs = append(supersedeCommitmentUUIDs, splitCommitment.UUID)
		}
		supersedeContext := db.CommitmentWorkflowContext{
			Reason:                 db.CommitmentReasonReceive,
			RelatedCommitmentIDs:   supersedeCommitmentIDs,
			RelatedCommitmentUUIDs: supersedeCommitmentUUIDs,
		}
		buf, err = json.Marshal(supersedeContext)
		if err != nil {
			return err
		}
		c.SupersedeContextJSON = Some(json.RawMessage(buf))
		err = db.ProjectCommitmentStore.Update(ctx, tx, c)
		if err != nil {
			return err
		}

		// result
		deletable := isDeletable(token, receivedCommitment, now)
		result = convertCommitmentToDisplayForm(receivedCommitment, azRes.Path, targetScope.Project.UUID, deletable)
		return nil
	})
	if err != nil {
		return none, err
	}

	// audit log
	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest:       ccr,
		CommitmentAttributeChangesets: cacs,
	}.ReplicateForAllProjectsWithDefaults(audittools.Event{
		Time:       now,
		Request:    r,
		User:       token,
		ReasonCode: http.StatusCreated,
		Action:     cadf.CreateAction,
	})
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}

	return
}
