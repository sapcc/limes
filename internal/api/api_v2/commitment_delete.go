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
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/audit"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

var findDeletableCommitmentQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	SELECT * FROM project_commitments
	WHERE uuid = $1
	AND status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}, {{util.CommitmentStatusDeleted}})
`))

func (p *v2Provider) handleDeleteCommitment(r *http.Request, token *gopherpolicy.Token) (any, error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/commitments/:commitment_uuid")

	// validate request contents
	cUUID := mux.Vars(r)["commitment_uuid"]
	var c db.ProjectCommitment
	err := p.DB.SelectOne(&c, findDeletableCommitmentQuery, cUUID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, respondwith.CustomStatus(http.StatusNotFound, errNoSuchCommitment)
	case err != nil:
		return nil, err
	}

	// obtain service ref
	sis := p.Cluster.SIC.GetSnapshot()
	azRes, ok := sis.GetAZResourceForID(c.AZResourceID)
	if !ok {
		// defense in depth, the referenced AZResource should exist
		return nil, errInvalidResourceReference
	}

	// check auth
	dbDomain, dbProject, err := p.checkProjectAccess(token, None[liquid.ProjectUUID](), Some(c.ProjectID), "v2:project:commitment_delete")
	if err != nil {
		return nil, err
	}
	canBeDeleted := isDeletable(token, c, p.timeNow)
	if !canBeDeleted {
		return nil, respondwith.CustomStatus(http.StatusForbidden, errNotDeletable)
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
			dbProject.UUID: {
				ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(dbProject, dbDomain),
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
	_, err = p.DB.Update(&c)
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
