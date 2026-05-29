// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"
	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/gg/options"

	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/audit"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

func (p *v2Provider) handlePostNewCommitment(r *http.Request) (resourcesv2.Commitment, error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/commitments/new")
	var (
		none resourcesv2.Commitment // used on error return paths only
		ctx  = r.Context()
	)

	// parse request
	req, err := parseRequestBodyAs[resourcesv2.CommitmentRequest](r)
	if err != nil {
		return none, err
	}
	path := db.AZResourcePath{
		ServiceType:      req.ServiceType,
		ResourceName:     req.ResourceName,
		AvailabilityZone: req.AvailabilityZone,
	}
	attrs := commitmentStatusAttributes{
		Status:          req.Status,
		ConfirmBy:       options.Map(req.ConfirmBy, util.FromUnixEncodedTime),
		NotifyOnConfirm: req.NotifyOnConfirm,
	}
	now := p.timeNow()

	// validate request contents
	dbDomain, dbProject, token, err := p.checkProjectAccess(r, req.ProjectUUID, "v2:commitment:create")
	if err != nil {
		return none, err
	}
	service, azResource, behavior, err := p.validateCommittability(path, dbDomain, dbProject, req.Duration)
	if err != nil {
		return none, err
	}
	if req.Amount == 0 {
		return none, respondwith.CustomStatus(http.StatusUnprocessableEntity, errEmptyAmount)
	}
	err = p.validateStatusAttributesOnNewCommitment(attrs, behavior, now)
	if err != nil {
		return none, err
	}

	// prepare commitment and commitment request
	creationContextJSON, err := json.Marshal(db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate})
	if err != nil {
		return none, err
	}
	c := db.ProjectCommitment{
		UUID:                datamodel.GenerateProjectCommitmentUUID(),
		AZResourceID:        azResource.ID,
		ProjectID:           dbProject.ID,
		Amount:              req.Amount,
		Duration:            req.Duration,
		CreatedAt:           now,
		UpdatedAt:           now,
		CreatorUUID:         token.UserUUID(),
		CreatorName:         fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
		ConfirmBy:           None[time.Time](), // may be set below
		ConfirmedAt:         None[time.Time](), // may be set below
		ExpiresAt:           req.Duration.AddTo(attrs.ConfirmBy.UnwrapOr(now)),
		CreationContextJSON: json.RawMessage(creationContextJSON),
		Status:              req.Status,
		NotifyOnConfirm:     req.NotifyOnConfirm,
	}
	switch c.Status {
	case liquid.CommitmentStatusConfirmed:
		c.ConfirmedAt = Some(now)
	case liquid.CommitmentStatusPending:
		c.ConfirmBy = Some(now)
	default:
		c.ConfirmBy = attrs.ConfirmBy
	}

	// when not doing a dry run, everything needs to be done in a DB transaction:
	// we need to insert the commitment first to get its ID (for use in the SupersedeContext of consumed commitments),
	// but we need to be able to revert this insertion if the CommitmentChangeRequest is rejected
	var (
		dbi    db.Interface
		commit func() error
	)
	if req.DryRun {
		dbi = p.DB
		commit = func() error { return nil }
	} else {
		tx, err := p.DB.Begin()
		if err != nil {
			return none, err
		}
		defer sqlext.RollbackUnlessCommitted(tx)
		commit = tx.Commit
	}

	stats, err := getCommitmentStats(dbi, dbProject.ID, azResource.ID)
	if err != nil {
		return none, err
	}

	err = dbi.Insert(&c)
	if err != nil {
		return none, err
	}

	var (
		auditEvents  []audittools.Event
		auditContext = audit.Context{
			UserIdentity: token,
			Request:      r,
		}
		resources, _ = p.Cluster.SIC.GetResourcesForType(path.ServiceType) // TODO: ugly (we just pass this around; callees should fetch this themselves)
	)

	// creation of confirmed commitments requires a special codepath because we might be consuming other commitments
	if c.Status == liquid.CommitmentStatusConfirmed {
		mailTemplate := None[core.MailTemplate]()
		if mailConfig, exists := p.Cluster.Config.MailNotifications.Unpack(); exists && !req.DryRun {
			mailTemplate = Some(mailConfig.Templates.TransferredCommitments)
		}
		tcc, err := datamodel.NewTransferableCommitmentCache(dbi, p.Cluster, service, resources, path, now, datamodel.GenerateProjectCommitmentUUID, datamodel.GenerateTransferToken, mailTemplate)
		if err != nil {
			return none, err
		}
		resp, err := tcc.CanConfirmWithTransfers(ctx, c, dbProject, dbDomain, true, req.DryRun, auditContext, cadf.CreateAction)
		if err != nil {
			return none, err
		}
		err = analyzeCommitmentChangeResponse(resp)
		if err != nil {
			return none, err
		}
		if !req.DryRun {
			auditEvents = append(auditEvents, tcc.RetrieveAuditEvents()...)
		}
		err = tcc.GenerateTransferMails(p.Cluster.BehaviorForResourcePath(path.Resource()).IdentityInV1API)
		if err != nil {
			return none, err
		}
	} else {
		isGuaranteed := uint64(0)
		if c.Status == liquid.CommitmentStatusGuaranteed {
			isGuaranteed = 1
		}
		ccr := liquid.CommitmentChangeRequest{
			AZ:          path.AvailabilityZone,
			InfoVersion: service.LiquidVersion,
			ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
				dbProject.UUID: {
					ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(dbProject, dbDomain),
					ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
						path.ResourceName: {
							TotalConfirmedBefore: stats.TotalConfirmed,
							TotalConfirmedAfter:  stats.TotalConfirmed,
							// TODO: change when introducing "guaranteed" commitments
							TotalGuaranteedBefore: stats.TotalGuaranteed,
							TotalGuaranteedAfter:  stats.TotalGuaranteed + isGuaranteed*c.Amount,
							Commitments: []liquid.Commitment{
								{
									UUID:      c.UUID,
									OldStatus: None[liquid.CommitmentStatus](),
									NewStatus: Some(c.Status),
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
		resp, err := datamodel.DelegateChangeCommitments(ctx, p.Cluster, ccr, service, resources, dbi)
		if err != nil {
			return none, err
		}
		if ccr.RequiresConfirmation() {
			err = analyzeCommitmentChangeResponse(resp)
			if err != nil {
				return none, err
			}
		}

		if !req.DryRun {
			auditEvents = append(auditEvents, audit.CommitmentEventTarget{
				CommitmentChangeRequest: ccr,
			}.ReplicateForAllProjectsWithDefaults(audittools.Event{
				Time:       now,
				Request:    r,
				User:       token,
				ReasonCode: http.StatusCreated,
				Action:     cadf.CreateAction,
			})...)
		}
	}

	err = commit()
	if err != nil {
		return none, err
	}
	if !req.DryRun {
		for _, event := range auditEvents {
			p.auditor.Record(event)
		}
	}

	// trigger a capacity scrape in order to ApplyComputedProjectQuota based on the new commitment
	if c.Status == liquid.CommitmentStatusConfirmed {
		_, err := p.DB.Exec(`UPDATE services SET next_scrape_at = $1 WHERE type = $2`, now, path.ServiceType)
		if err != nil {
			logg.Error("could not trigger a new capacity scrape after creating commitment %s: %s", c.UUID, err.Error())
		}
	}

	canBeDeleted := datamodel.CanDeleteCommitment(token, c, p.timeNow)
	result := convertCommitmentToDisplayForm(c, path, dbProject, canBeDeleted)
	if req.DryRun {
		result.UUID = "00000000-0000-0000-0000-000000000000"
	}
	return result, nil
}
