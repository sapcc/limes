// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
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

func (p *v2Provider) handlePostNewCommitment(r *http.Request, token *gopherpolicy.Token) (resourcesv2.Commitment, error) {
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
	dbDomain, dbProject, err := p.checkProjectAccess(token, req.ProjectUUID, "v2:project:commitment_create")
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
		// special case: need to insert this with a different status initially;
		// otherwise TransferableCommitmentCache will count it as an existing
		// confirmed commitment when gathering stats (will change this later)
		c.Status = liquid.CommitmentStatusPending
	case liquid.CommitmentStatusPending:
		c.ConfirmBy = Some(now)
	default:
		c.ConfirmBy = attrs.ConfirmBy
	}

	// using a transaction because we need to insert the commitment first to get
	// its ID (for use in the SupersedeContext of consumed commitments),
	// but we need to be able to revert this insertion if the CommitmentChangeRequest is rejected
	var auditEvents []audittools.Event
	err = withinTransactionThatMayBeADryRun(p.DB, req.DryRun, func(tx db.Interface) error {
		stats, err := getCommitmentStats(tx, dbProject.ID, azResource.ID)
		if err != nil {
			return err
		}

		err = tx.Insert(&c)
		if err != nil {
			return err
		}

		var (
			auditContext = audit.Context{UserIdentity: token, Request: r}
			resources, _ = p.Cluster.SIC.GetResourcesForType(path.ServiceType) // TODO: ugly (we just pass this around; maybe callees should fetch this themselves)
		)

		// creation of confirmed commitments requires a special codepath because we might be consuming other commitments
		if req.Status == liquid.CommitmentStatusConfirmed {
			mailTemplate := None[core.MailTemplate]()
			if mailConfig, exists := p.Cluster.Config.MailNotifications.Unpack(); exists && !req.DryRun {
				mailTemplate = Some(mailConfig.Templates.TransferredCommitments)
			}
			tcc, err := datamodel.NewTransferableCommitmentCache(tx, p.Cluster, service, resources, path, now, datamodel.GenerateProjectCommitmentUUID, datamodel.GenerateTransferToken, mailTemplate)
			if err != nil {
				return err
			}
			resp, err := tcc.CanConfirmWithTransfers(ctx, c, dbProject, dbDomain, true, req.DryRun, auditContext, cadf.CreateAction)
			if err != nil {
				return err
			}
			err = analyzeCommitmentChangeResponse(resp)
			if err != nil {
				return err
			}
			if !req.DryRun {
				auditEvents = append(auditEvents, tcc.RetrieveAuditEvents()...)
			}
			err = tcc.GenerateTransferMails(p.Cluster.BehaviorForResourcePath(path.Resource()).IdentityInV1API)
			if err != nil {
				return err
			}

			// update status (as mentioned before, we had to insert this as "pending" initially to avoid confusing the TCC)
			c.Status = liquid.CommitmentStatusConfirmed
			c.ConfirmedAt = Some(now)
			_, err = tx.Update(&c)
			if err != nil {
				return err
			}
		} else {
			ccr := liquid.CommitmentChangeRequest{
				DryRun:      req.DryRun,
				AZ:          path.AvailabilityZone,
				InfoVersion: service.LiquidVersion,
				ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
					dbProject.UUID: {
						ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(dbProject, dbDomain),
						ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
							path.ResourceName: {
								TotalConfirmedBefore:  stats.TotalConfirmed,
								TotalConfirmedAfter:   stats.TotalConfirmed,
								TotalGuaranteedBefore: stats.TotalGuaranteed,
								TotalGuaranteedAfter:  stats.TotalGuaranteed, // TODO: change when introducing "guaranteed" commitments
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
			resp, err := datamodel.DelegateChangeCommitments(ctx, p.Cluster, ccr, service, resources, tx)
			if err != nil {
				return err
			}
			if ccr.RequiresConfirmation() {
				err = analyzeCommitmentChangeResponse(resp)
				if err != nil {
					return err
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

		return nil
	}) // `tx` is committed here
	if err != nil {
		return none, err
	}
	if !req.DryRun {
		for _, event := range auditEvents {
			p.auditor.Record(event)
		}
	}

	// trigger a capacity scrape in order to ApplyComputedProjectQuota based on the new commitment
	if c.Status == liquid.CommitmentStatusConfirmed && !req.DryRun {
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

// withinTransactionThatMayBeADryRun starts a transaction such that the type system helps enforce that a dry run is not accidentally committed:
// Within `action`, `tx` is only a generic interface handle that does not allow calling `tx.Commit()` directly.
func withinTransactionThatMayBeADryRun(dbm *gorp.DbMap, dryRun bool, action func(tx db.Interface) error) error {
	tx, err := dbm.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)
	err = action(tx)
	if err != nil {
		return err
	}
	if dryRun {
		return tx.Rollback()
	} else {
		return tx.Commit()
	}
}
