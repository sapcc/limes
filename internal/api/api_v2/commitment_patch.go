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
	"github.com/sapcc/go-bits/sqlext"
	. "go.xyrillian.de/gg/option"

	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/audit"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
)

func (p *v2Provider) handlePatchCommitment(r *http.Request, token *gopherpolicy.Token) (resourcesv2.Commitment, error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/commitments/:commitment_uuid")
	var (
		none resourcesv2.Commitment
		ctx  = r.Context()
		sis  = p.Cluster.SIC.GetSnapshot()
	)

	// validate request contents
	req, err := parseRequestBodyAs[resourcesv2.CommitmentPatchRequest](r)
	if err != nil {
		return none, err
	}
	cUUID := mux.Vars(r)["commitment_uuid"]
	tx, err := p.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return none, err
	}
	defer sqlext.RollbackUnlessCommitted(tx)
	c, azRes, scope, err := p.selectCommitmentIfPermittedAndAlive(ctx, tx, sis, token, "v2:project:commitment_patch", liquid.CommitmentUUID(cUUID))
	if err != nil {
		return none, err
	}

	// prep the same commitment for early return
	deletable := isDeletable(token, c, p.timeNow)
	originalCommitment := convertCommitmentToDisplayForm(c, azRes.Path, scope.Project.UUID, deletable)

	// validate request and do according modifications
	var (
		now          = p.timeNow()
		oldExpiresAt = None[time.Time]()
		cac          audit.CommitmentAttributeChangeset
		behavior     = p.Cluster.CommitmentBehaviorForResourcePath(azRes.Path.Resource()).ForDomain(scope.Domain.Name)
	)
	if req.Duration.IsSome() && req.TransferStatus.IsSome() {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errOnlyOneCommitmentModification)
	}
	if req.Duration.IsNone() && req.TransferStatus.IsNone() {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoCommitmentModification)
	}
	if newTransferStatus, ok := req.TransferStatus.Unpack(); ok {
		if newTransferStatus == c.TransferStatus {
			return originalCommitment, nil
		}
		if !slices.Contains(commitmentTransferStatuses, newTransferStatus) {
			return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchTransferStatus)
		}
		cac = audit.CommitmentAttributeChangeset{
			OldTransferStatus: c.TransferStatus,
			NewTransferStatus: newTransferStatus,
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
			return originalCommitment, nil
		}
		if !slices.Contains(behavior.Durations, newDuration) {
			buf := must.Return(json.Marshal(behavior.Durations)) // panic on error is acceptable here, marshals should never fail
			msg := "unacceptable commitment duration for this resource; acceptable values: " + string(buf)
			return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New(msg))
		}
		newExpiresAt := newDuration.AddTo(c.ConfirmBy.UnwrapOr(c.CreatedAt))
		if newExpiresAt.Before(c.ExpiresAt) {
			return none, respondwith.CustomStatus(http.StatusBadRequest, errNoDurationShortening)
		}
		c.Duration = newDuration
		oldExpiresAt = Some(c.ExpiresAt)
		c.ExpiresAt = newExpiresAt
	}
	c.UpdatedAt = now

	// checking the response is only relevant for extending durations
	stats, err := getCommitmentStats(p.DB, c.ProjectID, c.AZResourceID)
	if err != nil {
		return none, err
	}
	ccr := liquid.CommitmentChangeRequest{
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
						TotalGuaranteedAfter:  stats.TotalGuaranteed, // TODO: change when introducing "guaranteed" commitments
						Commitments: []liquid.Commitment{
							{
								UUID:         c.UUID,
								OldStatus:    Some(c.Status),
								NewStatus:    Some(c.Status),
								Amount:       c.Amount,
								ConfirmBy:    c.ConfirmBy,
								ExpiresAt:    c.ExpiresAt,
								OldExpiresAt: oldExpiresAt,
							},
						},
					},
				},
			},
		},
	}
	resp, err := datamodel.DelegateChangeCommitments(ctx, p.Cluster, ccr, sis, azRes.Path.ServiceType, tx)
	if err != nil {
		return none, err
	}
	if ccr.RequiresConfirmation() {
		err = analyzeCommitmentChangeResponse(resp)
		if err != nil {
			return none, err
		}
	}

	// update
	err = db.ProjectCommitmentStore.Update(ctx, tx, c)
	if err != nil {
		return none, err
	}
	err = tx.Commit()
	if err != nil {
		return none, err
	}

	// audit log
	var cacs = make(map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset, 0)
	if req.TransferStatus.IsSome() {
		cacs[c.UUID] = cac
	}
	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest:       ccr,
		CommitmentAttributeChangesets: cacs,
	}.ReplicateForAllProjectsWithDefaults(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusAccepted,
		Action:     cadf.UpdateAction,
	})
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}

	updatedCommitment := convertCommitmentToDisplayForm(c, azRes.Path, scope.Project.UUID, deletable)
	return updatedCommitment, nil
}
