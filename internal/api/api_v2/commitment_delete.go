// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-api-declarations/cadf"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/sqlext"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"

	"github.com/sapcc/limes/internal/audit"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

func (p *v2Provider) handleDeleteCommitment(r *http.Request, token *gopherpolicy.Token) (any, error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/commitments/:commitment_uuid")
	var (
		ctx = r.Context()
		sis = p.Cluster.SIC.GetSnapshot()
	)

	// validate request contents
	cUUID := mux.Vars(r)["commitment_uuid"]
	tx, err := p.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer sqlext.RollbackUnlessCommitted(tx)
	c, azRes, scope, err := p.selectCommitmentIfPermittedAndAlive(ctx, tx, sis, token, "v2:project:commitment_delete", liquid.CommitmentUUID(cUUID))
	switch {
	case errors.Is(err, errNoSuchCommitment):
		return nil, nil // respond with 204 if commitment already deleted (DELETE should be idempotent)
	case err != nil:
		return nil, err
	}

	// prep deletion
	stats, err := getCommitmentStats(p.DB, c.ProjectID, c.AZResourceID)
	if err != nil {
		return nil, err
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
						TotalConfirmedAfter:   stats.TotalConfirmed - c.Amount,
						TotalGuaranteedBefore: stats.TotalGuaranteed,
						TotalGuaranteedAfter:  stats.TotalGuaranteed, // TODO: change when introducing "guaranteed" commitments
						Commitments: []liquid.Commitment{
							{
								UUID:      c.UUID,
								OldStatus: Some(c.Status),
								NewStatus: None[liquid.CommitmentStatus](),
								Amount:    c.Amount,
								ConfirmBy: c.ConfirmBy,
								ExpiresAt: c.ExpiresAt,
							},
						},
					},
				},
			},
		},
	}
	_, err = datamodel.DelegateChangeCommitments(r.Context(), p.Cluster, ccr, sis, azRes.Path.ServiceType, p.DB)
	if err != nil {
		return nil, err
	}

	// delete
	c.Status = util.CommitmentStatusDeleted
	c.DeletedAt = Some(p.timeNow())
	c.UpdatedAt = p.timeNow()
	c.TransferStatus = limesresources.CommitmentTransferStatusNone
	c.TransferToken = None[string]()
	c.TransferStartedAt = None[time.Time]()
	err = db.ProjectCommitmentStore.Update(ctx, tx, c)
	if err != nil {
		return nil, err
	}
	err = tx.Commit()
	if err != nil {
		return nil, err
	}

	// audit log
	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest: ccr,
	}.ReplicateForAllProjectsWithDefaults(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusNoContent,
		Action:     cadf.DeleteAction,
	})
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}

	return nil, nil
}
