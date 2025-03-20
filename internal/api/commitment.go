/******************************************************************************
*
*  Copyright 2023 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/liquids"
	"github.com/sapcc/limes/internal/reports"
)

var (
	getProjectCommitmentsQuery = sqlext.SimplifyWhitespace(`
		SELECT pc.*
		  FROM project_commitments pc
		  JOIN project_az_resources par ON pc.az_resource_id = par.id
		  JOIN project_resources pr ON par.resource_id = pr.id {{AND pr.name = $resource_name}}
		  JOIN project_services ps ON pr.service_id = ps.id {{AND ps.type = $service_type}}
		 WHERE %s AND pc.state NOT IN ('superseded', 'expired')
		 ORDER BY pc.id
	`)

	getProjectAZResourceLocationsQuery = sqlext.SimplifyWhitespace(`
		SELECT par.id, ps.type, pr.name, par.az
		  FROM project_az_resources par
		  JOIN project_resources pr ON par.resource_id = pr.id {{AND pr.name = $resource_name}}
		  JOIN project_services ps ON pr.service_id = ps.id {{AND ps.type = $service_type}}
		 WHERE %s
	`)

	findProjectCommitmentByIDQuery = sqlext.SimplifyWhitespace(`
		SELECT pc.*
		  FROM project_commitments pc
		  JOIN project_az_resources par ON pc.az_resource_id = par.id
		  JOIN project_resources pr ON par.resource_id = pr.id
		  JOIN project_services ps ON pr.service_id = ps.id
		 WHERE pc.id = $1 AND ps.project_id = $2
	`)

	// NOTE: The third output column is `resourceAllowsCommitments`.
	// We should be checking for `ResourceUsageReport.Forbidden == true`, but
	// since the `Forbidden` field is not persisted in the DB, we need to use
	// `max_quota_from_backend` as a proxy.
	findProjectAZResourceIDByLocationQuery = sqlext.SimplifyWhitespace(`
		SELECT pr.id, par.id, pr.max_quota_from_backend IS NULL
		  FROM project_az_resources par
		  JOIN project_resources pr ON par.resource_id = pr.id
		  JOIN project_services ps ON pr.service_id = ps.id
		 WHERE ps.project_id = $1 AND ps.type = $2 AND pr.name = $3 AND par.az = $4
	`)

	findProjectAZResourceLocationByIDQuery = sqlext.SimplifyWhitespace(`
		SELECT ps.type, pr.name, par.az
		  FROM project_az_resources par
		  JOIN project_resources pr ON par.resource_id = pr.id
		  JOIN project_services ps ON pr.service_id = ps.id
		 WHERE par.id = $1
	`)
	getCommitmentWithMatchingTransferTokenQuery = sqlext.SimplifyWhitespace(`
		SELECT * FROM project_commitments WHERE id = $1 AND transfer_token = $2
	`)
	findCommitmentByTransferToken = sqlext.SimplifyWhitespace(`
		SELECT * FROM project_commitments WHERE transfer_token = $1
	`)
	findTargetAZResourceIDBySourceIDQuery = sqlext.SimplifyWhitespace(`
		WITH source as (
		SELECT pr.id AS resource_id, ps.type, pr.name, par.az
		  FROM project_az_resources as par
		  JOIN project_resources pr ON par.resource_id = pr.id
		  JOIN project_services ps ON pr.service_id = ps.id
		 WHERE par.id = $1
		)
		SELECT s.resource_id, pr.id, par.id
		  FROM project_az_resources as par
		  JOIN project_resources pr ON par.resource_id = pr.id
		  JOIN project_services ps ON pr.service_id = ps.id
		  JOIN source s ON ps.type = s.type AND pr.name = s.name AND par.az = s.az
		 WHERE ps.project_id = $2
	`)
	findTargetAZResourceByTargetProjectQuery = sqlext.SimplifyWhitespace(`
		SELECT pr.id, par.id
		  FROM project_az_resources par
		  JOIN project_resources pr ON par.resource_id = pr.id
		  JOIN project_services ps ON pr.service_id = ps.id
		 WHERE ps.project_id = $1 AND ps.type = $2 AND pr.name = $3 AND par.az = $4
	`)
	forceImmediateCapacityScrapeQuery = sqlext.SimplifyWhitespace(`
		UPDATE cluster_capacitors SET next_scrape_at = $1 WHERE capacitor_id = (
			SELECT capacitor_id FROM cluster_services cs JOIN cluster_resources cr ON cs.id = cr.service_id
			WHERE cs.type = $2 AND cr.name = $3
		)
	`)
)

// GetProjectCommitments handles GET /v1/domains/:domain_id/projects/:project_id/commitments.
func (p *v1Provider) GetProjectCommitments(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/commitments")
	token := p.CheckToken(r)
	if !token.Require(w, "project:show") {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}
	dbProject := p.FindProjectFromRequest(w, r, dbDomain)
	if dbProject == nil {
		return
	}

	// enumerate project AZ resources
	filter := reports.ReadFilter(r, p.Cluster)
	queryStr, joinArgs := filter.PrepareQuery(getProjectAZResourceLocationsQuery)
	whereStr, whereArgs := db.BuildSimpleWhereClause(map[string]any{"ps.project_id": dbProject.ID}, len(joinArgs))
	azResourceLocationsByID := make(map[db.ProjectAZResourceID]core.AZResourceLocation)
	err := sqlext.ForeachRow(p.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			id  db.ProjectAZResourceID
			loc core.AZResourceLocation
		)
		err := rows.Scan(&id, &loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
		if err != nil {
			return err
		}
		// this check is defense in depth (the DB should be consistent with our config)
		if p.Cluster.HasResource(loc.ServiceType, loc.ResourceName) {
			azResourceLocationsByID[id] = loc
		}
		return nil
	})
	if respondwith.ErrorText(w, err) {
		return
	}

	// enumerate relevant project commitments
	queryStr, joinArgs = filter.PrepareQuery(getProjectCommitmentsQuery)
	whereStr, whereArgs = db.BuildSimpleWhereClause(map[string]any{"ps.project_id": dbProject.ID}, len(joinArgs))
	var dbCommitments []db.ProjectCommitment
	_, err = p.DB.Select(&dbCommitments, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...)...)
	if respondwith.ErrorText(w, err) {
		return
	}

	// render response
	result := make([]limesresources.Commitment, 0, len(dbCommitments))
	for _, c := range dbCommitments {
		loc, exists := azResourceLocationsByID[c.AZResourceID]
		if !exists {
			// defense in depth (the DB should not change that much between those two queries above)
			continue
		}
		result = append(result, p.convertCommitmentToDisplayForm(c, loc, token))
	}

	respondwith.JSON(w, http.StatusOK, map[string]any{"commitments": result})
}

func (p *v1Provider) convertCommitmentToDisplayForm(c db.ProjectCommitment, loc core.AZResourceLocation, token *gopherpolicy.Token) limesresources.Commitment {
	resInfo := p.Cluster.InfoForResource(loc.ServiceType, loc.ResourceName)
	apiIdentity := p.Cluster.BehaviorForResource(loc.ServiceType, loc.ResourceName).IdentityInV1API
	return limesresources.Commitment{
		ID:               int64(c.ID),
		ServiceType:      apiIdentity.ServiceType,
		ResourceName:     apiIdentity.Name,
		AvailabilityZone: loc.AvailabilityZone,
		Amount:           c.Amount,
		Unit:             resInfo.Unit,
		Duration:         c.Duration,
		CreatedAt:        limes.UnixEncodedTime{Time: c.CreatedAt},
		CreatorUUID:      c.CreatorUUID,
		CreatorName:      c.CreatorName,
		CanBeDeleted:     p.canDeleteCommitment(token, c),
		ConfirmBy:        maybeUnixEncodedTime(c.ConfirmBy),
		ConfirmedAt:      maybeUnixEncodedTime(c.ConfirmedAt),
		ExpiresAt:        limes.UnixEncodedTime{Time: c.ExpiresAt},
		TransferStatus:   c.TransferStatus,
		TransferToken:    c.TransferToken,
	}
}

func (p *v1Provider) parseAndValidateCommitmentRequest(w http.ResponseWriter, r *http.Request) (*limesresources.CommitmentRequest, *core.AZResourceLocation, *core.ResourceBehavior) {
	// parse request
	var parseTarget struct {
		Request limesresources.CommitmentRequest `json:"commitment"`
	}
	if !RequireJSON(w, r, &parseTarget) {
		return nil, nil, nil
	}
	req := parseTarget.Request

	// validate request
	nm := core.BuildResourceNameMapping(p.Cluster)
	dbServiceType, dbResourceName, ok := nm.MapFromV1API(req.ServiceType, req.ResourceName)
	if !ok {
		msg := fmt.Sprintf("no such service and/or resource: %s/%s", req.ServiceType, req.ResourceName)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return nil, nil, nil
	}
	behavior := p.Cluster.BehaviorForResource(dbServiceType, dbResourceName)
	resInfo := p.Cluster.InfoForResource(dbServiceType, dbResourceName)
	if len(behavior.CommitmentDurations) == 0 {
		http.Error(w, "commitments are not enabled for this resource", http.StatusUnprocessableEntity)
		return nil, nil, nil
	}
	if resInfo.Topology == liquid.FlatTopology {
		if req.AvailabilityZone != limes.AvailabilityZoneAny {
			http.Error(w, `resource does not accept AZ-aware commitments, so the AZ must be set to "any"`, http.StatusUnprocessableEntity)
			return nil, nil, nil
		}
	} else {
		if !slices.Contains(p.Cluster.Config.AvailabilityZones, req.AvailabilityZone) {
			http.Error(w, "no such availability zone", http.StatusUnprocessableEntity)
			return nil, nil, nil
		}
	}
	if !slices.Contains(behavior.CommitmentDurations, req.Duration) {
		buf := must.Return(json.Marshal(behavior.CommitmentDurations)) // panic on error is acceptable here, marshals should never fail
		msg := "unacceptable commitment duration for this resource, acceptable values: " + string(buf)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return nil, nil, nil
	}
	if req.Amount == 0 {
		http.Error(w, "amount of committed resource must be greater than zero", http.StatusUnprocessableEntity)
		return nil, nil, nil
	}

	loc := core.AZResourceLocation{
		ServiceType:      dbServiceType,
		ResourceName:     dbResourceName,
		AvailabilityZone: req.AvailabilityZone,
	}
	return &req, &loc, &behavior
}

// CanConfirmNewProjectCommitment handles POST /v1/domains/:domain_id/projects/:project_id/commitments/can-confirm.
func (p *v1Provider) CanConfirmNewProjectCommitment(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/commitments/can-confirm")
	token := p.CheckToken(r)
	if !token.Require(w, "project:edit") {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}
	dbProject := p.FindProjectFromRequest(w, r, dbDomain)
	if dbProject == nil {
		return
	}
	req, loc, behavior := p.parseAndValidateCommitmentRequest(w, r)
	if req == nil {
		return
	}

	var (
		resourceID                db.ProjectResourceID
		azResourceID              db.ProjectAZResourceID
		resourceAllowsCommitments bool
	)
	err := p.DB.QueryRow(findProjectAZResourceIDByLocationQuery, dbProject.ID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone).
		Scan(&resourceID, &azResourceID, &resourceAllowsCommitments)
	if respondwith.ErrorText(w, err) {
		return
	}
	if !resourceAllowsCommitments {
		msg := fmt.Sprintf("resource %s/%s is not enabled in this project", req.ServiceType, req.ResourceName)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	_ = azResourceID // returned by the above query, but not used in this function

	// commitments can never be confirmed immediately if we are before the min_confirm_date
	now := p.timeNow()
	if behavior.CommitmentMinConfirmDate != nil && behavior.CommitmentMinConfirmDate.After(now) {
		respondwith.JSON(w, http.StatusOK, map[string]bool{"result": false})
		return
	}

	// check for committable capacity
	result, err := datamodel.CanConfirmNewCommitment(*loc, resourceID, req.Amount, p.Cluster, p.DB)
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, http.StatusOK, map[string]bool{"result": result})
}

// CreateProjectCommitment handles POST /v1/domains/:domain_id/projects/:project_id/commitments/new.
func (p *v1Provider) CreateProjectCommitment(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/commitments/new")
	token := p.CheckToken(r)
	if !token.Require(w, "project:edit") {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}
	dbProject := p.FindProjectFromRequest(w, r, dbDomain)
	if dbProject == nil {
		return
	}
	req, loc, behavior := p.parseAndValidateCommitmentRequest(w, r)
	if req == nil {
		return
	}

	var (
		resourceID                db.ProjectResourceID
		azResourceID              db.ProjectAZResourceID
		resourceAllowsCommitments bool
	)
	err := p.DB.QueryRow(findProjectAZResourceIDByLocationQuery, dbProject.ID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone).
		Scan(&resourceID, &azResourceID, &resourceAllowsCommitments)
	if respondwith.ErrorText(w, err) {
		return
	}
	if !resourceAllowsCommitments {
		msg := fmt.Sprintf("resource %s/%s is not enabled in this project", req.ServiceType, req.ResourceName)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}

	// if given, confirm_by must definitely after time.Now(), and also after the MinConfirmDate if configured
	now := p.timeNow()
	if req.ConfirmBy != nil && req.ConfirmBy.Before(now) {
		http.Error(w, "confirm_by must not be set in the past", http.StatusUnprocessableEntity)
		return
	}
	if minConfirmBy := behavior.CommitmentMinConfirmDate; minConfirmBy != nil && minConfirmBy.After(now) {
		if req.ConfirmBy == nil || req.ConfirmBy.Before(*minConfirmBy) {
			msg := "this commitment needs a `confirm_by` timestamp at or after " + behavior.CommitmentMinConfirmDate.Format(time.RFC3339)
			http.Error(w, msg, http.StatusUnprocessableEntity)
			return
		}
	}

	// we want to validate committable capacity in the same transaction that creates the commitment
	tx, err := p.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// prepare commitment
	confirmBy := maybeUnpackUnixEncodedTime(req.ConfirmBy)
	creationContext := db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate}
	buf, err := json.Marshal(creationContext)
	if respondwith.ErrorText(w, err) {
		return
	}
	dbCommitment := db.ProjectCommitment{
		AZResourceID:        azResourceID,
		Amount:              req.Amount,
		Duration:            req.Duration,
		CreatedAt:           now,
		CreatorUUID:         token.UserUUID(),
		CreatorName:         fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
		ConfirmBy:           confirmBy,
		ConfirmedAt:         nil, // may be set below
		ExpiresAt:           req.Duration.AddTo(unwrapOrDefault(confirmBy, now)),
		CreationContextJSON: json.RawMessage(buf),
	}
	if req.ConfirmBy == nil {
		// if not planned for confirmation in the future, confirm immediately (or fail)
		ok, err := datamodel.CanConfirmNewCommitment(*loc, resourceID, req.Amount, p.Cluster, tx)
		if respondwith.ErrorText(w, err) {
			return
		}
		if !ok {
			http.Error(w, "not enough capacity available for immediate confirmation", http.StatusConflict)
			return
		}
		dbCommitment.ConfirmedAt = &now
		dbCommitment.State = db.CommitmentStateActive
	} else {
		dbCommitment.State = db.CommitmentStatePlanned
	}

	// create commitment
	err = tx.Insert(&dbCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}
	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}
	p.auditor.Record(audittools.Event{
		Time:       now,
		Request:    r,
		User:       token,
		ReasonCode: http.StatusCreated,
		Action:     cadf.CreateAction,
		Target: commitmentEventTarget{
			DomainID:        dbDomain.UUID,
			DomainName:      dbDomain.Name,
			ProjectID:       dbProject.UUID,
			ProjectName:     dbProject.Name,
			Commitments:     []limesresources.Commitment{p.convertCommitmentToDisplayForm(dbCommitment, *loc, token)},
			WorkflowContext: &creationContext,
		},
	})

	// if the commitment is immediately confirmed, trigger a capacity scrape in
	// order to ApplyComputedProjectQuotas based on the new commitment
	if dbCommitment.ConfirmedAt != nil {
		_, err := p.DB.Exec(forceImmediateCapacityScrapeQuery, now, loc.ServiceType, loc.ResourceName)
		if respondwith.ErrorText(w, err) {
			return
		}
	}

	// display the possibly confirmed commitment to the user
	err = p.DB.SelectOne(&dbCommitment, `SELECT * FROM project_commitments WHERE id = $1`, dbCommitment.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	c := p.convertCommitmentToDisplayForm(dbCommitment, *loc, token)
	respondwith.JSON(w, http.StatusCreated, map[string]any{"commitment": c})
}

// MergeProjectCommitments handles POST /v1/domains/:domain_id/projects/:project_id/commitments/merge.
func (p *v1Provider) MergeProjectCommitments(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/commitments/merge")
	token := p.CheckToken(r)
	if !token.Require(w, "project:edit") {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}
	dbProject := p.FindProjectFromRequest(w, r, dbDomain)
	if dbProject == nil {
		return
	}
	var parseTarget struct {
		CommitmentIDs []db.ProjectCommitmentID `json:"commitment_ids"`
	}
	if !RequireJSON(w, r, &parseTarget) {
		return
	}
	commitmentIDs := parseTarget.CommitmentIDs
	if len(commitmentIDs) < 2 {
		http.Error(w, fmt.Sprintf("merging requires at least two commitments, but %d were given", len(commitmentIDs)), http.StatusBadRequest)
		return
	}

	// Load commitments
	dbCommitments := make([]db.ProjectCommitment, len(commitmentIDs))
	for i, commitmentID := range commitmentIDs {
		err := p.DB.SelectOne(&dbCommitments[i], findProjectCommitmentByIDQuery, commitmentID, dbProject.ID)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no such commitment", http.StatusNotFound)
			return
		} else if respondwith.ErrorText(w, err) {
			return
		}
	}

	// Verify that all commitments agree on resource and AZ and are active
	azResourceID := dbCommitments[0].AZResourceID
	for _, dbCommitment := range dbCommitments {
		if dbCommitment.AZResourceID != azResourceID {
			http.Error(w, "all commitments must be on the same resource and AZ", http.StatusConflict)
			return
		}
		if dbCommitment.State != db.CommitmentStateActive {
			http.Error(w, "only active commitments may be merged", http.StatusConflict)
			return
		}
	}

	var loc core.AZResourceLocation
	err := p.DB.QueryRow(findProjectAZResourceLocationByIDQuery, azResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	// Start transaction for creating new commitment and marking merged commitments as superseded
	tx, err := p.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// Create merged template
	now := p.timeNow()
	dbMergedCommitment := db.ProjectCommitment{
		AZResourceID: azResourceID,
		Amount:       0,                                   // overwritten below
		Duration:     limesresources.CommitmentDuration{}, // overwritten below
		CreatedAt:    now,
		CreatorUUID:  token.UserUUID(),
		CreatorName:  fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
		ConfirmedAt:  &now,
		ExpiresAt:    time.Time{}, // overwritten below
		State:        db.CommitmentStateActive,
	}

	// Fill amount and latest expiration date
	for _, dbCommitment := range dbCommitments {
		dbMergedCommitment.Amount += dbCommitment.Amount
		if dbCommitment.ExpiresAt.After(dbMergedCommitment.ExpiresAt) {
			dbMergedCommitment.ExpiresAt = dbCommitment.ExpiresAt
			dbMergedCommitment.Duration = dbCommitment.Duration
		}
	}

	// Fill workflow context
	creationContext := db.CommitmentWorkflowContext{
		Reason:               db.CommitmentReasonMerge,
		RelatedCommitmentIDs: commitmentIDs,
	}
	buf, err := json.Marshal(creationContext)
	if respondwith.ErrorText(w, err) {
		return
	}
	dbMergedCommitment.CreationContextJSON = json.RawMessage(buf)

	// Insert into database
	err = tx.Insert(&dbMergedCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}

	// Mark merged commits as superseded
	supersedeContext := db.CommitmentWorkflowContext{
		Reason:               db.CommitmentReasonMerge,
		RelatedCommitmentIDs: []db.ProjectCommitmentID{dbMergedCommitment.ID},
	}
	buf, err = json.Marshal(supersedeContext)
	if respondwith.ErrorText(w, err) {
		return
	}
	for _, dbCommitment := range dbCommitments {
		dbCommitment.SupersededAt = &now
		dbCommitment.SupersedeContextJSON = liquids.PointerTo(json.RawMessage(buf))
		dbCommitment.State = db.CommitmentStateSuperseded
		_, err = tx.Update(&dbCommitment)
		if respondwith.ErrorText(w, err) {
			return
		}
	}

	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	c := p.convertCommitmentToDisplayForm(dbMergedCommitment, loc, token)
	auditEvent := commitmentEventTarget{
		DomainID:        dbDomain.UUID,
		DomainName:      dbDomain.Name,
		ProjectID:       dbProject.UUID,
		ProjectName:     dbProject.Name,
		Commitments:     []limesresources.Commitment{c},
		WorkflowContext: &creationContext,
	}
	p.auditor.Record(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusAccepted,
		Action:     cadf.UpdateAction,
		Target:     auditEvent,
	})

	respondwith.JSON(w, http.StatusAccepted, map[string]any{"commitment": c})
}

// DeleteProjectCommitment handles DELETE /v1/domains/:domain_id/projects/:project_id/commitments/:id.
func (p *v1Provider) DeleteProjectCommitment(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/commitments/:id")
	token := p.CheckToken(r)
	if !token.Require(w, "project:edit") { //NOTE: There is a more specific AuthZ check further down below.
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}
	dbProject := p.FindProjectFromRequest(w, r, dbDomain)
	if dbProject == nil {
		return
	}

	// load commitment
	var dbCommitment db.ProjectCommitment
	err := p.DB.SelectOne(&dbCommitment, findProjectCommitmentByIDQuery, mux.Vars(r)["id"], dbProject.ID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}
	var loc core.AZResourceLocation
	err = p.DB.QueryRow(findProjectAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	// check authorization for this specific commitment
	if !p.canDeleteCommitment(token, dbCommitment) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// perform deletion
	_, err = p.DB.Delete(&dbCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}
	p.auditor.Record(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusNoContent,
		Action:     cadf.DeleteAction,
		Target: commitmentEventTarget{
			DomainID:    dbDomain.UUID,
			DomainName:  dbDomain.Name,
			ProjectID:   dbProject.UUID,
			ProjectName: dbProject.Name,
			Commitments: []limesresources.Commitment{p.convertCommitmentToDisplayForm(dbCommitment, loc, token)},
		},
	})

	w.WriteHeader(http.StatusNoContent)
}

func (p *v1Provider) canDeleteCommitment(token *gopherpolicy.Token, commitment db.ProjectCommitment) bool {
	// up to 24 hours after creation of fresh commitments, future commitments can still be deleted by their creators
	if commitment.State == db.CommitmentStatePlanned || commitment.State == db.CommitmentStatePending || commitment.State == db.CommitmentStateActive {
		var creationContext db.CommitmentWorkflowContext
		err := json.Unmarshal(commitment.CreationContextJSON, &creationContext)
		if err == nil && creationContext.Reason == db.CommitmentReasonCreate && p.timeNow().Before(commitment.CreatedAt.Add(24*time.Hour)) {
			if token.Check("project:edit") {
				return true
			}
		}
	}

	// afterwards, a more specific permission is required to delete it
	//
	// This protects cloud admins making capacity planning decisions based on future commitments
	// from having their forecasts ruined by project admins suffering from buyer's remorse.
	return token.Check("project:uncommit")
}

// StartCommitmentTransfer handles POST /v1/domains/:id/projects/:id/commitments/:id/start-transfer
func (p *v1Provider) StartCommitmentTransfer(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/commitments/:id/start-transfer")
	token := p.CheckToken(r)
	if !token.Require(w, "project:edit") {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}
	dbProject := p.FindProjectFromRequest(w, r, dbDomain)
	if dbProject == nil {
		return
	}
	// TODO: eventually migrate this struct into go-api-declarations
	var parseTarget struct {
		Request struct {
			Amount         uint64                                  `json:"amount"`
			TransferStatus limesresources.CommitmentTransferStatus `json:"transfer_status,omitempty"`
		} `json:"commitment"`
	}
	if !RequireJSON(w, r, &parseTarget) {
		return
	}
	req := parseTarget.Request

	if req.TransferStatus != limesresources.CommitmentTransferStatusUnlisted && req.TransferStatus != limesresources.CommitmentTransferStatusPublic {
		http.Error(w, fmt.Sprintf("Invalid transfer_status code. Must be %s or %s.", limesresources.CommitmentTransferStatusUnlisted, limesresources.CommitmentTransferStatusPublic), http.StatusBadRequest)
		return
	}

	if req.Amount <= 0 {
		http.Error(w, "delivered amount needs to be a positive value.", http.StatusBadRequest)
		return
	}

	// load commitment
	var dbCommitment db.ProjectCommitment
	err := p.DB.SelectOne(&dbCommitment, findProjectCommitmentByIDQuery, mux.Vars(r)["id"], dbProject.ID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	// Mark whole commitment or a newly created, splitted one as transferrable.
	tx, err := p.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)
	transferToken := p.generateTransferToken()

	// Deny requests with a greater amount than the commitment.
	if req.Amount > dbCommitment.Amount {
		http.Error(w, "delivered amount exceeds the commitment amount.", http.StatusBadRequest)
		return
	}

	if req.Amount == dbCommitment.Amount {
		dbCommitment.TransferStatus = req.TransferStatus
		dbCommitment.TransferToken = &transferToken
		_, err = tx.Update(&dbCommitment)
		if respondwith.ErrorText(w, err) {
			return
		}
	} else {
		now := p.timeNow()
		transferAmount := req.Amount
		remainingAmount := dbCommitment.Amount - req.Amount
		transferCommitment, err := p.buildSplitCommitment(dbCommitment, transferAmount)
		if respondwith.ErrorText(w, err) {
			return
		}
		transferCommitment.TransferStatus = req.TransferStatus
		transferCommitment.TransferToken = &transferToken
		remainingCommitment, err := p.buildSplitCommitment(dbCommitment, remainingAmount)
		if respondwith.ErrorText(w, err) {
			return
		}
		err = tx.Insert(&transferCommitment)
		if respondwith.ErrorText(w, err) {
			return
		}
		err = tx.Insert(&remainingCommitment)
		if respondwith.ErrorText(w, err) {
			return
		}
		supersedeContext := db.CommitmentWorkflowContext{
			Reason:               db.CommitmentReasonSplit,
			RelatedCommitmentIDs: []db.ProjectCommitmentID{transferCommitment.ID, remainingCommitment.ID},
		}
		buf, err := json.Marshal(supersedeContext)
		if respondwith.ErrorText(w, err) {
			return
		}
		dbCommitment.State = db.CommitmentStateSuperseded
		dbCommitment.SupersededAt = &now
		dbCommitment.SupersedeContextJSON = liquids.PointerTo(json.RawMessage(buf))
		_, err = tx.Update(&dbCommitment)
		if respondwith.ErrorText(w, err) {
			return
		}
		dbCommitment = transferCommitment
	}
	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	var loc core.AZResourceLocation
	err = p.DB.QueryRow(findProjectAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	c := p.convertCommitmentToDisplayForm(dbCommitment, loc, token)
	p.auditor.Record(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusAccepted,
		Action:     cadf.UpdateAction,
		Target: commitmentEventTarget{
			DomainID:    dbDomain.UUID,
			DomainName:  dbDomain.Name,
			ProjectID:   dbProject.UUID,
			ProjectName: dbProject.Name,
			Commitments: []limesresources.Commitment{c},
		},
	})
	respondwith.JSON(w, http.StatusAccepted, map[string]any{"commitment": c})
}

func (p *v1Provider) buildSplitCommitment(dbCommitment db.ProjectCommitment, amount uint64) (db.ProjectCommitment, error) {
	now := p.timeNow()
	creationContext := db.CommitmentWorkflowContext{
		Reason:               db.CommitmentReasonSplit,
		RelatedCommitmentIDs: []db.ProjectCommitmentID{dbCommitment.ID},
	}
	buf, err := json.Marshal(creationContext)
	if err != nil {
		return db.ProjectCommitment{}, err
	}
	return db.ProjectCommitment{
		AZResourceID:        dbCommitment.AZResourceID,
		Amount:              amount,
		Duration:            dbCommitment.Duration,
		CreatedAt:           now,
		CreatorUUID:         dbCommitment.CreatorUUID,
		CreatorName:         dbCommitment.CreatorName,
		ConfirmBy:           dbCommitment.ConfirmBy,
		ConfirmedAt:         dbCommitment.ConfirmedAt,
		ExpiresAt:           dbCommitment.ExpiresAt,
		CreationContextJSON: json.RawMessage(buf),
		State:               dbCommitment.State,
	}, nil
}

func (p *v1Provider) buildConvertedCommitment(dbCommitment db.ProjectCommitment, azResourceID db.ProjectAZResourceID, amount uint64) (db.ProjectCommitment, error) {
	now := p.timeNow()
	creationContext := db.CommitmentWorkflowContext{
		Reason:               db.CommitmentReasonConvert,
		RelatedCommitmentIDs: []db.ProjectCommitmentID{dbCommitment.ID},
	}
	buf, err := json.Marshal(creationContext)
	if err != nil {
		return db.ProjectCommitment{}, err
	}
	return db.ProjectCommitment{
		AZResourceID:        azResourceID,
		Amount:              amount,
		Duration:            dbCommitment.Duration,
		CreatedAt:           now,
		CreatorUUID:         dbCommitment.CreatorUUID,
		CreatorName:         dbCommitment.CreatorName,
		ConfirmBy:           dbCommitment.ConfirmBy,
		ConfirmedAt:         dbCommitment.ConfirmedAt,
		ExpiresAt:           dbCommitment.ExpiresAt,
		CreationContextJSON: json.RawMessage(buf),
		State:               dbCommitment.State,
	}, nil
}

// GetCommitmentByTransferToken handles GET /v1/commitments/{token}
func (p *v1Provider) GetCommitmentByTransferToken(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/commitments/:token")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show_basic") {
		return
	}
	transferToken := mux.Vars(r)["token"]

	// The token column is a unique key, so we expect only one result.
	var dbCommitment db.ProjectCommitment
	err := p.DB.SelectOne(&dbCommitment, findCommitmentByTransferToken, transferToken)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no matching commitment found.", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	var loc core.AZResourceLocation
	err = p.DB.QueryRow(findProjectAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "location data not found.", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	c := p.convertCommitmentToDisplayForm(dbCommitment, loc, token)
	respondwith.JSON(w, http.StatusAccepted, map[string]any{"commitment": c})
}

// TransferCommitment handles POST /v1/domains/{domain_id}/projects/{project_id}/transfer-commitment/{id}?token={token}
func (p *v1Provider) TransferCommitment(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/transfer-commitment/:id")
	token := p.CheckToken(r)
	if !token.Require(w, "project:edit") {
		return
	}
	transferToken := r.Header.Get("Transfer-Token")
	if transferToken == "" {
		http.Error(w, "no transfer token provided", http.StatusBadRequest)
		return
	}
	commitmentID := mux.Vars(r)["id"]
	if commitmentID == "" {
		http.Error(w, "no transfer token provided", http.StatusBadRequest)
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}
	targetProject := p.FindProjectFromRequest(w, r, dbDomain)
	if targetProject == nil {
		return
	}

	// find commitment by transfer_token
	var dbCommitment db.ProjectCommitment
	err := p.DB.SelectOne(&dbCommitment, getCommitmentWithMatchingTransferTokenQuery, commitmentID, transferToken)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no matching commitment found", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	var loc core.AZResourceLocation
	err = p.DB.QueryRow(findProjectAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	// get target service and AZ resource
	var (
		sourceResourceID   db.ProjectResourceID
		targetResourceID   db.ProjectResourceID
		targetAZResourceID db.ProjectAZResourceID
	)
	err = p.DB.QueryRow(findTargetAZResourceIDBySourceIDQuery, dbCommitment.AZResourceID, targetProject.ID).
		Scan(&sourceResourceID, &targetResourceID, &targetAZResourceID)
	if respondwith.ErrorText(w, err) {
		return
	}

	// validate that we have enough committable capacity on the receiving side
	tx, err := p.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)
	ok, err := datamodel.CanMoveExistingCommitment(dbCommitment.Amount, loc, sourceResourceID, targetResourceID, p.Cluster, tx)
	if respondwith.ErrorText(w, err) {
		return
	}
	if !ok {
		http.Error(w, "not enough committable capacity on the receiving side", http.StatusConflict)
		return
	}

	dbCommitment.TransferStatus = ""
	dbCommitment.TransferToken = nil
	dbCommitment.AZResourceID = targetAZResourceID
	_, err = tx.Update(&dbCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}
	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	c := p.convertCommitmentToDisplayForm(dbCommitment, loc, token)
	p.auditor.Record(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusAccepted,
		Action:     cadf.UpdateAction,
		Target: commitmentEventTarget{
			DomainID:    dbDomain.UUID,
			DomainName:  dbDomain.Name,
			ProjectID:   targetProject.UUID,
			ProjectName: targetProject.Name,
			Commitments: []limesresources.Commitment{c},
		},
	})

	respondwith.JSON(w, http.StatusAccepted, map[string]any{"commitment": c})
}

// GetCommitmentConversion handles GET /v1/commitment-conversion/{service_type}/{resource_name}
func (p *v1Provider) GetCommitmentConversions(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/commitment-conversion/:service_type/:resource_name")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show_basic") {
		return
	}

	// validate request
	vars := mux.Vars(r)
	nm := core.BuildResourceNameMapping(p.Cluster)
	sourceServiceType, sourceResourceName, exists := nm.MapFromV1API(
		limes.ServiceType(vars["service_type"]),
		limesresources.ResourceName(vars["resource_name"]),
	)
	if !exists {
		msg := fmt.Sprintf("no such service and/or resource: %s/%s", vars["service_type"], vars["resource_name"])
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	sourceBehavior := p.Cluster.BehaviorForResource(sourceServiceType, sourceResourceName)
	sourceResInfo := p.Cluster.InfoForResource(sourceServiceType, sourceResourceName)

	// enumerate possible conversions
	conversions := make([]limesresources.CommitmentConversionRule, 0)
	for targetServiceType, quotaPlugin := range p.Cluster.QuotaPlugins {
		for targetResourceName, targetResInfo := range quotaPlugin.Resources() {
			targetBehavior := p.Cluster.BehaviorForResource(targetServiceType, targetResourceName)
			if targetBehavior.CommitmentConversion == (core.CommitmentConversion{}) {
				continue
			}
			if sourceServiceType == targetServiceType && sourceResourceName == targetResourceName {
				continue
			}
			if sourceResInfo.Unit != targetResInfo.Unit {
				continue
			}
			if sourceBehavior.CommitmentConversion.Identifier != targetBehavior.CommitmentConversion.Identifier {
				continue
			}

			fromAmount, toAmount := p.getCommitmentConversionRate(sourceBehavior, targetBehavior)
			apiServiceType, apiResourceName, ok := nm.MapToV1API(targetServiceType, targetResourceName)
			if ok {
				conversions = append(conversions, limesresources.CommitmentConversionRule{
					FromAmount:     fromAmount,
					ToAmount:       toAmount,
					TargetService:  apiServiceType,
					TargetResource: apiResourceName,
				})
			}
		}
	}

	// use a defined sorting to ensure deterministic behavior in tests
	slices.SortFunc(conversions, func(lhs, rhs limesresources.CommitmentConversionRule) int {
		result := strings.Compare(string(lhs.TargetService), string(rhs.TargetService))
		if result != 0 {
			return result
		}
		return strings.Compare(string(lhs.TargetResource), string(rhs.TargetResource))
	})

	respondwith.JSON(w, http.StatusOK, map[string]any{"conversions": conversions})
}

// ConvertCommitment handles POST /v1/domains/{domain_id}/projects/{project_id}/commitments/{commitment_id}/convert
func (p *v1Provider) ConvertCommitment(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:domain_id/projects/:project_id/commitments/:commitment_id/convert")
	token := p.CheckToken(r)
	if !token.Require(w, "project:edit") {
		return
	}
	commitmentID := mux.Vars(r)["commitment_id"]
	if commitmentID == "" {
		http.Error(w, "no transfer token provided", http.StatusBadRequest)
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}
	dbProject := p.FindProjectFromRequest(w, r, dbDomain)
	if dbProject == nil {
		return
	}

	// section: sourceBehavior
	var dbCommitment db.ProjectCommitment
	err := p.DB.SelectOne(&dbCommitment, findProjectCommitmentByIDQuery, commitmentID, dbProject.ID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}
	var sourceLoc core.AZResourceLocation
	err = p.DB.QueryRow(findProjectAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&sourceLoc.ServiceType, &sourceLoc.ResourceName, &sourceLoc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}
	sourceBehavior := p.Cluster.BehaviorForResource(sourceLoc.ServiceType, sourceLoc.ResourceName)

	// section: targetBehavior
	var parseTarget struct {
		Request struct {
			TargetService  limes.ServiceType           `json:"target_service"`
			TargetResource limesresources.ResourceName `json:"target_resource"`
			SourceAmount   uint64                      `json:"source_amount"`
			TargetAmount   uint64                      `json:"target_amount"`
		} `json:"commitment"`
	}
	if !RequireJSON(w, r, &parseTarget) {
		return
	}
	req := parseTarget.Request
	nm := core.BuildResourceNameMapping(p.Cluster)
	targetServiceType, targetResourceName, exists := nm.MapFromV1API(req.TargetService, req.TargetResource)
	if !exists {
		msg := fmt.Sprintf("no such service and/or resource: %s/%s", req.TargetService, req.TargetResource)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	targetBehavior := p.Cluster.BehaviorForResource(targetServiceType, targetResourceName)
	if sourceLoc.ResourceName == targetResourceName && sourceLoc.ServiceType == targetServiceType {
		http.Error(w, "conversion attempt to the same resource.", http.StatusConflict)
		return
	}
	if len(targetBehavior.CommitmentDurations) == 0 {
		msg := fmt.Sprintf("commitments are not enabled for resource %s/%s", req.TargetService, req.TargetResource)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	if sourceBehavior.CommitmentConversion.Identifier == "" || sourceBehavior.CommitmentConversion.Identifier != targetBehavior.CommitmentConversion.Identifier {
		msg := fmt.Sprintf("commitment is not convertible into resource %s/%s", req.TargetService, req.TargetResource)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}

	// section: conversion
	if req.SourceAmount > dbCommitment.Amount {
		msg := fmt.Sprintf("unprocessable source amount. provided: %v, commitment: %v", req.SourceAmount, dbCommitment.Amount)
		http.Error(w, msg, http.StatusConflict)
		return
	}
	fromAmount, toAmount := p.getCommitmentConversionRate(sourceBehavior, targetBehavior)
	conversionAmount := (req.SourceAmount / fromAmount) * toAmount
	remainderAmount := req.SourceAmount % fromAmount
	if remainderAmount > 0 {
		msg := fmt.Sprintf("amount: %v does not fit into conversion rate of: %v", req.SourceAmount, fromAmount)
		http.Error(w, msg, http.StatusConflict)
		return
	}
	if conversionAmount != req.TargetAmount {
		msg := fmt.Sprintf("conversion mismatch. provided: %v, calculated: %v", req.TargetAmount, conversionAmount)
		http.Error(w, msg, http.StatusConflict)
		return
	}

	tx, err := p.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	var (
		targetResourceID   db.ProjectResourceID
		targetAZResourceID db.ProjectAZResourceID
	)
	err = p.DB.QueryRow(findTargetAZResourceByTargetProjectQuery, dbProject.ID, targetServiceType, targetResourceName, sourceLoc.AvailabilityZone).
		Scan(&targetResourceID, &targetAZResourceID)
	if respondwith.ErrorText(w, err) {
		return
	}
	// defense in depth. ServiceType and ResourceName of source and target are already checked. Here it's possible to explicitly check the ID's.
	if dbCommitment.AZResourceID == targetAZResourceID {
		http.Error(w, "conversion attempt to the same resource.", http.StatusConflict)
		return
	}
	targetLoc := core.AZResourceLocation{
		ServiceType:      targetServiceType,
		ResourceName:     targetResourceName,
		AvailabilityZone: sourceLoc.AvailabilityZone,
	}
	// The commitment at the source resource was already confirmed and checked.
	// Therefore only the addition to the target resource has to be checked against.
	if dbCommitment.ConfirmedAt != nil {
		ok, err := datamodel.CanConfirmNewCommitment(targetLoc, targetResourceID, conversionAmount, p.Cluster, p.DB)
		if respondwith.ErrorText(w, err) {
			return
		}
		if !ok {
			http.Error(w, "not enough capacity to confirm the commitment", http.StatusUnprocessableEntity)
			return
		}
	}

	auditEvent := commitmentEventTarget{
		DomainID:    dbDomain.UUID,
		DomainName:  dbDomain.Name,
		ProjectID:   dbProject.UUID,
		ProjectName: dbProject.Name,
	}

	relatedCommitmentIDs := make([]db.ProjectCommitmentID, 0)
	remainingAmount := dbCommitment.Amount - req.SourceAmount
	if remainingAmount > 0 {
		remainingCommitment, err := p.buildSplitCommitment(dbCommitment, remainingAmount)
		if respondwith.ErrorText(w, err) {
			return
		}
		relatedCommitmentIDs = append(relatedCommitmentIDs, remainingCommitment.ID)
		err = tx.Insert(&remainingCommitment)
		if respondwith.ErrorText(w, err) {
			return
		}
		auditEvent.Commitments = append(auditEvent.Commitments,
			p.convertCommitmentToDisplayForm(remainingCommitment, sourceLoc, token),
		)
	}

	convertedCommitment, err := p.buildConvertedCommitment(dbCommitment, targetAZResourceID, conversionAmount)
	if respondwith.ErrorText(w, err) {
		return
	}
	relatedCommitmentIDs = append(relatedCommitmentIDs, convertedCommitment.ID)
	err = tx.Insert(&convertedCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}

	// supersede the original commitment
	now := p.timeNow()
	supersedeContext := db.CommitmentWorkflowContext{
		Reason:               db.CommitmentReasonConvert,
		RelatedCommitmentIDs: relatedCommitmentIDs,
	}
	buf, err := json.Marshal(supersedeContext)
	if respondwith.ErrorText(w, err) {
		return
	}
	dbCommitment.State = db.CommitmentStateSuperseded
	dbCommitment.SupersededAt = &now
	dbCommitment.SupersedeContextJSON = liquids.PointerTo(json.RawMessage(buf))
	_, err = tx.Update(&dbCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}

	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	c := p.convertCommitmentToDisplayForm(convertedCommitment, targetLoc, token)
	auditEvent.Commitments = append([]limesresources.Commitment{c}, auditEvent.Commitments...)
	auditEvent.WorkflowContext = &db.CommitmentWorkflowContext{
		Reason:               db.CommitmentReasonSplit,
		RelatedCommitmentIDs: []db.ProjectCommitmentID{dbCommitment.ID},
	}
	p.auditor.Record(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusAccepted,
		Action:     cadf.UpdateAction,
		Target:     auditEvent,
	})

	respondwith.JSON(w, http.StatusAccepted, map[string]any{"commitment": c})
}

func (p *v1Provider) getCommitmentConversionRate(source, target core.ResourceBehavior) (fromAmount, toAmount uint64) {
	divisor := GetGreatestCommonDivisor(source.CommitmentConversion.Weight, target.CommitmentConversion.Weight)
	fromAmount = target.CommitmentConversion.Weight / divisor
	toAmount = source.CommitmentConversion.Weight / divisor
	return fromAmount, toAmount
}

// ExtendCommitmentDuration handles POST /v1/domains/{domain_id}/projects/{project_id}/commitments/{commitment_id}/update-duration
func (p *v1Provider) UpdateCommitmentDuration(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:domain_id/projects/:project_id/commitments/:commitment_id/update-duration")
	token := p.CheckToken(r)
	if !token.Require(w, "project:edit") {
		return
	}
	commitmentID := mux.Vars(r)["commitment_id"]
	if commitmentID == "" {
		http.Error(w, "no transfer token provided", http.StatusBadRequest)
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}
	dbProject := p.FindProjectFromRequest(w, r, dbDomain)
	if dbProject == nil {
		return
	}
	var Request struct {
		Duration limesresources.CommitmentDuration `json:"duration"`
	}
	req := Request
	if !RequireJSON(w, r, &req) {
		return
	}

	var dbCommitment db.ProjectCommitment
	err := p.DB.SelectOne(&dbCommitment, findProjectCommitmentByIDQuery, commitmentID, dbProject.ID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	now := p.timeNow()
	if dbCommitment.ExpiresAt.Before(now) || dbCommitment.ExpiresAt.Equal(now) {
		http.Error(w, "unable to process expired commitment", http.StatusForbidden)
		return
	}

	if dbCommitment.State == db.CommitmentStateSuperseded {
		msg := fmt.Sprintf("unable to operate on commitment with a state of %s", dbCommitment.State)
		http.Error(w, msg, http.StatusForbidden)
		return
	}

	var loc core.AZResourceLocation
	err = p.DB.QueryRow(findProjectAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}
	behavior := p.Cluster.BehaviorForResource(loc.ServiceType, loc.ResourceName)
	if !slices.Contains(behavior.CommitmentDurations, req.Duration) {
		msg := fmt.Sprintf("provided duration: %s does not match the config %v", req.Duration, behavior.CommitmentDurations)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}

	newExpiresAt := req.Duration.AddTo(unwrapOrDefault(dbCommitment.ConfirmBy, dbCommitment.CreatedAt))
	if newExpiresAt.Before(dbCommitment.ExpiresAt) {
		msg := fmt.Sprintf("duration change from %s to %s forbidden", dbCommitment.Duration, req.Duration)
		http.Error(w, msg, http.StatusForbidden)
		return
	}

	dbCommitment.Duration = req.Duration
	dbCommitment.ExpiresAt = newExpiresAt
	_, err = p.DB.Update(&dbCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}

	c := p.convertCommitmentToDisplayForm(dbCommitment, loc, token)
	p.auditor.Record(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusOK,
		Action:     cadf.UpdateAction,
		Target: commitmentEventTarget{
			DomainID:    dbDomain.UUID,
			DomainName:  dbDomain.Name,
			ProjectID:   dbProject.UUID,
			ProjectName: dbProject.Name,
			Commitments: []limesresources.Commitment{c},
		},
	})

	respondwith.JSON(w, http.StatusOK, map[string]any{"commitment": c})
}
