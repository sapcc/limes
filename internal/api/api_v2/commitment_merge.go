// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/sapcc/go-api-declarations/cadf"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/respondwith"
	"go.xyrillian.de/gg/gsql"
	. "go.xyrillian.de/gg/option"

	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/audit"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
)

func (p *v2Provider) handleMergeCommitments(r *http.Request, token *gopherpolicy.Token) (result resourcesv2.Commitment, _ error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/commitments/merge")
	var (
		none resourcesv2.Commitment
		ctx  = r.Context()
		sis  = p.Cluster.SIC.GetSnapshot()
		ccr  liquid.CommitmentChangeRequest
		now  = p.timeNow()
	)

	// validate request contents
	req, err := parseRequestBodyAs[resourcesv2.CommitmentMergeRequest](r)
	if err != nil {
		return none, err
	}
	if len(req.CommitmentUUIDs) < 2 {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errMergeInTwoOrMore)
	}

	err = p.DB.WithinTransaction(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead}, func(tx *gsql.Tx) error {
		commitments, azRes, scope, err := p.selectCommitmentsIfPermittedAndAlive(ctx, tx, sis, token, "v2:project:commitment_update", req.CommitmentUUIDs)
		if err != nil {
			return err
		}

		// in this loop we do 3 things to avoid unnecessary iterations:
		// * validate status of mergeable commitments
		// * build merged commitment: sum amounts, use latest expiresAt and its duration
		// * collect IDs for context
		commitmentIDs := make([]db.ProjectCommitmentID, len(commitments))
		commitmentUUIDs := make([]liquid.CommitmentUUID, len(commitments))
		commitmentsForCCR := make([]liquid.Commitment, len(commitments)+1)
		var (
			totalAmount    uint64
			latestDuration = commitments[0].Duration
			latestExpiry   = commitments[0].ExpiresAt
		)
		for i, c := range commitments {
			if c.Status != liquid.CommitmentStatusConfirmed {
				return respondwith.CustomStatus(http.StatusConflict, errOnlyConfirmedMergeable)
			}
			if c.TransferStatus != limesresources.CommitmentTransferStatusNone {
				return respondwith.CustomStatus(http.StatusBadRequest, errNoTransferMerge)
			}

			commitmentIDs[i] = c.ID
			commitmentUUIDs[i] = c.UUID
			totalAmount += c.Amount
			commitmentsForCCR[i] = liquid.Commitment{
				UUID:      c.UUID,
				OldStatus: Some(liquid.CommitmentStatusConfirmed),
				NewStatus: Some(liquid.CommitmentStatusSuperseded),
				Amount:    c.Amount,
				ConfirmBy: c.ConfirmBy,
				ExpiresAt: c.ExpiresAt,
			}
			if c.ExpiresAt.After(latestExpiry) {
				latestExpiry = c.ExpiresAt
				latestDuration = c.Duration
			}
		}

		creationContext := db.CommitmentWorkflowContext{
			Reason:                 db.CommitmentReasonMerge,
			RelatedCommitmentIDs:   commitmentIDs,
			RelatedCommitmentUUIDs: commitmentUUIDs,
		}
		creationBuf, err := json.Marshal(creationContext)
		if err != nil {
			return err
		}
		mergedCommitment := db.ProjectCommitment{
			UUID:                datamodel.GenerateProjectCommitmentUUID(),
			ProjectID:           commitments[0].ProjectID,
			AZResourceID:        commitments[0].AZResourceID,
			Amount:              totalAmount,
			Duration:            latestDuration,
			CreatedAt:           now,
			UpdatedAt:           now,
			CreatorUUID:         token.UserUUID(),
			CreatorName:         fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
			ConfirmedAt:         Some(now),
			ExpiresAt:           latestExpiry,
			Status:              liquid.CommitmentStatusConfirmed,
			CreationContextJSON: json.RawMessage(creationBuf),
		}

		// inform liquid
		commitmentsForCCR[len(commitmentsForCCR)-1] = liquid.Commitment{
			UUID:      mergedCommitment.UUID,
			NewStatus: Some(liquid.CommitmentStatusConfirmed),
			Amount:    mergedCommitment.Amount,
			ConfirmBy: mergedCommitment.ConfirmBy,
			ExpiresAt: mergedCommitment.ExpiresAt,
		}
		stats, err := getCommitmentStats(p.DB, commitments[0].ProjectID, commitments[0].AZResourceID)
		if err != nil {
			return err
		}
		ccr = liquid.CommitmentChangeRequest{
			AZ:          azRes.Path.AvailabilityZone,
			InfoVersion: must.BeOK(sis.GetServiceForType(azRes.Path.ServiceType)).LiquidVersion,
			ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
				scope.Project.UUID: {
					ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(scope.Project, scope.Domain),
					ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
						azRes.Path.ResourceName: {
							TotalConfirmedBefore:  stats.TotalConfirmed,
							TotalConfirmedAfter:   stats.TotalConfirmed,
							TotalGuaranteedBefore: stats.TotalGuaranteed,
							TotalGuaranteedAfter:  stats.TotalGuaranteed,
							Commitments:           commitmentsForCCR,
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

		// do insert and then extract the related IDs for the updates
		err = db.ProjectCommitmentStore.Insert(ctx, tx, &mergedCommitment)
		if err != nil {
			return err
		}
		supersedeContext := db.CommitmentWorkflowContext{
			Reason:                 db.CommitmentReasonMerge,
			RelatedCommitmentIDs:   []db.ProjectCommitmentID{mergedCommitment.ID},
			RelatedCommitmentUUIDs: []liquid.CommitmentUUID{mergedCommitment.UUID},
		}
		supersedeBuf, err := json.Marshal(supersedeContext)
		if err != nil {
			return err
		}
		for i := range commitments {
			commitments[i].Status = liquid.CommitmentStatusSuperseded
			commitments[i].SupersededAt = Some(now)
			commitments[i].SupersedeContextJSON = Some(json.RawMessage(supersedeBuf))
			commitments[i].UpdatedAt = now
		}
		err = db.ProjectCommitmentStore.Update(ctx, tx, commitments...)
		if err != nil {
			return err
		}

		// assemble result
		deletable := isDeletable(token, mergedCommitment, now)
		result = convertCommitmentToDisplayForm(mergedCommitment, azRes.Path, scope.Project.UUID, deletable)
		return nil
	})
	if err != nil {
		return none, err
	}

	// audit log
	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest: ccr,
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

	return result, nil
}
