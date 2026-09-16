// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/mux"
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

func (p *v2Provider) handleSplitCommitment(r *http.Request, token *gopherpolicy.Token) (result resourcesv2.CommitmentList, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/commitments/:commitment_uuid")
	var (
		none resourcesv2.CommitmentList
		ctx  = r.Context()
		sis  = p.Cluster.SIC.GetSnapshot()
		ccr  liquid.CommitmentChangeRequest
		now  = p.timeNow()
	)

	// validate request contents
	req, err := parseRequestBodyAs[resourcesv2.CommitmentSplitRequest](r)
	if err != nil {
		return none, err
	}
	if len(req.Amounts) < 2 {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errSplitInTwoOrMore)
	}

	err = p.DB.WithinTransaction(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead}, func(tx *gsql.Tx) error {
		cUUID := liquid.CommitmentUUID(mux.Vars(r)["commitment_uuid"])
		commitments, azRes, scope, err := p.selectCommitmentsIfPermittedAndAlive(ctx, tx, sis, token, "v2:project:commitment_create", []liquid.CommitmentUUID{cUUID})
		if err != nil {
			return err
		}
		c := commitments[0]
		deletable := isDeletable(token, c, now)

		// validate sum of amounts
		newSum := uint64(0)
		for _, amount := range req.Amounts {
			newSum += amount
		}
		if newSum != c.Amount {
			return respondwith.CustomStatus(http.StatusBadRequest, errAmountMismatch)
		}
		if c.TransferStatus != limesresources.CommitmentTransferStatusNone {
			return respondwith.CustomStatus(http.StatusBadRequest, errNoTransferSplit)
		}

		// prep modifications to commitments
		splitCommitments, err := buildSplitCommitments(c, req.Amounts, now)
		if err != nil {
			return err
		}
		splitLiquidCommitments := make([]liquid.Commitment, len(splitCommitments))
		for i, splitCommitment := range splitCommitments {
			splitLiquidCommitments[i] = liquid.Commitment{
				UUID:      splitCommitment.UUID,
				NewStatus: Some(splitCommitment.Status),
				Amount:    splitCommitment.Amount,
				ConfirmBy: splitCommitment.ConfirmBy,
				ExpiresAt: splitCommitment.ExpiresAt,
			}
		}

		// inform liquid
		stats, err := getCommitmentStats(p.DB, c.ProjectID, c.AZResourceID)
		if err != nil {
			return err
		}
		ccrCommitments := append([]liquid.Commitment{
			{
				UUID:      c.UUID,
				OldStatus: Some(c.Status),
				NewStatus: Some(liquid.CommitmentStatusSuperseded),
				Amount:    c.Amount,
				ConfirmBy: c.ConfirmBy,
				ExpiresAt: c.ExpiresAt.UTC(),
			},
		}, splitLiquidCommitments...)
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
							Commitments:           ccrCommitments,
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

		// do inserts and then extract the related IDs for the update
		err = db.ProjectCommitmentStore.Insert(ctx, tx, splitCommitments...)
		if err != nil {
			return err
		}
		splitCommitmentIDs := make([]db.ProjectCommitmentID, len(splitCommitments))
		splitCommitmentUUIDs := make([]liquid.CommitmentUUID, len(splitCommitments))
		for i, splitCommitment := range splitCommitments {
			splitCommitmentIDs[i] = splitCommitment.ID
			splitCommitmentUUIDs[i] = splitCommitment.UUID
			result.Commitments = append(result.Commitments, convertCommitmentToDisplayForm(*splitCommitment, azRes.Path, scope.Project.UUID, deletable))
		}
		supersedeContext := db.CommitmentWorkflowContext{
			Reason:                 db.CommitmentReasonSplit,
			RelatedCommitmentIDs:   splitCommitmentIDs,
			RelatedCommitmentUUIDs: splitCommitmentUUIDs,
		}
		buf, err := json.Marshal(supersedeContext)
		if err != nil {
			return err
		}
		c.SupersedeContextJSON = Some(json.RawMessage(buf))
		c.Status = liquid.CommitmentStatusSuperseded
		c.SupersededAt = Some(now)
		c.UpdatedAt = now
		err = db.ProjectCommitmentStore.Update(ctx, tx, c)
		if err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return none, err
	}

	// audit log
	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest: ccr,
	}.ReplicateForAllProjectsWithDefaults(audittools.Event{
		Time:       p.timeNow(),
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

// buildSplitCommitments prepares commitments from an existing one, whose creation contexts
// indicate that they were split from the given existing commitment.
func buildSplitCommitments(dbCommitment db.ProjectCommitment, amounts []uint64, now time.Time) ([]*db.ProjectCommitment, error) {
	creationContext := db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonSplit,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{dbCommitment.ID},
		RelatedCommitmentUUIDs: []liquid.CommitmentUUID{dbCommitment.UUID},
	}
	buf, err := json.Marshal(creationContext)
	if err != nil {
		return nil, err
	}

	result := make([]*db.ProjectCommitment, len(amounts))
	for i, amount := range amounts {
		result[i] = &db.ProjectCommitment{
			UUID:                datamodel.GenerateProjectCommitmentUUID(),
			ProjectID:           dbCommitment.ProjectID,
			AZResourceID:        dbCommitment.AZResourceID,
			Amount:              amount,
			Duration:            dbCommitment.Duration,
			CreatedAt:           now,
			UpdatedAt:           now,
			CreatorUUID:         dbCommitment.CreatorUUID,
			CreatorName:         dbCommitment.CreatorName,
			ConfirmBy:           dbCommitment.ConfirmBy,
			ConfirmedAt:         dbCommitment.ConfirmedAt,
			ExpiresAt:           dbCommitment.ExpiresAt,
			CreationContextJSON: json.RawMessage(buf),
			Status:              dbCommitment.Status,
		}
	}
	return result, nil
}
