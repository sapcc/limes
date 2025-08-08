// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/mux"
	. "github.com/majewsky/gg/option"
	"github.com/majewsky/gg/options"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/reports"
)

var (
	getProjectCommitmentsQuery = sqlext.SimplifyWhitespace(`
		SELECT pc.*
		  FROM project_commitments pc
		  JOIN az_resources cazr ON pc.az_resource_id = cazr.id
		  JOIN resources cr ON cazr.resource_id = cr.id {{AND cr.name = $resource_name}}
		  JOIN services cs ON cr.service_id = cs.id {{AND cs.type = $service_type}}
		 WHERE %s AND pc.state NOT IN ('superseded', 'expired')
		 ORDER BY pc.id
	`)

	getAZResourceLocationsQuery = sqlext.SimplifyWhitespace(`
		SELECT cazr.id, cs.type, cr.name, cazr.az
		  FROM project_az_resources pazr
		  JOIN az_resources cazr on pazr.az_resource_id = cazr.id
		  JOIN resources cr ON cazr.resource_id = cr.id {{AND cr.name = $resource_name}}
		  JOIN services cs ON cr.service_id = cs.id {{AND cs.type = $service_type}}
		 WHERE %s
	`)

	findProjectCommitmentByIDQuery = sqlext.SimplifyWhitespace(`
		SELECT pc.*
		  FROM project_commitments pc
		 WHERE pc.id = $1 AND pc.project_id = $2
	`)

	findAZResourceIDByLocationQuery = sqlext.SimplifyWhitespace(`
		SELECT cazr.id, pr.forbidden IS NOT TRUE as resource_allows_commitments
		  FROM az_resources cazr
		  JOIN resources cr ON cazr.resource_id = cr.id
		  JOIN services cs ON cr.service_id = cs.id
		  JOIN project_resources pr ON pr.resource_id = cr.id
		 WHERE pr.project_id = $1 AND cs.type = $2 AND cr.name = $3 AND cazr.az = $4
	`)

	findAZResourceLocationByIDQuery = sqlext.SimplifyWhitespace(`
		SELECT cs.type, cr.name, cazr.az
		  FROM az_resources cazr
		  JOIN resources cr ON cazr.resource_id = cr.id
		  JOIN services cs ON cr.service_id = cs.id
		 WHERE cazr.id = $1
	`)
	getCommitmentWithMatchingTransferTokenQuery = sqlext.SimplifyWhitespace(`
		SELECT * FROM project_commitments WHERE id = $1 AND transfer_token = $2
	`)
	findCommitmentByTransferToken = sqlext.SimplifyWhitespace(`
		SELECT * FROM project_commitments WHERE transfer_token = $1
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
	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ErrorText(w, err) {
		return
	}

	// enumerate project AZ resources
	filter := reports.ReadFilter(r, p.Cluster, serviceInfos)
	queryStr, joinArgs := filter.PrepareQuery(getAZResourceLocationsQuery)
	whereStr, whereArgs := db.BuildSimpleWhereClause(map[string]any{"pazr.project_id": dbProject.ID}, len(joinArgs))
	azResourceLocationsByID := make(map[db.AZResourceID]core.AZResourceLocation)
	err = sqlext.ForeachRow(p.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			id  db.AZResourceID
			loc core.AZResourceLocation
		)
		err := rows.Scan(&id, &loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
		if err != nil {
			return err
		}
		// this check is defense in depth (the DB should be consistent with our config)
		if core.HasResource(serviceInfos, loc.ServiceType, loc.ResourceName) {
			azResourceLocationsByID[id] = loc
		}
		return nil
	})
	if respondwith.ErrorText(w, err) {
		return
	}

	// enumerate relevant project commitments
	queryStr, joinArgs = filter.PrepareQuery(getProjectCommitmentsQuery)
	whereStr, whereArgs = db.BuildSimpleWhereClause(map[string]any{"pc.project_id": dbProject.ID}, len(joinArgs))
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
		serviceInfo := core.InfoForService(serviceInfos, loc.ServiceType)
		resInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
		result = append(result, p.convertCommitmentToDisplayForm(c, loc, token, resInfo.Unit))
	}

	respondwith.JSON(w, http.StatusOK, map[string]any{"commitments": result})
}

// The state in the db can be directly mapped to the liquid.CommitmentStatus.
// However, the state "active" is named "confirmed" in the API. If the persisted
// state cannot be mapped to liquid terms, an empty string is returned.
func (p *v1Provider) convertCommitmentStateToDisplayForm(c db.ProjectCommitment) liquid.CommitmentStatus {
	var status = liquid.CommitmentStatus(c.State)
	if c.State == "active" {
		status = liquid.CommitmentStatusConfirmed
	}
	if status.IsValid() {
		return status
	}
	return "" // An empty state will be omitted when json serialized.
}

func (p *v1Provider) convertCommitmentToDisplayForm(c db.ProjectCommitment, loc core.AZResourceLocation, token *gopherpolicy.Token, unit limes.Unit) limesresources.Commitment {
	apiIdentity := p.Cluster.BehaviorForResource(loc.ServiceType, loc.ResourceName).IdentityInV1API
	return limesresources.Commitment{
		ID:               int64(c.ID),
		UUID:             string(c.UUID),
		ServiceType:      apiIdentity.ServiceType,
		ResourceName:     apiIdentity.Name,
		AvailabilityZone: loc.AvailabilityZone,
		Amount:           c.Amount,
		Unit:             unit,
		Duration:         c.Duration,
		CreatedAt:        limes.UnixEncodedTime{Time: c.CreatedAt},
		CreatorUUID:      c.CreatorUUID,
		CreatorName:      c.CreatorName,
		CanBeDeleted:     p.canDeleteCommitment(token, c),
		ConfirmBy:        options.Map(c.ConfirmBy, intoUnixEncodedTime).AsPointer(),
		ConfirmedAt:      options.Map(c.ConfirmedAt, intoUnixEncodedTime).AsPointer(),
		ExpiresAt:        limes.UnixEncodedTime{Time: c.ExpiresAt},
		TransferStatus:   c.TransferStatus,
		TransferToken:    c.TransferToken.AsPointer(),
		Status:           p.convertCommitmentStateToDisplayForm(c),
		NotifyOnConfirm:  c.NotifyOnConfirm,
		WasRenewed:       c.RenewContextJSON.IsSome(),
	}
}

// parseAndValidateCommitmentRequest parses and validates the request body for a commitment creation or confirmation.
// This function in its current form should only be used if the serviceInfo is not necessary to be used outside
// of this validation to avoid unnecessary database queries.
func (p *v1Provider) parseAndValidateCommitmentRequest(w http.ResponseWriter, r *http.Request, dbDomain db.Domain) (*limesresources.CommitmentRequest, *core.AZResourceLocation, *core.ScopedCommitmentBehavior) {
	// parse request
	var parseTarget struct {
		Request limesresources.CommitmentRequest `json:"commitment"`
	}
	if !RequireJSON(w, r, &parseTarget) {
		return nil, nil, nil
	}
	req := parseTarget.Request

	// validate request
	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ErrorText(w, err) {
		return nil, nil, nil
	}
	nm := core.BuildResourceNameMapping(p.Cluster, serviceInfos)
	dbServiceType, dbResourceName, ok := nm.MapFromV1API(req.ServiceType, req.ResourceName)
	if !ok {
		msg := fmt.Sprintf("no such service and/or resource: %s/%s", req.ServiceType, req.ResourceName)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return nil, nil, nil
	}
	behavior := p.Cluster.CommitmentBehaviorForResource(dbServiceType, dbResourceName).ForDomain(dbDomain.Name)
	serviceInfo := core.InfoForService(serviceInfos, dbServiceType)
	resInfo := core.InfoForResource(serviceInfo, dbResourceName)
	if len(behavior.Durations) == 0 {
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
	if !slices.Contains(behavior.Durations, req.Duration) {
		buf := must.Return(json.Marshal(behavior.Durations)) // panic on error is acceptable here, marshals should never fail
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
	req, loc, behavior := p.parseAndValidateCommitmentRequest(w, r, *dbDomain)
	if req == nil {
		return
	}

	var (
		azResourceID              db.AZResourceID
		resourceAllowsCommitments bool
	)
	err := p.DB.QueryRow(findAZResourceIDByLocationQuery, dbProject.ID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone).
		Scan(&azResourceID, &resourceAllowsCommitments)
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
	if !behavior.CanConfirmCommitmentsAt(now) {
		respondwith.JSON(w, http.StatusOK, map[string]bool{"result": false})
		return
	}

	// check for committable capacity
	result, err := datamodel.CanConfirmNewCommitment(*loc, dbProject.ID, req.Amount, p.Cluster, p.DB)
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
	req, loc, behavior := p.parseAndValidateCommitmentRequest(w, r, *dbDomain)
	if req == nil {
		return
	}

	var (
		azResourceID              db.AZResourceID
		resourceAllowsCommitments bool
	)
	err := p.DB.QueryRow(findAZResourceIDByLocationQuery, dbProject.ID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone).
		Scan(&azResourceID, &resourceAllowsCommitments)
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
	if minConfirmBy, ok := behavior.MinConfirmDate.Unpack(); ok && minConfirmBy.After(now) {
		if req.ConfirmBy == nil || req.ConfirmBy.Before(minConfirmBy) {
			msg := "this commitment needs a `confirm_by` timestamp at or after " + minConfirmBy.Format(time.RFC3339)
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
	confirmBy := options.Map(options.FromPointer(req.ConfirmBy), fromUnixEncodedTime)
	creationContext := db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate}
	buf, err := json.Marshal(creationContext)
	if respondwith.ErrorText(w, err) {
		return
	}
	dbCommitment := db.ProjectCommitment{
		UUID:                p.generateProjectCommitmentUUID(),
		AZResourceID:        azResourceID,
		ProjectID:           dbProject.ID,
		Amount:              req.Amount,
		Duration:            req.Duration,
		CreatedAt:           now,
		CreatorUUID:         token.UserUUID(),
		CreatorName:         fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
		ConfirmBy:           confirmBy,
		ConfirmedAt:         None[time.Time](), // may be set below
		ExpiresAt:           req.Duration.AddTo(confirmBy.UnwrapOr(now)),
		CreationContextJSON: json.RawMessage(buf),
	}
	if req.NotifyOnConfirm && req.ConfirmBy == nil {
		http.Error(w, "notification on confirm cannot be set for commitments with immediate confirmation", http.StatusConflict)
		return
	}
	dbCommitment.NotifyOnConfirm = req.NotifyOnConfirm

	if req.ConfirmBy == nil {
		// if not planned for confirmation in the future, confirm immediately (or fail)
		ok, err := datamodel.CanConfirmNewCommitment(*loc, dbProject.ID, req.Amount, p.Cluster, tx)
		if respondwith.ErrorText(w, err) {
			return
		}
		if !ok {
			http.Error(w, "not enough capacity available for immediate confirmation", http.StatusConflict)
			return
		}
		dbCommitment.ConfirmedAt = Some(now)
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
	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
	commitment := p.convertCommitmentToDisplayForm(dbCommitment, *loc, token, resourceInfo.Unit)
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
			Commitments:     []limesresources.Commitment{commitment},
			WorkflowContext: Some(creationContext),
		},
	})

	// if the commitment is immediately confirmed, trigger a capacity scrape in
	// order to ApplyComputedProjectQuotas based on the new commitment
	if dbCommitment.ConfirmedAt.IsSome() {
		_, err := p.DB.Exec(`UPDATE services SET next_scrape_at = $1 WHERE type = $2`, now, loc.ServiceType)
		if respondwith.ErrorText(w, err) {
			return
		}
	}

	// display the possibly confirmed commitment to the user
	err = p.DB.SelectOne(&dbCommitment, `SELECT * FROM project_commitments WHERE id = $1`, dbCommitment.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	respondwith.JSON(w, http.StatusCreated, map[string]any{"commitment": commitment})
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
	commitmentUUIDs := make([]db.ProjectCommitmentUUID, len(commitmentIDs))
	for i, commitmentID := range commitmentIDs {
		err := p.DB.SelectOne(&dbCommitments[i], findProjectCommitmentByIDQuery, commitmentID, dbProject.ID)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no such commitment", http.StatusNotFound)
			return
		} else if respondwith.ErrorText(w, err) {
			return
		}
		commitmentUUIDs[i] = dbCommitments[i].UUID
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
	err := p.DB.QueryRow(findAZResourceLocationByIDQuery, azResourceID).
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
		UUID:         p.generateProjectCommitmentUUID(),
		ProjectID:    dbProject.ID,
		AZResourceID: azResourceID,
		Amount:       0,                                   // overwritten below
		Duration:     limesresources.CommitmentDuration{}, // overwritten below
		CreatedAt:    now,
		CreatorUUID:  token.UserUUID(),
		CreatorName:  fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
		ConfirmedAt:  Some(now),
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
		Reason:                 db.CommitmentReasonMerge,
		RelatedCommitmentIDs:   commitmentIDs,
		RelatedCommitmentUUIDs: commitmentUUIDs,
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
		Reason:                 db.CommitmentReasonMerge,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{dbMergedCommitment.ID},
		RelatedCommitmentUUIDs: []db.ProjectCommitmentUUID{dbMergedCommitment.UUID},
	}
	buf, err = json.Marshal(supersedeContext)
	if respondwith.ErrorText(w, err) {
		return
	}
	for _, dbCommitment := range dbCommitments {
		dbCommitment.SupersededAt = Some(now)
		dbCommitment.SupersedeContextJSON = Some(json.RawMessage(buf))
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

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)

	c := p.convertCommitmentToDisplayForm(dbMergedCommitment, loc, token, resourceInfo.Unit)
	auditEvent := commitmentEventTarget{
		DomainID:        dbDomain.UUID,
		DomainName:      dbDomain.Name,
		ProjectID:       dbProject.UUID,
		ProjectName:     dbProject.Name,
		Commitments:     []limesresources.Commitment{c},
		WorkflowContext: Some(creationContext),
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

// As per the API spec, commitments can be renewed 90 days in advance at the earliest.
const commitmentRenewalPeriod = 90 * 24 * time.Hour

// RenewProjectCommitments handles POST /v1/domains/:domain_id/projects/:project_id/commitments/:id/renew.
func (p *v1Provider) RenewProjectCommitments(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/commitments/:id/renew")
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

	// Load commitment
	var dbCommitment db.ProjectCommitment
	err := p.DB.SelectOne(&dbCommitment, findProjectCommitmentByIDQuery, mux.Vars(r)["id"], dbProject.ID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}
	now := p.timeNow()

	// Check if commitment can be renewed
	var errs errext.ErrorSet
	if dbCommitment.State != db.CommitmentStateActive {
		errs.Addf("invalid state %q", dbCommitment.State)
	} else if now.After(dbCommitment.ExpiresAt) {
		errs.Addf("invalid state %q", db.CommitmentStateExpired)
	}
	if now.Before(dbCommitment.ExpiresAt.Add(-commitmentRenewalPeriod)) {
		errs.Addf("renewal attempt too early")
	}
	if dbCommitment.RenewContextJSON.IsSome() {
		errs.Addf("already renewed")
	}

	if !errs.IsEmpty() {
		msg := "cannot renew this commitment: " + errs.Join(", ")
		http.Error(w, msg, http.StatusConflict)
		return
	}

	// Create renewed commitment
	tx, err := p.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	var loc core.AZResourceLocation
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	creationContext := db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonRenew,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{dbCommitment.ID},
		RelatedCommitmentUUIDs: []db.ProjectCommitmentUUID{dbCommitment.UUID},
	}
	buf, err := json.Marshal(creationContext)
	if respondwith.ErrorText(w, err) {
		return
	}
	dbRenewedCommitment := db.ProjectCommitment{
		UUID:                p.generateProjectCommitmentUUID(),
		ProjectID:           dbProject.ID,
		AZResourceID:        dbCommitment.AZResourceID,
		Amount:              dbCommitment.Amount,
		Duration:            dbCommitment.Duration,
		CreatedAt:           now,
		CreatorUUID:         token.UserUUID(),
		CreatorName:         fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
		ConfirmBy:           Some(dbCommitment.ExpiresAt),
		ExpiresAt:           dbCommitment.Duration.AddTo(dbCommitment.ExpiresAt),
		State:               db.CommitmentStatePlanned,
		CreationContextJSON: json.RawMessage(buf),
	}

	err = tx.Insert(&dbRenewedCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}

	renewContext := db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonRenew,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{dbRenewedCommitment.ID},
		RelatedCommitmentUUIDs: []db.ProjectCommitmentUUID{dbRenewedCommitment.UUID},
	}
	buf, err = json.Marshal(renewContext)
	if respondwith.ErrorText(w, err) {
		return
	}
	dbCommitment.RenewContextJSON = Some(json.RawMessage(buf))
	_, err = tx.Update(&dbCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}

	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)

	// Create resultset and auditlogs
	c := p.convertCommitmentToDisplayForm(dbRenewedCommitment, loc, token, resourceInfo.Unit)
	auditEvent := commitmentEventTarget{
		DomainID:        dbDomain.UUID,
		DomainName:      dbDomain.Name,
		ProjectID:       dbProject.UUID,
		ProjectName:     dbProject.Name,
		Commitments:     []limesresources.Commitment{c},
		WorkflowContext: Some(creationContext),
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
	if !token.Require(w, "project:edit") { // NOTE: There is a more specific AuthZ check further down below.
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
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
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
	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
	c := p.convertCommitmentToDisplayForm(dbCommitment, loc, token, resourceInfo.Unit)
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
			Commitments: []limesresources.Commitment{c},
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
		dbCommitment.TransferToken = Some(transferToken)
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
		transferCommitment.TransferToken = Some(transferToken)
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
			Reason:                 db.CommitmentReasonSplit,
			RelatedCommitmentIDs:   []db.ProjectCommitmentID{transferCommitment.ID, remainingCommitment.ID},
			RelatedCommitmentUUIDs: []db.ProjectCommitmentUUID{transferCommitment.UUID, remainingCommitment.UUID},
		}
		buf, err := json.Marshal(supersedeContext)
		if respondwith.ErrorText(w, err) {
			return
		}
		dbCommitment.State = db.CommitmentStateSuperseded
		dbCommitment.SupersededAt = Some(now)
		dbCommitment.SupersedeContextJSON = Some(json.RawMessage(buf))
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
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
	c := p.convertCommitmentToDisplayForm(dbCommitment, loc, token, resourceInfo.Unit)
	if respondwith.ErrorText(w, err) {
		return
	}
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
		Reason:                 db.CommitmentReasonSplit,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{dbCommitment.ID},
		RelatedCommitmentUUIDs: []db.ProjectCommitmentUUID{dbCommitment.UUID},
	}
	buf, err := json.Marshal(creationContext)
	if err != nil {
		return db.ProjectCommitment{}, err
	}
	return db.ProjectCommitment{
		UUID:                p.generateProjectCommitmentUUID(),
		ProjectID:           dbCommitment.ProjectID,
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

func (p *v1Provider) buildConvertedCommitment(dbCommitment db.ProjectCommitment, azResourceID db.AZResourceID, amount uint64) (db.ProjectCommitment, error) {
	now := p.timeNow()
	creationContext := db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonConvert,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{dbCommitment.ID},
		RelatedCommitmentUUIDs: []db.ProjectCommitmentUUID{dbCommitment.UUID},
	}
	buf, err := json.Marshal(creationContext)
	if err != nil {
		return db.ProjectCommitment{}, err
	}
	return db.ProjectCommitment{
		UUID:                p.generateProjectCommitmentUUID(),
		ProjectID:           dbCommitment.ProjectID,
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
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "location data not found.", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
	c := p.convertCommitmentToDisplayForm(dbCommitment, loc, token, resourceInfo.Unit)
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
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	// check that the target project allows commitments at all
	var (
		azResourceID              db.AZResourceID
		resourceAllowsCommitments bool
	)
	err = p.DB.QueryRow(findAZResourceIDByLocationQuery, targetProject.ID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone).
		Scan(&azResourceID, &resourceAllowsCommitments)
	if respondwith.ErrorText(w, err) {
		return
	}
	if !resourceAllowsCommitments {
		msg := fmt.Sprintf("resource %s/%s is not enabled in the target project", loc.ServiceType, loc.ResourceName)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	_ = azResourceID // returned by the above query, but not used in this function

	// validate that we have enough committable capacity on the receiving side
	tx, err := p.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)
	ok, err := datamodel.CanMoveExistingCommitment(dbCommitment.Amount, loc, dbCommitment.ProjectID, targetProject.ID, p.Cluster, tx)
	if respondwith.ErrorText(w, err) {
		return
	}
	if !ok {
		http.Error(w, "not enough committable capacity on the receiving side", http.StatusConflict)
		return
	}

	dbCommitment.TransferStatus = ""
	dbCommitment.TransferToken = None[string]()
	dbCommitment.ProjectID = targetProject.ID
	_, err = tx.Update(&dbCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}
	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
	c := p.convertCommitmentToDisplayForm(dbCommitment, loc, token, resourceInfo.Unit)
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

	// TODO v2 API: This endpoint should be project-scoped in order to make it
	// easier to select the correct domain scope for the CommitmentBehavior.
	forTokenScope := func(behavior core.CommitmentBehavior) core.ScopedCommitmentBehavior {
		name := cmp.Or(token.ProjectScopeDomainName(), token.DomainScopeName(), "")
		if name != "" {
			return behavior.ForDomain(name)
		}
		return behavior.ForCluster()
	}

	// validate request
	vars := mux.Vars(r)
	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ErrorText(w, err) {
		return
	}

	nm := core.BuildResourceNameMapping(p.Cluster, serviceInfos)
	sourceServiceType, sourceResourceName, exists := nm.MapFromV1API(
		limes.ServiceType(vars["service_type"]),
		limesresources.ResourceName(vars["resource_name"]),
	)
	if !exists {
		msg := fmt.Sprintf("no such service and/or resource: %s/%s", vars["service_type"], vars["resource_name"])
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	sourceBehavior := forTokenScope(p.Cluster.CommitmentBehaviorForResource(sourceServiceType, sourceResourceName))

	serviceInfo := core.InfoForService(serviceInfos, sourceServiceType)
	sourceResInfo := core.InfoForResource(serviceInfo, sourceResourceName)

	// enumerate possible conversions
	conversions := make([]limesresources.CommitmentConversionRule, 0)
	if sourceBehavior.ConversionRule.IsSome() {
		for _, targetServiceType := range slices.Sorted(maps.Keys(serviceInfos)) {
			for targetResourceName, targetResInfo := range serviceInfos[targetServiceType].Resources {
				if sourceServiceType == targetServiceType && sourceResourceName == targetResourceName {
					continue
				}
				if sourceResInfo.Unit != targetResInfo.Unit {
					continue
				}

				targetBehavior := forTokenScope(p.Cluster.CommitmentBehaviorForResource(targetServiceType, targetResourceName))
				if rate, ok := sourceBehavior.GetConversionRateTo(targetBehavior).Unpack(); ok {
					apiServiceType, apiResourceName, ok := nm.MapToV1API(targetServiceType, targetResourceName)
					if ok {
						conversions = append(conversions, limesresources.CommitmentConversionRule{
							FromAmount:     rate.FromAmount,
							ToAmount:       rate.ToAmount,
							TargetService:  apiServiceType,
							TargetResource: apiResourceName,
						})
					}
				}
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
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&sourceLoc.ServiceType, &sourceLoc.ResourceName, &sourceLoc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}
	sourceBehavior := p.Cluster.CommitmentBehaviorForResource(sourceLoc.ServiceType, sourceLoc.ResourceName).ForDomain(dbDomain.Name)

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
	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ErrorText(w, err) {
		return
	}
	nm := core.BuildResourceNameMapping(p.Cluster, serviceInfos)
	targetServiceType, targetResourceName, exists := nm.MapFromV1API(req.TargetService, req.TargetResource)
	if !exists {
		msg := fmt.Sprintf("no such service and/or resource: %s/%s", req.TargetService, req.TargetResource)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	targetBehavior := p.Cluster.CommitmentBehaviorForResource(targetServiceType, targetResourceName).ForDomain(dbDomain.Name)
	if sourceLoc.ResourceName == targetResourceName && sourceLoc.ServiceType == targetServiceType {
		http.Error(w, "conversion attempt to the same resource.", http.StatusConflict)
		return
	}
	if len(targetBehavior.Durations) == 0 {
		msg := fmt.Sprintf("commitments are not enabled for resource %s/%s", req.TargetService, req.TargetResource)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	rate, ok := sourceBehavior.GetConversionRateTo(targetBehavior).Unpack()
	if !ok {
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
	conversionAmount := (req.SourceAmount / rate.FromAmount) * rate.ToAmount
	remainderAmount := req.SourceAmount % rate.FromAmount
	if remainderAmount > 0 {
		msg := fmt.Sprintf("amount: %v does not fit into conversion rate of: %v", req.SourceAmount, rate.FromAmount)
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
		targetAZResourceID        db.AZResourceID
		resourceAllowsCommitments bool
	)
	err = p.DB.QueryRow(findAZResourceIDByLocationQuery, dbProject.ID, targetServiceType, targetResourceName, sourceLoc.AvailabilityZone).
		Scan(&targetAZResourceID, &resourceAllowsCommitments)
	if respondwith.ErrorText(w, err) {
		return
	}
	// defense in depth. ServiceType and ResourceName of source and target are already checked. Here it's possible to explicitly check the ID's.
	if dbCommitment.AZResourceID == targetAZResourceID {
		http.Error(w, "conversion attempt to the same resource.", http.StatusConflict)
		return
	}
	if !resourceAllowsCommitments {
		msg := fmt.Sprintf("resource %s/%s is not enabled in this project", targetServiceType, targetResourceName)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	targetLoc := core.AZResourceLocation{
		ServiceType:      targetServiceType,
		ResourceName:     targetResourceName,
		AvailabilityZone: sourceLoc.AvailabilityZone,
	}
	// The commitment at the source resource was already confirmed and checked.
	// Therefore only the addition to the target resource has to be checked against.
	if dbCommitment.ConfirmedAt.IsSome() {
		ok, err := datamodel.CanConfirmNewCommitment(targetLoc, dbProject.ID, conversionAmount, p.Cluster, p.DB)
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

	var (
		relatedCommitmentIDs   []db.ProjectCommitmentID
		relatedCommitmentUUIDs []db.ProjectCommitmentUUID
	)
	remainingAmount := dbCommitment.Amount - req.SourceAmount
	serviceInfo := core.InfoForService(serviceInfos, sourceLoc.ServiceType)
	resourceInfo := core.InfoForResource(serviceInfo, sourceLoc.ResourceName)
	if remainingAmount > 0 {
		remainingCommitment, err := p.buildSplitCommitment(dbCommitment, remainingAmount)
		if respondwith.ErrorText(w, err) {
			return
		}
		relatedCommitmentIDs = append(relatedCommitmentIDs, remainingCommitment.ID)
		relatedCommitmentUUIDs = append(relatedCommitmentUUIDs, remainingCommitment.UUID)
		err = tx.Insert(&remainingCommitment)
		if respondwith.ErrorText(w, err) {
			return
		}
		auditEvent.Commitments = append(auditEvent.Commitments,
			p.convertCommitmentToDisplayForm(remainingCommitment, sourceLoc, token, resourceInfo.Unit),
		)
	}

	convertedCommitment, err := p.buildConvertedCommitment(dbCommitment, targetAZResourceID, conversionAmount)
	if respondwith.ErrorText(w, err) {
		return
	}
	relatedCommitmentIDs = append(relatedCommitmentIDs, convertedCommitment.ID)
	relatedCommitmentUUIDs = append(relatedCommitmentUUIDs, convertedCommitment.UUID)
	err = tx.Insert(&convertedCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}

	// supersede the original commitment
	now := p.timeNow()
	supersedeContext := db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonConvert,
		RelatedCommitmentIDs:   relatedCommitmentIDs,
		RelatedCommitmentUUIDs: relatedCommitmentUUIDs,
	}
	buf, err := json.Marshal(supersedeContext)
	if respondwith.ErrorText(w, err) {
		return
	}
	dbCommitment.State = db.CommitmentStateSuperseded
	dbCommitment.SupersededAt = Some(now)
	dbCommitment.SupersedeContextJSON = Some(json.RawMessage(buf))
	_, err = tx.Update(&dbCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}

	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	c := p.convertCommitmentToDisplayForm(convertedCommitment, targetLoc, token, resourceInfo.Unit)
	auditEvent.Commitments = append([]limesresources.Commitment{c}, auditEvent.Commitments...)
	auditEvent.WorkflowContext = Some(db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonSplit,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{dbCommitment.ID},
		RelatedCommitmentUUIDs: []db.ProjectCommitmentUUID{dbCommitment.UUID},
	})
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
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}
	behavior := p.Cluster.CommitmentBehaviorForResource(loc.ServiceType, loc.ResourceName).ForDomain(dbDomain.Name)
	if !slices.Contains(behavior.Durations, req.Duration) {
		msg := fmt.Sprintf("provided duration: %s does not match the config %v", req.Duration, behavior.Durations)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}

	newExpiresAt := req.Duration.AddTo(dbCommitment.ConfirmBy.UnwrapOr(dbCommitment.CreatedAt))
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

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
	c := p.convertCommitmentToDisplayForm(dbCommitment, loc, token, resourceInfo.Unit)
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
