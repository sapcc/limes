// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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

// As per the API spec, commitments can be renewed 90 days in advance at the earliest.
const commitmentRenewalPeriod = 90 * 24 * time.Hour

func (p *v2Provider) handleRenewCommitment(r *http.Request, token *gopherpolicy.Token) (result resourcesv2.Commitment, _ error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/commitments/:commitment_uuid")
	var (
		none resourcesv2.Commitment
		ctx  = r.Context()
		sis  = p.Cluster.SIC.GetSnapshot()
		ccr  liquid.CommitmentChangeRequest
		now  = p.timeNow()
	)

	// validate request contents
	req, err := parseRequestBodyAs[resourcesv2.CommitmentRenewRequest](r)
	if err != nil {
		return none, err
	}
	err = p.DB.WithinTransaction(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead}, func(tx *gsql.Tx) error {
		cUUID := liquid.CommitmentUUID(mux.Vars(r)["commitment_uuid"])
		commitments, azRes, scope, err := p.selectCommitmentsIfPermittedAndAlive(ctx, tx, sis, token, "v2:project:commitment_create", []liquid.CommitmentUUID{cUUID})
		if err != nil {
			return err
		}
		c := commitments[0]

		// validate request semantics
		behavior := p.Cluster.CommitmentBehaviorForResourcePath(azRes.Path.Resource()).ForDomain(scope.Domain.Name)

		if !slices.Contains(behavior.Durations, req.Duration) {
			buf := must.Return(json.Marshal(behavior.Durations)) // panic on error is acceptable here, marshals should never fail
			msg := "unacceptable commitment duration for this resource; acceptable values: " + string(buf)
			return respondwith.CustomStatus(http.StatusBadRequest, errors.New(msg))
		}
		if c.Status != liquid.CommitmentStatusConfirmed {
			return respondwith.CustomStatus(http.StatusBadRequest, errRenewalStatusMustBeConfirmed)
		}
		if c.TransferStatus != limesresources.CommitmentTransferStatusNone {
			return respondwith.CustomStatus(http.StatusBadRequest, errRenewalInTransferNotAllowed)
		}
		if now.After(c.ExpiresAt) {
			// this is defense in depth: The status will be updated shortly after expiry, but may take some time
			return respondwith.CustomStatus(http.StatusBadRequest, errRenewalMustNotBeExpired)
		}
		if now.Before(c.ExpiresAt.Add(-commitmentRenewalPeriod)) {
			return respondwith.CustomStatus(http.StatusBadRequest, errRenewalMustNotBeEarly)
		}
		if c.RenewContextJSON.IsSome() {
			return respondwith.CustomStatus(http.StatusBadRequest, errRenewalAlreadyDone)
		}

		// assemble new commitment
		creationContext := db.CommitmentWorkflowContext{
			Reason:                 db.CommitmentReasonRenew,
			RelatedCommitmentIDs:   []db.ProjectCommitmentID{c.ID},
			RelatedCommitmentUUIDs: []liquid.CommitmentUUID{c.UUID},
		}
		buf, err := json.Marshal(creationContext)
		if err != nil {
			return err
		}
		renewedCommitment := db.ProjectCommitment{
			UUID:                datamodel.GenerateProjectCommitmentUUID(),
			ProjectID:           c.ProjectID,
			AZResourceID:        c.AZResourceID,
			Amount:              c.Amount,
			Duration:            req.Duration,
			CreatedAt:           now,
			UpdatedAt:           now,
			CreatorUUID:         token.UserUUID(),
			CreatorName:         fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
			ConfirmBy:           Some(c.ExpiresAt),
			ExpiresAt:           req.Duration.AddTo(c.ExpiresAt),
			CreationContextJSON: buf,
			Status:              liquid.CommitmentStatusPlanned,
			NotifyOnConfirm:     req.NotifyOnConfirm,
		}

		// inform liquid
		stats, err := getCommitmentStats(p.DB, c.ProjectID, c.AZResourceID)
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
							TotalGuaranteedBefore: 0,
							TotalGuaranteedAfter:  0,
							Commitments: []liquid.Commitment{
								{
									UUID:      renewedCommitment.UUID,
									NewStatus: Some(renewedCommitment.Status),
									Amount:    renewedCommitment.Amount,
									ConfirmBy: renewedCommitment.ConfirmBy,
									ExpiresAt: renewedCommitment.ExpiresAt,
								},
							},
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

		// insert new one and update old one
		err = db.ProjectCommitmentStore.Insert(ctx, tx, &renewedCommitment)
		if err != nil {
			return err
		}
		renewContext := db.CommitmentWorkflowContext{
			Reason:                 db.CommitmentReasonRenew,
			RelatedCommitmentIDs:   []db.ProjectCommitmentID{renewedCommitment.ID},
			RelatedCommitmentUUIDs: []liquid.CommitmentUUID{renewedCommitment.UUID},
		}
		buf, err = json.Marshal(renewContext)
		if err != nil {
			return err
		}
		c.UpdatedAt = now
		c.RenewContextJSON = Some(json.RawMessage(buf))
		err = db.ProjectCommitmentStore.Update(ctx, tx, c)
		if err != nil {
			return err
		}

		// result
		deletable := isDeletable(token, renewedCommitment, now)
		result = convertCommitmentToDisplayForm(renewedCommitment, azRes.Path, scope.Project.UUID, deletable)
		return db.ProjectCommitmentStore.Update(ctx, tx, c)
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
		ReasonCode: http.StatusAccepted,
		Action:     cadf.CreateAction,
	})
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}

	return result, nil
}
