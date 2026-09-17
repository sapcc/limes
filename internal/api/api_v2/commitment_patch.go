// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"database/sql"
	"encoding/json"
	"errors"
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
	"go.xyrillian.de/gg/options"

	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/audit"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
)

func (p *v2Provider) handlePatchCommitment(r *http.Request, token *gopherpolicy.Token) (result resourcesv2.Commitment, _ error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/commitments/:commitment_uuid")
	var (
		none resourcesv2.Commitment
		ctx  = r.Context()
		sis  = p.Cluster.SIC.GetSnapshot()
		now  = p.timeNow()
		ccr  liquid.CommitmentChangeRequest
		cacs map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset
	)

	// validate request contents
	req, err := parseRequestBodyAs[resourcesv2.CommitmentPatchRequest](r)
	if err != nil {
		return none, err
	}
	err = p.DB.WithinTransaction(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead}, func(tx *gsql.Tx) error {
		cUUID := liquid.CommitmentUUID(mux.Vars(r)["commitment_uuid"])
		commitments, azRes, scope, err := p.selectCommitmentsIfPermittedAndAlive(ctx, tx, sis, token, "v2:project:commitment_update", []liquid.CommitmentUUID{cUUID})
		if err != nil {
			return err
		}
		c := commitments[0]

		// prep the same commitment for early return
		deletable := isDeletable(token, c, now)
		result = convertCommitmentToDisplayForm(c, azRes.Path, scope.Project.UUID, deletable)

		// validate request and do according modifications
		var (
			now          = now
			oldExpiresAt = None[time.Time]()
			behavior     = p.Cluster.CommitmentBehaviorForResourcePath(azRes.Path.Resource()).ForDomain(scope.Domain.Name)
		)
		if req.Duration.IsSome() && req.TransferStatus.IsSome() {
			return respondwith.CustomStatus(http.StatusBadRequest, errOnlyOneCommitmentModification)
		}
		if req.Duration.IsNone() && req.TransferStatus.IsNone() {
			return respondwith.CustomStatus(http.StatusBadRequest, errNoCommitmentModification)
		}
		if newTransferStatus, ok := req.TransferStatus.Unpack(); ok {
			if newTransferStatus == c.TransferStatus {
				return nil // skip work because there is nothing to do
			}
			if !slices.Contains(commitmentTransferStatuses, newTransferStatus) {
				return respondwith.CustomStatus(http.StatusBadRequest, errNoSuchTransferStatus)
			}
			cacs = map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset{
				c.UUID: {
					OldTransferStatus: c.TransferStatus,
					NewTransferStatus: newTransferStatus,
				},
			}
			c.TransferStatus = newTransferStatus
			if newTransferStatus == limesresources.CommitmentTransferStatusNone {
				c.TransferStartedAt = None[time.Time]()
				c.TransferToken = None[string]()
			} else {
				c.TransferStartedAt = Some(now)
				c.TransferToken = Some(datamodel.GenerateTransferToken())
			}
		}
		if newDuration, ok := req.Duration.Unpack(); ok {
			if newDuration == c.Duration {
				return nil // skip work because there is nothing to do
			}
			if !slices.Contains(behavior.Durations, newDuration) {
				buf := must.Return(json.Marshal(behavior.Durations)) // panic on error is acceptable here, marshals should never fail
				msg := "unacceptable commitment duration for this resource; acceptable values: " + string(buf)
				return respondwith.CustomStatus(http.StatusBadRequest, errors.New(msg))
			}
			newExpiresAt := newDuration.AddTo(c.ConfirmBy.UnwrapOr(c.CreatedAt))
			if newExpiresAt.Before(c.ExpiresAt) {
				return respondwith.CustomStatus(http.StatusBadRequest, errNoDurationShortening)
			}
			c.Duration = newDuration
			oldExpiresAt = Some(c.ExpiresAt)
			c.ExpiresAt = newExpiresAt
		}
		c.UpdatedAt = now

		// sending the patch to liquid is only relevant for extending durations
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
							TotalGuaranteedBefore: stats.TotalGuaranteed,
							TotalGuaranteedAfter:  stats.TotalGuaranteed,
							Commitments: []liquid.Commitment{
								{
									UUID:         c.UUID,
									OldStatus:    Some(c.Status),
									NewStatus:    Some(c.Status),
									Amount:       c.Amount,
									ConfirmBy:    c.ConfirmBy,
									ExpiresAt:    c.ExpiresAt.UTC(),
									OldExpiresAt: options.Map(oldExpiresAt, time.Time.UTC),
								},
							},
						},
					},
				},
			},
		}
		if req.Duration.IsSome() {
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
		}

		// update
		result = convertCommitmentToDisplayForm(c, azRes.Path, scope.Project.UUID, deletable)
		return db.ProjectCommitmentStore.Update(ctx, tx, c)
	})
	if err != nil {
		return none, err
	}

	// audit log (only if something was really changed)
	if len(ccr.ByProject) > 0 || len(cacs) > 0 {
		auditEvents := audit.CommitmentEventTarget{
			CommitmentChangeRequest:       ccr,
			CommitmentAttributeChangesets: cacs,
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
	}

	return result, nil
}
