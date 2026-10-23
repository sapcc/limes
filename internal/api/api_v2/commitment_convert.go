// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

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

func (p *v2Provider) handleConvertCommitment(r *http.Request, token *gopherpolicy.Token) (result resourcesv2.Commitment, _ error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/commitments/:commitment_uuid")
	var (
		none resourcesv2.Commitment
		ctx  = r.Context()
		sis  = p.Cluster.SIC.GetSnapshot()
		now  = p.timeNow()
		ccr  liquid.CommitmentChangeRequest
	)

	// validate request contents
	req, err := parseRequestBodyAs[resourcesv2.CommitmentConvertRequest](r)
	if err != nil {
		return none, err
	}
	err = p.DB.WithinTransaction(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead}, func(tx *gsql.Tx) error {
		cUUID := liquid.CommitmentUUID(mux.Vars(r)["commitment_uuid"])
		commitments, sourceAZRes, scope, err := p.selectCommitmentsIfPermittedAndAlive(ctx, tx, sis, token, "v2:project:commitment_update", []liquid.CommitmentUUID{cUUID})
		if err != nil {
			return err
		}
		c := commitments[0]
		sourceRes := must.BeOK(sis.GetResourceForPath(sourceAZRes.Path.Resource()))

		// checks on the commitment itself
		if c.TransferStatus != limesresources.CommitmentTransferStatusNone {
			return respondwith.CustomStatus(http.StatusUnprocessableEntity, errNoTransferConversion)
		}

		// service/ resource validations
		targetPath := db.AZResourcePath{ServiceType: req.TargetServiceType, ResourceName: req.TargetResourceName, AvailabilityZone: sourceAZRes.AvailabilityZone}
		targetAZRes, targetBehavior, err := p.validateCommittability(targetPath, scope, c.Duration, sis, "in target resource: ")
		if err != nil {
			return err
		}
		if targetAZRes.Path == sourceAZRes.Path {
			return respondwith.CustomStatus(http.StatusBadRequest, errConversionSameResource)
		}
		targetRes := must.BeOK(sis.GetResourceForPath(targetAZRes.Path.Resource()))
		sourceBehavior := p.Cluster.CommitmentBehaviorForResourcePath(sourceRes.Path).ForDomain(scope.Domain.Name)
		rate, exists := sourceBehavior.GetConversionRateTo(targetBehavior, sourceRes.Unit, targetRes.Unit).Unpack()
		if !exists {
			return respondwith.CustomStatus(http.StatusUnprocessableEntity, errNoSuchConversion)
		}

		// number validations
		if req.SourceAmount == 0 {
			return respondwith.CustomStatus(http.StatusBadRequest, errEmptyAmount)
		}
		if req.SourceAmount > c.Amount {
			return respondwith.CustomStatus(http.StatusBadRequest, errConversionAmountTooHigh)
		}
		remainderAmount := req.SourceAmount % rate.FromAmount
		if remainderAmount > 0 && !rate.AllowRounding {
			return respondwith.CustomStatus(http.StatusBadRequest, errConversionNoRounding)
		}
		conversionAmount := (req.SourceAmount * rate.ToAmount) / rate.FromAmount
		if conversionAmount == 0 {
			return respondwith.CustomStatus(http.StatusUnprocessableEntity, errConversionTargetZero)
		}
		if conversionAmount != req.TargetAmount {
			return respondwith.CustomStatus(http.StatusUnprocessableEntity, errConversionTargetMismatch)
		}
		remainingAmount := c.Amount - req.SourceAmount

		// prep changes and ccr
		sourceCommitmentsForCCR := []liquid.Commitment{{
			UUID:      c.UUID,
			OldStatus: Some(c.Status),
			NewStatus: Some(liquid.CommitmentStatusSuperseded),
			Amount:    c.Amount,
			ConfirmBy: c.ConfirmBy,
			ExpiresAt: c.ExpiresAt,
		}}
		var leftoverCommitment db.ProjectCommitment
		if remainingAmount > 0 {
			splitContext := db.CommitmentWorkflowContext{
				Reason:                 db.CommitmentReasonSplit,
				RelatedCommitmentIDs:   []db.ProjectCommitmentID{c.ID},
				RelatedCommitmentUUIDs: []liquid.CommitmentUUID{c.UUID},
			}
			buf, err := json.Marshal(splitContext)
			if err != nil {
				return err
			}
			leftoverCommitment = db.ProjectCommitment{
				UUID:                datamodel.GenerateProjectCommitmentUUID(),
				ProjectID:           c.ProjectID,
				AZResourceID:        c.AZResourceID,
				Amount:              remainingAmount,
				Duration:            c.Duration,
				CreatedAt:           now,
				UpdatedAt:           now,
				CreatorUUID:         c.CreatorUUID,
				CreatorName:         c.CreatorName,
				ConfirmBy:           c.ConfirmBy,
				ConfirmedAt:         c.ConfirmedAt,
				ExpiresAt:           c.ExpiresAt,
				CreationContextJSON: buf,
				Status:              c.Status,
				NotifyOnConfirm:     c.NotifyOnConfirm,
			}
			sourceCommitmentsForCCR = append(sourceCommitmentsForCCR, liquid.Commitment{
				UUID:      leftoverCommitment.UUID,
				NewStatus: Some(leftoverCommitment.Status),
				Amount:    leftoverCommitment.Amount,
				ConfirmBy: leftoverCommitment.ConfirmBy,
				ExpiresAt: leftoverCommitment.ExpiresAt,
			})
		}
		convertContext := db.CommitmentWorkflowContext{
			Reason:                 db.CommitmentReasonConvert,
			RelatedCommitmentIDs:   []db.ProjectCommitmentID{c.ID},
			RelatedCommitmentUUIDs: []liquid.CommitmentUUID{c.UUID},
		}
		buf, err := json.Marshal(convertContext)
		if err != nil {
			return err
		}
		convertedCommitment := db.ProjectCommitment{
			UUID:                datamodel.GenerateProjectCommitmentUUID(),
			ProjectID:           c.ProjectID,
			AZResourceID:        targetAZRes.ID,
			Amount:              conversionAmount,
			Duration:            c.Duration,
			CreatedAt:           now,
			UpdatedAt:           now,
			CreatorUUID:         token.UserUUID(),
			CreatorName:         fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
			ConfirmBy:           c.ConfirmBy,
			ConfirmedAt:         c.ConfirmedAt,
			ExpiresAt:           c.ExpiresAt,
			CreationContextJSON: buf,
			Status:              c.Status,
			NotifyOnConfirm:     c.NotifyOnConfirm,
		}
		convertedCommitmentForCCR := liquid.Commitment{
			UUID:      convertedCommitment.UUID,
			NewStatus: Some(convertedCommitment.Status),
			Amount:    convertedCommitment.Amount,
			ConfirmBy: convertedCommitment.ConfirmBy,
			ExpiresAt: convertedCommitment.ExpiresAt,
		}

		// inform liquid
		sourceStats, err := getCommitmentStats(tx, scope.Project.ID, sourceAZRes.ID)
		if err != nil {
			return err
		}
		targetStats, err := getCommitmentStats(tx, scope.Project.ID, targetAZRes.ID)
		if err != nil {
			return err
		}
		var (
			confirmedChangeBeforeConversion = uint64(0)
			confirmedChangeAfterConversion  = uint64(0)
		)
		if c.Status == liquid.CommitmentStatusConfirmed {
			confirmedChangeBeforeConversion = req.SourceAmount
			confirmedChangeAfterConversion = req.TargetAmount
		}
		ccr = liquid.CommitmentChangeRequest{
			AZ:          sourceAZRes.Path.AvailabilityZone,
			InfoVersion: must.BeOK(sis.GetServiceForType(sourceAZRes.Path.ServiceType)).LiquidVersion,
			ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
				scope.Project.UUID: {
					ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(scope.Project, scope.Domain),
					ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
						// source
						sourceAZRes.Path.ResourceName: {
							TotalConfirmedBefore:  sourceStats.TotalConfirmed,
							TotalConfirmedAfter:   sourceStats.TotalConfirmed - confirmedChangeBeforeConversion,
							TotalGuaranteedBefore: 0,
							TotalGuaranteedAfter:  0,
							Commitments:           sourceCommitmentsForCCR,
						},
						// target
						targetAZRes.Path.ResourceName: {
							TotalConfirmedBefore:  targetStats.TotalConfirmed,
							TotalConfirmedAfter:   targetStats.TotalConfirmed + confirmedChangeAfterConversion,
							TotalGuaranteedBefore: 0,
							TotalGuaranteedAfter:  0,
							Commitments:           []liquid.Commitment{convertedCommitmentForCCR},
						},
					},
				},
			},
		}
		resp, err := datamodel.DelegateChangeCommitments(ctx, p.Cluster, ccr, sis, sourceAZRes.Path.ServiceType, tx)
		if err != nil {
			return err
		}
		if ccr.RequiresConfirmation() {
			err = analyzeCommitmentChangeResponse(resp)
			if err != nil {
				return err
			}
		}

		// Insert the new commitments first, so that we can use their IDs for the context.
		err = db.ProjectCommitmentStore.Insert(ctx, tx, &convertedCommitment)
		if err != nil {
			return err
		}
		supersedeCommitmentIDs := []db.ProjectCommitmentID{convertedCommitment.ID}
		supersedeCommitmentUUIDs := []liquid.CommitmentUUID{convertedCommitment.UUID}
		deletable := isDeletable(token, convertedCommitment, now)
		result = convertCommitmentToDisplayForm(convertedCommitment, targetAZRes.Path, scope.Project.UUID, deletable)
		if remainingAmount > 0 {
			err = db.ProjectCommitmentStore.Insert(ctx, tx, &leftoverCommitment)
			if err != nil {
				return err
			}
			supersedeCommitmentIDs = append(supersedeCommitmentIDs, leftoverCommitment.ID)
			supersedeCommitmentUUIDs = append(supersedeCommitmentUUIDs, leftoverCommitment.UUID)
		}

		c.Status = liquid.CommitmentStatusSuperseded
		c.SupersededAt = Some(now)
		c.UpdatedAt = now
		supersedeContext := db.CommitmentWorkflowContext{
			Reason:                 db.CommitmentReasonConvert,
			RelatedCommitmentIDs:   supersedeCommitmentIDs,
			RelatedCommitmentUUIDs: supersedeCommitmentUUIDs,
		}
		buf, err = json.Marshal(supersedeContext)
		if err != nil {
			return err
		}
		c.SupersedeContextJSON = Some(json.RawMessage(buf))
		return db.ProjectCommitmentStore.Update(ctx, tx, c)
	})
	if err != nil {
		return none, err
	}

	// audit log (only if something was really changed)

	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest: ccr,
	}.ReplicateForAllProjectsWithDefaults(audittools.Event{
		Time:       now,
		Request:    r,
		User:       token,
		ReasonCode: http.StatusAccepted,
		Action:     cadf.UpdateAction,
	})
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}

	return result, nil
}
