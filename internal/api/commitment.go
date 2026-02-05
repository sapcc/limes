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
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/audit"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/reports"
	"github.com/sapcc/limes/internal/util"
)

var (
	getProjectCommitmentsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		SELECT pc.*
		  FROM project_commitments pc
		  JOIN az_resources azr ON pc.az_resource_id = azr.id
		  JOIN resources r ON azr.resource_id = r.id {{AND r.name = $resource_name}}
		  JOIN services s ON r.service_id = s.id {{AND s.type = $service_type}}
		 WHERE %s AND pc.status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}})
		 ORDER BY pc.id
	`))

	getAZResourceLocationsQuery = sqlext.SimplifyWhitespace(`
		SELECT azr.id, s.type, r.name, azr.az
		  FROM project_az_resources pazr
		  JOIN az_resources azr on pazr.az_resource_id = azr.id
		  JOIN resources r ON azr.resource_id = r.id {{AND r.name = $resource_name}}
		  JOIN services s ON r.service_id = s.id {{AND s.type = $service_type}}
		 WHERE %s
	`)

	getPublicCommitmentsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		SELECT pc.*
		  FROM project_commitments pc
		  JOIN az_resources azr ON pc.az_resource_id = azr.id
		  JOIN resources r ON azr.resource_id = r.id
		 WHERE r.path = $1
		   AND pc.status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}})
		   AND pc.transfer_status = {{limesresources.CommitmentTransferStatusPublic}}
	`))

	findProjectCommitmentByIDQuery = sqlext.SimplifyWhitespace(`
		SELECT pc.*
		  FROM project_commitments pc
		 WHERE pc.id = $1 AND pc.project_id = $2
	`)

	findAZResourceIDByLocationQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		SELECT azr.id, pr.forbidden IS NOT TRUE as resource_allows_commitments, COALESCE(total_confirmed, 0) as total_confirmed
		FROM az_resources azr
		JOIN resources r ON azr.resource_id = r.id
		JOIN services s ON r.service_id = s.id
		JOIN project_resources pr ON pr.resource_id = r.id
		LEFT JOIN (
			SELECT SUM(pc.amount) as total_confirmed
			FROM az_resources azr
			JOIN resources r ON azr.resource_id = r.id
			JOIN services s ON r.service_id = s.id
			JOIN project_commitments pc ON azr.id = pc.az_resource_id
			WHERE pc.project_id = $1 AND s.type = $2 AND r.name = $3 AND azr.az = $4 AND status = {{liquid.CommitmentStatusConfirmed}}
		) pc ON 1=1
		WHERE pr.project_id = $1 AND s.type = $2 AND r.name = $3 AND azr.az = $4
	`))

	findAZResourceLocationByIDQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		SELECT s.type, r.name, azr.az, COALESCE(pc.total_confirmed,0) AS total_confirmed
		FROM az_resources azr
		JOIN resources r ON azr.resource_id = r.id
		JOIN services s ON r.service_id = s.id
		LEFT JOIN (
				SELECT SUM(amount) as total_confirmed
				FROM project_commitments pc
				WHERE az_resource_id = $1 AND project_id = $2 AND status = {{liquid.CommitmentStatusConfirmed}}
		) pc ON 1=1
		WHERE azr.id = $1;
	`))
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
	if respondwith.ObfuscatedErrorText(w, err) {
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
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// enumerate relevant project commitments
	queryStr, joinArgs = filter.PrepareQuery(getProjectCommitmentsQuery)
	whereStr, whereArgs = db.BuildSimpleWhereClause(map[string]any{"pc.project_id": dbProject.ID}, len(joinArgs))
	var dbCommitments []db.ProjectCommitment
	_, err = p.DB.Select(&dbCommitments, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...)...)
	if respondwith.ObfuscatedErrorText(w, err) {
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
		result = append(result, datamodel.ConvertCommitmentToDisplayForm(c, loc.AvailabilityZone, p.Cluster.BehaviorForResourceLocation(loc).IdentityInV1API, datamodel.CanDeleteCommitment(token, c, p.timeNow), resInfo.Unit))
	}

	respondwith.JSON(w, http.StatusOK, map[string]any{"commitments": result})
}

// GetPublicCommitments handles GET /v1/public-commitments.
func (p *v1Provider) GetPublicCommitments(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/public-commitments")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show_basic") {
		return
	}

	// with the "cluster:show" permission, the user is assumed to be a cloud admin;
	// non-cloud-admins will be restricted to committability rules in their respective domain
	var dbDomain Option[db.Domain]
	if token.Check("cluster:show") {
		dbDomain = None[db.Domain]()
	} else {
		domainUUID := cmp.Or(token.ProjectScopeDomainUUID(), token.DomainScopeUUID(), "")
		if domainUUID == "" {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		var domain db.Domain
		err := p.DB.SelectOne(&domain, `SELECT * FROM domains WHERE uuid = $1`, domainUUID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "no such domain", http.StatusNotFound)
			return
		case respondwith.ObfuscatedErrorText(w, err):
			return
		default:
			dbDomain = Some(domain)
		}
	}

	// parse and validate request
	query := r.URL.Query()
	requestedServiceType := limes.ServiceType(query.Get("service"))
	requestedResourceName := limesresources.ResourceName(query.Get("resource"))

	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	nm := core.BuildResourceNameMapping(p.Cluster, serviceInfos)
	dbServiceType, dbResourceName, ok := nm.MapFromV1API(requestedServiceType, requestedResourceName)
	if !ok {
		msg := fmt.Sprintf("no such service and/or resource: %q", fmt.Sprintf("%s/%s", requestedServiceType, requestedResourceName))
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	serviceInfo := core.InfoForService(serviceInfos, dbServiceType)
	resInfo := core.InfoForResource(serviceInfo, dbResourceName)

	if domain, ok := dbDomain.Unpack(); ok {
		behavior := p.Cluster.CommitmentBehaviorForResource(dbServiceType, dbResourceName).ForDomain(domain.Name)
		if len(behavior.Durations) == 0 {
			http.Error(w, "commitments are not enabled for this resource", http.StatusUnprocessableEntity)
			return
		}
	} else {
		// as a cloud-admin, allow listing commitments if there is any rule that could allow a domain to have commitments
		behavior := p.Cluster.CommitmentBehaviorForResource(dbServiceType, dbResourceName)
		allowsCommitments := false
		for _, entry := range behavior.DurationsPerDomain {
			if len(entry.Value) > 0 {
				allowsCommitments = true
				break
			}
		}
		if !allowsCommitments {
			http.Error(w, "commitments are not enabled for this resource", http.StatusUnprocessableEntity)
			return
		}
	}

	// list AZ resource locations
	filter := reports.Filter{
		Includes: map[db.ServiceType]map[liquid.ResourceName]bool{
			dbServiceType: {dbResourceName: true},
		},
		ServiceTypeIsFiltered:  true,
		ResourceNameIsFiltered: true,
	}
	queryStr, joinArgs := filter.PrepareQuery(getAZResourceLocationsQuery)
	whereStr, whereArgs := db.BuildSimpleWhereClause(nil, len(joinArgs))
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
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// list commitments
	var dbCommitments []db.ProjectCommitment
	_, err = p.DB.Select(&dbCommitments, getPublicCommitmentsQuery, fmt.Sprintf("%s/%s", dbServiceType, dbResourceName))
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	result := make([]limesresources.Commitment, 0, len(dbCommitments))
	for _, dbCommitment := range dbCommitments {
		loc, exists := azResourceLocationsByID[dbCommitment.AZResourceID]
		if !exists {
			continue // like above, this is just defense in depth (the DB should be consistent with itself)
		}
		c := datamodel.ConvertCommitmentToDisplayForm(dbCommitment, loc.AvailabilityZone, p.Cluster.BehaviorForResourceLocation(loc).IdentityInV1API, datamodel.CanDeleteCommitment(token, dbCommitment, p.timeNow), resInfo.Unit)
		// hide some fields that we should not be showing in this very public list
		c.CreatorUUID = ""
		c.CreatorName = ""
		c.CanBeDeleted = false
		c.NotifyOnConfirm = false
		c.WasRenewed = false

		result = append(result, c)
	}

	respondwith.JSON(w, http.StatusOK, map[string]any{"commitments": result})
}

// parseAndValidateCommitmentRequest parses and validates the request body for a commitment creation or confirmation.
// This function in its current form should only be used if the serviceInfo is not necessary to be used outside
// of this validation to avoid unnecessary database queries.
func (p *v1Provider) parseAndValidateCommitmentRequest(w http.ResponseWriter, r *http.Request, dbDomain db.Domain) (*limesresources.CommitmentRequest, *core.AZResourceLocation, *core.ScopedCommitmentBehavior, *liquid.ServiceInfo) {
	// parse request
	var parseTarget struct {
		Request limesresources.CommitmentRequest `json:"commitment"`
	}
	if !RequireJSON(w, r, &parseTarget) {
		return nil, nil, nil, nil
	}
	req := parseTarget.Request

	// validate request
	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ObfuscatedErrorText(w, err) {
		return nil, nil, nil, nil
	}
	nm := core.BuildResourceNameMapping(p.Cluster, serviceInfos)
	dbServiceType, dbResourceName, ok := nm.MapFromV1API(req.ServiceType, req.ResourceName)
	if !ok {
		msg := fmt.Sprintf("no such service and/or resource: %s/%s", req.ServiceType, req.ResourceName)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return nil, nil, nil, nil
	}
	behavior := p.Cluster.CommitmentBehaviorForResource(dbServiceType, dbResourceName).ForDomain(dbDomain.Name)
	serviceInfo := core.InfoForService(serviceInfos, dbServiceType)
	resInfo := core.InfoForResource(serviceInfo, dbResourceName)
	if len(behavior.Durations) == 0 {
		http.Error(w, "commitments are not enabled for this resource", http.StatusUnprocessableEntity)
		return nil, nil, nil, nil
	}
	if resInfo.Topology == liquid.FlatTopology {
		if req.AvailabilityZone != limes.AvailabilityZoneAny {
			http.Error(w, `resource does not accept AZ-aware commitments, so the AZ must be set to "any"`, http.StatusUnprocessableEntity)
			return nil, nil, nil, nil
		}
	} else {
		if !slices.Contains(p.Cluster.Config.AvailabilityZones, req.AvailabilityZone) {
			http.Error(w, "no such availability zone", http.StatusUnprocessableEntity)
			return nil, nil, nil, nil
		}
	}
	if !slices.Contains(behavior.Durations, req.Duration) {
		buf := must.Return(json.Marshal(behavior.Durations)) // panic on error is acceptable here, marshals should never fail
		msg := "unacceptable commitment duration for this resource, acceptable values: " + string(buf)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return nil, nil, nil, nil
	}
	if req.Amount == 0 {
		http.Error(w, "amount of committed resource must be greater than zero", http.StatusUnprocessableEntity)
		return nil, nil, nil, nil
	}

	loc := core.AZResourceLocation{
		ServiceType:      dbServiceType,
		ResourceName:     dbResourceName,
		AvailabilityZone: req.AvailabilityZone,
	}
	return &req, &loc, &behavior, &serviceInfo
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
	req, loc, behavior, serviceInfo := p.parseAndValidateCommitmentRequest(w, r, *dbDomain)
	if req == nil {
		return
	}

	var (
		azResourceID              db.AZResourceID
		resourceAllowsCommitments bool
		totalConfirmed            uint64
	)
	err := p.DB.QueryRow(findAZResourceIDByLocationQuery, dbProject.ID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone).
		Scan(&azResourceID, &resourceAllowsCommitments, &totalConfirmed)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	if !resourceAllowsCommitments {
		msg := fmt.Sprintf("resource %s/%s is not enabled in this project", req.ServiceType, req.ResourceName)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	_ = azResourceID // returned by the above query, but not used in this function

	// this api should always check CanConfirm at now()
	now := p.timeNow()
	if req.ConfirmBy != nil {
		http.Error(w, "this API can only check whether a commitment can be confirmed immediately", http.StatusUnprocessableEntity)
		return
	}
	canConfirmErrMsg := behavior.CanConfirmCommitmentsAt(now)
	if canConfirmErrMsg != "" {
		respondwith.JSON(w, http.StatusOK, map[string]bool{"result": false})
		return
	}

	// check for committable capacity
	newStatus := liquid.CommitmentStatusConfirmed
	totalConfirmedAfter := totalConfirmed + req.Amount

	ccr, err := datamodel.DelegateChangeCommitments(r.Context(), p.Cluster, liquid.CommitmentChangeRequest{
		DryRun:      true,
		AZ:          loc.AvailabilityZone,
		InfoVersion: serviceInfo.Version,
		ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
			dbProject.UUID: {
				ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(*dbProject, *dbDomain),
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					loc.ResourceName: {
						TotalConfirmedBefore: totalConfirmed,
						TotalConfirmedAfter:  totalConfirmedAfter,
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
						Commitments: []liquid.Commitment{
							{
								UUID:      p.generateProjectCommitmentUUID(),
								OldStatus: None[liquid.CommitmentStatus](),
								NewStatus: Some(newStatus),
								Amount:    req.Amount,
								ExpiresAt: req.Duration.AddTo(now),
							},
						},
					},
				},
			},
		},
	}, *loc, *serviceInfo, p.DB)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	result := !commitmentChangeRequestWasRejected(ccr, w, false)
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
	req, loc, behavior, serviceInfo := p.parseAndValidateCommitmentRequest(w, r, *dbDomain)
	if req == nil {
		return
	}

	var (
		azResourceID              db.AZResourceID
		resourceAllowsCommitments bool
		totalConfirmed            uint64
	)
	err := p.DB.QueryRow(findAZResourceIDByLocationQuery, dbProject.ID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone).
		Scan(&azResourceID, &resourceAllowsCommitments, &totalConfirmed)
	if respondwith.ObfuscatedErrorText(w, err) {
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
	confirmBy := options.Map(options.FromPointer(req.ConfirmBy), util.FromUnixEncodedTime)
	canConfirmErrMsg := behavior.CanConfirmCommitmentsAt(confirmBy.UnwrapOr(now))
	if canConfirmErrMsg != "" {
		http.Error(w, canConfirmErrMsg, http.StatusUnprocessableEntity)
		return
	}

	// we want to validate committable capacity in the same transaction that creates the commitment
	tx, err := p.DB.Begin()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// prepare commitment
	creationContext := db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate}
	buf, err := json.Marshal(creationContext)
	if respondwith.ObfuscatedErrorText(w, err) {
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
	if req.NotifyOnConfirm && confirmBy.IsNone() {
		http.Error(w, "notification on confirm cannot be set for commitments with immediate confirmation", http.StatusConflict)
		return
	}
	dbCommitment.NotifyOnConfirm = req.NotifyOnConfirm
	var auditEvents []audittools.Event

	if confirmBy.IsNone() {
		// When the commitment is to be confirmed immediately, the capacity check
		// is carried out together with the transferability check in the cache.
		mailTemplate := None[core.MailTemplate]()
		if mailConfig, exists := p.Cluster.Config.MailNotifications.Unpack(); exists {
			mailTemplate = Some(mailConfig.Templates.TransferredCommitments)
		}
		transferableCommitmentCache, err := datamodel.NewTransferableCommitmentCache(tx, p.Cluster, *serviceInfo, *loc, now, p.generateProjectCommitmentUUID, p.generateTransferToken, mailTemplate)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
		auditContext := audit.Context{
			UserIdentity: token,
			Request:      r,
		}
		result, err := transferableCommitmentCache.CanConfirmWithTransfers(r.Context(), dbCommitment, *dbProject, *dbDomain, true, false, auditContext, cadf.CreateAction)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
		if commitmentChangeRequestWasRejected(result, w, true) {
			return
		}

		// retrieve mails and audit event
		auditEvents = append(auditEvents, transferableCommitmentCache.RetrieveAuditEvents()...)
		err = transferableCommitmentCache.GenerateTransferMails(p.Cluster.BehaviorForResourceLocation(*loc).IdentityInV1API)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}

		dbCommitment.ConfirmedAt = Some(now)
		dbCommitment.Status = liquid.CommitmentStatusConfirmed
	} else {
		// when the commitment is not to be confirmed immediately, we check
		// (or inform the liquid) about the capacity independently.
		ccr := liquid.CommitmentChangeRequest{
			AZ:          loc.AvailabilityZone,
			InfoVersion: serviceInfo.Version,
			ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
				dbProject.UUID: {
					ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(*dbProject, *dbDomain),
					ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
						loc.ResourceName: {
							TotalConfirmedBefore: totalConfirmed,
							TotalConfirmedAfter:  totalConfirmed,
							// TODO: change when introducing "guaranteed" commitments
							TotalGuaranteedBefore: 0,
							TotalGuaranteedAfter:  0,
							Commitments: []liquid.Commitment{
								{
									UUID:      dbCommitment.UUID,
									OldStatus: None[liquid.CommitmentStatus](),
									NewStatus: Some(liquid.CommitmentStatusPlanned),
									Amount:    req.Amount,
									ConfirmBy: confirmBy,
									ExpiresAt: req.Duration.AddTo(confirmBy.UnwrapOr(now)),
								},
							},
						},
					},
				},
			},
		}
		commitmentChangeResponse, err := datamodel.DelegateChangeCommitments(r.Context(), p.Cluster, ccr, *loc, *serviceInfo, tx)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
		if ccr.RequiresConfirmation() && commitmentChangeRequestWasRejected(commitmentChangeResponse, w, true) {
			return
		}
		// TODO: change when introducing "guaranteed" commitments
		dbCommitment.Status = liquid.CommitmentStatusPlanned

		auditEvents = append(auditEvents, audit.CommitmentEventTarget{
			CommitmentChangeRequest: ccr,
		}.ReplicateForAllProjects(audittools.Event{
			Time:       now,
			Request:    r,
			User:       token,
			ReasonCode: http.StatusCreated,
			Action:     cadf.CreateAction,
		}, None[cadf.Action](), None[liquid.ProjectUUID]())...)
	}

	// create commitment
	err = tx.Insert(&dbCommitment)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	err = tx.Commit()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}

	// if the commitment is immediately confirmed, trigger a capacity scrape in
	// order to ApplyComputedProjectQuotas based on the new commitment
	if dbCommitment.ConfirmedAt.IsSome() {
		_, err := p.DB.Exec(`UPDATE services SET next_scrape_at = $1 WHERE type = $2`, now, loc.ServiceType)
		if err != nil {
			logg.Error("could not trigger a new capacity scrape after creating commitment %s: %s", dbCommitment.UUID, err.Error())
		}
	}

	// render response
	resourceInfo := core.InfoForResource(*serviceInfo, loc.ResourceName)
	commitment := datamodel.ConvertCommitmentToDisplayForm(dbCommitment, loc.AvailabilityZone, p.Cluster.BehaviorForResourceLocation(*loc).IdentityInV1API, datamodel.CanDeleteCommitment(token, dbCommitment, p.timeNow), resourceInfo.Unit)
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
	commitmentUUIDs := make([]liquid.CommitmentUUID, len(commitmentIDs))
	for i, commitmentID := range commitmentIDs {
		err := p.DB.SelectOne(&dbCommitments[i], findProjectCommitmentByIDQuery, commitmentID, dbProject.ID)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "no such commitment", http.StatusNotFound)
			return
		} else if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
		commitmentUUIDs[i] = dbCommitments[i].UUID
	}

	// Verify that all commitments...:
	// - agree on resource and AZ
	// - are confirmed
	// - are not in transfer
	azResourceID := dbCommitments[0].AZResourceID
	for _, dbCommitment := range dbCommitments {
		if dbCommitment.AZResourceID != azResourceID {
			http.Error(w, "all commitments must be on the same resource and AZ", http.StatusConflict)
			return
		}
		if dbCommitment.Status != liquid.CommitmentStatusConfirmed {
			http.Error(w, "only confirmed commitments may be merged", http.StatusConflict)
			return
		}
		// do not allow conversions on commitments in transfer
		if dbCommitment.TransferStatus != limesresources.CommitmentTransferStatusNone {
			http.Error(w, "commitments in transfer cannot be merged", http.StatusUnprocessableEntity)
			return
		}
	}

	var (
		loc            core.AZResourceLocation
		totalConfirmed uint64
	)
	err := p.DB.QueryRow(findAZResourceLocationByIDQuery, azResourceID, dbProject.ID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone, &totalConfirmed)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// Start transaction for creating new commitment and marking merged commitments as superseded
	tx, err := p.DB.Begin()
	if respondwith.ObfuscatedErrorText(w, err) {
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
		Status:       liquid.CommitmentStatusConfirmed,
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
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	dbMergedCommitment.CreationContextJSON = json.RawMessage(buf)

	// Insert into database
	err = tx.Insert(&dbMergedCommitment)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// Mark merged commits as superseded
	supersedeContext := db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonMerge,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{dbMergedCommitment.ID},
		RelatedCommitmentUUIDs: []liquid.CommitmentUUID{dbMergedCommitment.UUID},
	}
	buf, err = json.Marshal(supersedeContext)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	for _, dbCommitment := range dbCommitments {
		dbCommitment.SupersededAt = Some(now)
		dbCommitment.SupersedeContextJSON = Some(json.RawMessage(buf))
		dbCommitment.Status = liquid.CommitmentStatusSuperseded
		_, err = tx.Update(&dbCommitment)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
	}

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	liquidCommitments := make([]liquid.Commitment, 1, len(dbCommitments)+1)
	// new
	liquidCommitments[0] = liquid.Commitment{
		UUID:      dbMergedCommitment.UUID,
		OldStatus: None[liquid.CommitmentStatus](),
		NewStatus: Some(liquid.CommitmentStatusConfirmed),
		Amount:    dbMergedCommitment.Amount,
		ConfirmBy: dbMergedCommitment.ConfirmBy,
		ExpiresAt: dbMergedCommitment.ExpiresAt,
	}
	// old
	for _, dbCommitment := range dbCommitments {
		liquidCommitments = append(liquidCommitments, liquid.Commitment{
			UUID:      dbCommitment.UUID,
			OldStatus: Some(liquid.CommitmentStatusConfirmed),
			NewStatus: Some(liquid.CommitmentStatusSuperseded),
			Amount:    dbCommitment.Amount,
			ConfirmBy: dbCommitment.ConfirmBy,
			ExpiresAt: dbCommitment.ExpiresAt,
		})
	}
	ccr := liquid.CommitmentChangeRequest{
		AZ:          loc.AvailabilityZone,
		InfoVersion: serviceInfo.Version,
		ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
			dbProject.UUID: {
				ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(*dbProject, *dbDomain),
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					loc.ResourceName: {
						TotalConfirmedBefore: totalConfirmed,
						TotalConfirmedAfter:  totalConfirmed,
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
						Commitments:           liquidCommitments,
					},
				},
			},
		},
	}
	_, err = datamodel.DelegateChangeCommitments(r.Context(), p.Cluster, ccr, loc, serviceInfo, tx)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	err = tx.Commit()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
	c := datamodel.ConvertCommitmentToDisplayForm(dbMergedCommitment, loc.AvailabilityZone, p.Cluster.BehaviorForResourceLocation(loc).IdentityInV1API, datamodel.CanDeleteCommitment(token, dbMergedCommitment, p.timeNow), resourceInfo.Unit)

	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest: ccr,
	}.ReplicateForAllProjects(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusAccepted,
		Action:     cadf.UpdateAction,
	}, None[cadf.Action](), None[liquid.ProjectUUID]())
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}

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
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	now := p.timeNow()

	// Check if commitment can be renewed
	var errs errext.ErrorSet
	if dbCommitment.Status != liquid.CommitmentStatusConfirmed {
		errs.Addf("invalid status %q", dbCommitment.Status)
	} else if now.After(dbCommitment.ExpiresAt) {
		errs.Addf("invalid status %q", liquid.CommitmentStatusExpired)
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
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	var (
		loc            core.AZResourceLocation
		totalConfirmed uint64
	)
	err = tx.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID, dbProject.ID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone, &totalConfirmed)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	creationContext := db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonRenew,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{dbCommitment.ID},
		RelatedCommitmentUUIDs: []liquid.CommitmentUUID{dbCommitment.UUID},
	}
	buf, err := json.Marshal(creationContext)
	if respondwith.ObfuscatedErrorText(w, err) {
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
		Status:              liquid.CommitmentStatusPlanned,
		CreationContextJSON: json.RawMessage(buf),
	}

	err = tx.Insert(&dbRenewedCommitment)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	renewContext := db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonRenew,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{dbRenewedCommitment.ID},
		RelatedCommitmentUUIDs: []liquid.CommitmentUUID{dbRenewedCommitment.UUID},
	}
	buf, err = json.Marshal(renewContext)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	dbCommitment.RenewContextJSON = Some(json.RawMessage(buf))
	_, err = tx.Update(&dbCommitment)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	// TODO: for now, this is CommitmentChangeRequest.RequiresConfirmation() = false, because totalConfirmed stays and guaranteed is not used yet.
	// when we change this, we need to evaluate the response of the liquid
	ccr := liquid.CommitmentChangeRequest{
		AZ:          loc.AvailabilityZone,
		InfoVersion: serviceInfo.Version,
		ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
			dbProject.UUID: {
				ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(*dbProject, *dbDomain),
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					loc.ResourceName: {
						TotalConfirmedBefore: totalConfirmed,
						TotalConfirmedAfter:  totalConfirmed,
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
						Commitments: []liquid.Commitment{
							{
								UUID:      dbRenewedCommitment.UUID,
								OldStatus: None[liquid.CommitmentStatus](),
								NewStatus: Some(liquid.CommitmentStatusPlanned),
								Amount:    dbRenewedCommitment.Amount,
								ConfirmBy: dbRenewedCommitment.ConfirmBy,
								ExpiresAt: dbRenewedCommitment.ExpiresAt,
							},
						},
					},
				},
			},
		},
	}
	_, err = datamodel.DelegateChangeCommitments(r.Context(), p.Cluster, ccr, loc, serviceInfo, tx)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	err = tx.Commit()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// Create resultset and auditlogs
	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
	c := datamodel.ConvertCommitmentToDisplayForm(dbRenewedCommitment, loc.AvailabilityZone, p.Cluster.BehaviorForResourceLocation(loc).IdentityInV1API, datamodel.CanDeleteCommitment(token, dbRenewedCommitment, p.timeNow), resourceInfo.Unit)

	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest: ccr,
	}.ReplicateForAllProjects(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusAccepted,
		Action:     cadf.UpdateAction,
	}, None[cadf.Action](), None[liquid.ProjectUUID]())
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}

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
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	var (
		loc            core.AZResourceLocation
		totalConfirmed uint64
	)
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID, dbProject.ID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone, &totalConfirmed)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// check authorization for this specific commitment
	if !datamodel.CanDeleteCommitment(token, dbCommitment, p.timeNow) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	totalConfirmedAfter := totalConfirmed
	if dbCommitment.Status == liquid.CommitmentStatusConfirmed {
		totalConfirmedAfter -= dbCommitment.Amount
	}

	ccr := liquid.CommitmentChangeRequest{
		AZ:          loc.AvailabilityZone,
		InfoVersion: serviceInfo.Version,
		ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
			dbProject.UUID: {
				ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(*dbProject, *dbDomain),
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					loc.ResourceName: {
						TotalConfirmedBefore: totalConfirmed,
						TotalConfirmedAfter:  totalConfirmedAfter,
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
						Commitments: []liquid.Commitment{
							{
								UUID:      dbCommitment.UUID,
								OldStatus: Some(dbCommitment.Status),
								NewStatus: None[liquid.CommitmentStatus](),
								Amount:    dbCommitment.Amount,
								ConfirmBy: dbCommitment.ConfirmBy,
								ExpiresAt: dbCommitment.ExpiresAt,
							},
						},
					},
				},
			},
		},
	}
	_, err = datamodel.DelegateChangeCommitments(r.Context(), p.Cluster, ccr, loc, serviceInfo, p.DB)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// perform deletion
	_, err = p.DB.Delete(&dbCommitment)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest: ccr,
	}.ReplicateForAllProjects(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusNoContent,
		Action:     cadf.DeleteAction,
	}, None[cadf.Action](), None[liquid.ProjectUUID]())
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}
	w.WriteHeader(http.StatusNoContent)
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

	acceptableTransferStatuses := []limesresources.CommitmentTransferStatus{
		limesresources.CommitmentTransferStatusUnlisted,
		limesresources.CommitmentTransferStatusPublic,
		// None is allowed in order to withdraw a public offer for a commitment transfer
		limesresources.CommitmentTransferStatusNone,
	}
	if !slices.Contains(acceptableTransferStatuses, req.TransferStatus) {
		http.Error(w, fmt.Sprintf("Invalid transfer_status code. Must be one of %v.", acceptableTransferStatuses), http.StatusBadRequest)
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
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// Deny requests which do not change the current transfer status.
	if dbCommitment.TransferStatus == req.TransferStatus {
		http.Error(w, "transfer_status is already set to desired value", http.StatusBadRequest)
		return
	}

	if req.TransferStatus != limesresources.CommitmentTransferStatusNone {
		// In order to prevent confusion, only commitments in a certain status can be marked as transferable.
		if slices.Contains([]liquid.CommitmentStatus{liquid.CommitmentStatusSuperseded, liquid.CommitmentStatusExpired}, dbCommitment.Status) {
			http.Error(w, "expired or superseded commitments cannot be transferred", http.StatusBadRequest)
			return
		}

		// Deny requests with a greater amount than the commitment.
		if req.Amount > dbCommitment.Amount {
			http.Error(w, "delivered amount exceeds the commitment amount.", http.StatusBadRequest)
			return
		}
	}

	var (
		loc            core.AZResourceLocation
		totalConfirmed uint64
	)
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID, dbProject.ID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone, &totalConfirmed)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	// when moving into CommitmentTransferStatusNone, the token is cleared;
	// otherwise a new token is generated and filled in for the transfer
	transferToken := None[string]()
	transferStartedAt := None[time.Time]()
	if req.TransferStatus != limesresources.CommitmentTransferStatusNone {
		transferToken = Some(p.generateTransferToken())
		transferStartedAt = Some(p.timeNow())
	}

	// Mark whole commitment or a newly created, split one as transferable.
	tx, err := p.DB.Begin()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// if not split, the ccr is just used for audit logging
	ccr := liquid.CommitmentChangeRequest{
		AZ:          loc.AvailabilityZone,
		InfoVersion: serviceInfo.Version,
		ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
			dbProject.UUID: {
				ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(*dbProject, *dbDomain),
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					loc.ResourceName: {
						TotalConfirmedBefore: totalConfirmed,
						TotalConfirmedAfter:  totalConfirmed,
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
					},
				},
			},
		},
	}
	cac := map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset{}

	if req.Amount == dbCommitment.Amount || req.TransferStatus == limesresources.CommitmentTransferStatusNone {
		rcr := ccr.ByProject[dbProject.UUID].ByResource[loc.ResourceName]
		rcr.Commitments = []liquid.Commitment{
			// unchanged
			{
				UUID:      dbCommitment.UUID,
				OldStatus: Some(dbCommitment.Status),
				NewStatus: Some(dbCommitment.Status),
				Amount:    dbCommitment.Amount,
				ConfirmBy: dbCommitment.ConfirmBy,
				ExpiresAt: dbCommitment.ExpiresAt,
			},
		}
		ccr.ByProject[dbProject.UUID].ByResource[loc.ResourceName] = rcr
		cac[dbCommitment.UUID] = audit.CommitmentAttributeChangeset{
			OldTransferStatus: dbCommitment.TransferStatus,
			NewTransferStatus: req.TransferStatus,
		}

		dbCommitment.TransferStatus = req.TransferStatus
		dbCommitment.TransferToken = transferToken
		dbCommitment.TransferStartedAt = transferStartedAt
		_, err = tx.Update(&dbCommitment)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
	} else {
		now := p.timeNow()
		transferAmount := req.Amount
		remainingAmount := dbCommitment.Amount - req.Amount
		transferCommitment, err := datamodel.BuildSplitCommitment(dbCommitment, transferAmount, p.timeNow(), p.generateProjectCommitmentUUID)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
		transferCommitment.TransferStatus = req.TransferStatus
		transferCommitment.TransferToken = transferToken
		transferCommitment.TransferStartedAt = transferStartedAt
		remainingCommitment, err := datamodel.BuildSplitCommitment(dbCommitment, remainingAmount, p.timeNow(), p.generateProjectCommitmentUUID)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
		err = tx.Insert(&transferCommitment)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
		err = tx.Insert(&remainingCommitment)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}

		rcr := ccr.ByProject[dbProject.UUID].ByResource[loc.ResourceName]
		rcr.Commitments = []liquid.Commitment{
			// old
			{
				UUID:      dbCommitment.UUID,
				OldStatus: Some(dbCommitment.Status),
				NewStatus: Some(liquid.CommitmentStatusSuperseded),
				Amount:    dbCommitment.Amount,
				ConfirmBy: dbCommitment.ConfirmBy,
				ExpiresAt: dbCommitment.ExpiresAt,
			},
			// new
			{
				UUID:      transferCommitment.UUID,
				OldStatus: None[liquid.CommitmentStatus](),
				NewStatus: Some(transferCommitment.Status),
				Amount:    transferCommitment.Amount,
				ConfirmBy: transferCommitment.ConfirmBy,
				ExpiresAt: transferCommitment.ExpiresAt,
			},
			{
				UUID:      remainingCommitment.UUID,
				OldStatus: None[liquid.CommitmentStatus](),
				NewStatus: Some(remainingCommitment.Status),
				Amount:    remainingCommitment.Amount,
				ConfirmBy: remainingCommitment.ConfirmBy,
				ExpiresAt: remainingCommitment.ExpiresAt,
			},
		}
		ccr.ByProject[dbProject.UUID].ByResource[loc.ResourceName] = rcr
		cac[transferCommitment.UUID] = audit.CommitmentAttributeChangeset{
			OldTransferStatus: limesresources.CommitmentTransferStatusNone,
			NewTransferStatus: req.TransferStatus,
		}

		_, err = datamodel.DelegateChangeCommitments(r.Context(), p.Cluster, ccr, loc, serviceInfo, tx)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}

		supersedeContext := db.CommitmentWorkflowContext{
			Reason:                 db.CommitmentReasonSplit,
			RelatedCommitmentIDs:   []db.ProjectCommitmentID{transferCommitment.ID, remainingCommitment.ID},
			RelatedCommitmentUUIDs: []liquid.CommitmentUUID{transferCommitment.UUID, remainingCommitment.UUID},
		}
		buf, err := json.Marshal(supersedeContext)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
		dbCommitment.Status = liquid.CommitmentStatusSuperseded
		dbCommitment.SupersededAt = Some(now)
		dbCommitment.SupersedeContextJSON = Some(json.RawMessage(buf))
		_, err = tx.Update(&dbCommitment)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}

		dbCommitment = transferCommitment
	}
	err = tx.Commit()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
	c := datamodel.ConvertCommitmentToDisplayForm(dbCommitment, loc.AvailabilityZone, p.Cluster.BehaviorForResourceLocation(loc).IdentityInV1API, datamodel.CanDeleteCommitment(token, dbCommitment, p.timeNow), resourceInfo.Unit)

	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest:       ccr,
		CommitmentAttributeChangesets: cac,
	}.ReplicateForAllProjects(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusAccepted,
		Action:     cadf.UpdateAction,
	}, None[cadf.Action](), None[liquid.ProjectUUID]())
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}
	respondwith.JSON(w, http.StatusAccepted, map[string]any{"commitment": c})
}

func (p *v1Provider) buildConvertedCommitment(dbCommitment db.ProjectCommitment, azResourceID db.AZResourceID, amount uint64) (db.ProjectCommitment, error) {
	now := p.timeNow()
	creationContext := db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonConvert,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{dbCommitment.ID},
		RelatedCommitmentUUIDs: []liquid.CommitmentUUID{dbCommitment.UUID},
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
		Status:              dbCommitment.Status,
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
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	var (
		loc            core.AZResourceLocation
		totalConfirmed uint64
	)
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID, dbCommitment.ProjectID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone, &totalConfirmed)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "location data not found.", http.StatusNotFound)
		return
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
	c := datamodel.ConvertCommitmentToDisplayForm(dbCommitment, loc.AvailabilityZone, p.Cluster.BehaviorForResourceLocation(loc).IdentityInV1API, datamodel.CanDeleteCommitment(token, dbCommitment, p.timeNow), resourceInfo.Unit)
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
	targetDomain := p.FindDomainFromRequest(w, r)
	if targetDomain == nil {
		return
	}
	targetProject := p.FindProjectFromRequest(w, r, targetDomain)
	if targetProject == nil {
		return
	}

	// find commitment by transfer_token
	var dbCommitment db.ProjectCommitment
	err := p.DB.SelectOne(&dbCommitment, getCommitmentWithMatchingTransferTokenQuery, commitmentID, transferToken)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no matching commitment found", http.StatusNotFound)
		return
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	var (
		loc                  core.AZResourceLocation
		sourceTotalConfirmed uint64
	)
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID, dbCommitment.ProjectID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone, &sourceTotalConfirmed)

	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// get old project additionally
	var sourceProject db.Project
	err = p.DB.SelectOne(&sourceProject, `SELECT * FROM projects WHERE id = $1`, dbCommitment.ProjectID)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	var sourceDomain db.Domain
	err = p.DB.SelectOne(&sourceDomain, `SELECT * FROM domains WHERE id = $1`, sourceProject.DomainID)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// check that the target project allows commitments at all
	var (
		azResourceID              db.AZResourceID
		resourceAllowsCommitments bool
		targetTotalConfirmed      uint64
	)
	err = p.DB.QueryRow(findAZResourceIDByLocationQuery, targetProject.ID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone).
		Scan(&azResourceID, &resourceAllowsCommitments, &targetTotalConfirmed)
	if respondwith.ObfuscatedErrorText(w, err) {
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
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	sourceTotalConfirmedAfter := sourceTotalConfirmed
	targetTotalConfirmedAfter := targetTotalConfirmed
	if dbCommitment.Status == liquid.CommitmentStatusConfirmed {
		sourceTotalConfirmedAfter -= dbCommitment.Amount
		targetTotalConfirmedAfter += dbCommitment.Amount
	}

	// check move is allowed
	cac := map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset{
		dbCommitment.UUID: {
			OldTransferStatus: dbCommitment.TransferStatus,
			NewTransferStatus: limesresources.CommitmentTransferStatusNone,
		},
	}
	ccr := liquid.CommitmentChangeRequest{
		AZ:          loc.AvailabilityZone,
		InfoVersion: serviceInfo.Version,
		ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
			sourceProject.UUID: {
				ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(sourceProject, sourceDomain),
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					loc.ResourceName: {
						TotalConfirmedBefore: sourceTotalConfirmed,
						TotalConfirmedAfter:  sourceTotalConfirmedAfter,
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
						Commitments: []liquid.Commitment{
							{
								UUID:      dbCommitment.UUID,
								OldStatus: Some(dbCommitment.Status),
								NewStatus: None[liquid.CommitmentStatus](),
								Amount:    dbCommitment.Amount,
								ConfirmBy: dbCommitment.ConfirmBy,
								ExpiresAt: dbCommitment.ExpiresAt,
							},
						},
					},
				},
			},
			targetProject.UUID: {
				ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(*targetProject, *targetDomain),
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					loc.ResourceName: {
						TotalConfirmedBefore: targetTotalConfirmed,
						TotalConfirmedAfter:  targetTotalConfirmedAfter,
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
						Commitments: []liquid.Commitment{
							{
								UUID:      dbCommitment.UUID,
								OldStatus: None[liquid.CommitmentStatus](),
								NewStatus: Some(dbCommitment.Status),
								Amount:    dbCommitment.Amount,
								ConfirmBy: dbCommitment.ConfirmBy,
								ExpiresAt: dbCommitment.ExpiresAt,
							},
						},
					},
				},
			},
		},
	}
	commitmentChangeResponse, err := datamodel.DelegateChangeCommitments(r.Context(), p.Cluster, ccr, loc, serviceInfo, tx)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	if commitmentChangeRequestWasRejected(commitmentChangeResponse, w, true) {
		return
	}

	// TODO: counter metric for moves by transfer_status (to see if the marketplace has any impact)

	dbCommitment.TransferStatus = ""
	dbCommitment.TransferToken = None[string]()
	dbCommitment.ProjectID = targetProject.ID
	_, err = tx.Update(&dbCommitment)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	err = tx.Commit()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
	c := datamodel.ConvertCommitmentToDisplayForm(dbCommitment, loc.AvailabilityZone, p.Cluster.BehaviorForResourceLocation(loc).IdentityInV1API, datamodel.CanDeleteCommitment(token, dbCommitment, p.timeNow), resourceInfo.Unit)

	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest:       ccr,
		CommitmentAttributeChangesets: cac,
	}.ReplicateForAllProjects(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusAccepted,
		Action:     cadf.UpdateAction,
	}, None[cadf.Action](), None[liquid.ProjectUUID]())
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}

	respondwith.JSON(w, http.StatusAccepted, map[string]any{"commitment": c})
}

// GetCommitmentConversions handles GET /v1/commitment-conversion/{service_type}/{resource_name}
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
	if respondwith.ObfuscatedErrorText(w, err) {
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
		http.Error(w, "no commitment_id provided", http.StatusBadRequest)
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
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	var (
		sourceLoc            core.AZResourceLocation
		sourceTotalConfirmed uint64
	)
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID, dbProject.ID).
		Scan(&sourceLoc.ServiceType, &sourceLoc.ResourceName, &sourceLoc.AvailabilityZone, &sourceTotalConfirmed)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ObfuscatedErrorText(w, err) {
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
	if respondwith.ObfuscatedErrorText(w, err) {
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
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	var (
		targetAZResourceID        db.AZResourceID
		resourceAllowsCommitments bool
		targetTotalConfirmed      uint64
	)
	err = p.DB.QueryRow(findAZResourceIDByLocationQuery, dbProject.ID, targetServiceType, targetResourceName, sourceLoc.AvailabilityZone).
		Scan(&targetAZResourceID, &resourceAllowsCommitments, &targetTotalConfirmed)
	if respondwith.ObfuscatedErrorText(w, err) {
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
	// do not allow conversions on commitments in transfer
	if dbCommitment.TransferStatus != limesresources.CommitmentTransferStatusNone {
		http.Error(w, "commitments in transfer cannot be converted", http.StatusUnprocessableEntity)
		return
	}
	targetLoc := core.AZResourceLocation{
		ServiceType:      sourceLoc.ServiceType,
		ResourceName:     targetResourceName,
		AvailabilityZone: sourceLoc.AvailabilityZone,
	}
	serviceInfo := core.InfoForService(serviceInfos, sourceLoc.ServiceType)
	remainingAmount := dbCommitment.Amount - req.SourceAmount
	var remainingCommitment db.ProjectCommitment

	// old commitment is always superseded
	sourceCommitments := []liquid.Commitment{
		{
			UUID:      dbCommitment.UUID,
			OldStatus: Some(dbCommitment.Status),
			NewStatus: Some(liquid.CommitmentStatusSuperseded),
			Amount:    dbCommitment.Amount,
			ConfirmBy: dbCommitment.ConfirmBy,
			ExpiresAt: dbCommitment.ExpiresAt,
		},
	}
	// when there is a remaining amount, we must request to add this
	if remainingAmount > 0 {
		remainingCommitment, err = datamodel.BuildSplitCommitment(dbCommitment, remainingAmount, p.timeNow(), p.generateProjectCommitmentUUID)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
		sourceCommitments = append(sourceCommitments, liquid.Commitment{
			UUID:      remainingCommitment.UUID,
			OldStatus: None[liquid.CommitmentStatus](),
			NewStatus: Some(remainingCommitment.Status),
			Amount:    remainingCommitment.Amount,
			ConfirmBy: remainingCommitment.ConfirmBy,
			ExpiresAt: remainingCommitment.ExpiresAt,
		})
	}
	convertedCommitment, err := p.buildConvertedCommitment(dbCommitment, targetAZResourceID, conversionAmount)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	sourceTotalConfirmedAfter := sourceTotalConfirmed
	targetTotalConfirmedAfter := targetTotalConfirmed
	if dbCommitment.ConfirmedAt.IsSome() {
		sourceTotalConfirmedAfter -= req.SourceAmount
		targetTotalConfirmedAfter += req.TargetAmount
	}

	ccr := liquid.CommitmentChangeRequest{
		AZ:          sourceLoc.AvailabilityZone,
		InfoVersion: serviceInfo.Version,
		ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
			dbProject.UUID: {
				ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(*dbProject, *dbDomain),
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					sourceLoc.ResourceName: {
						TotalConfirmedBefore: sourceTotalConfirmed,
						TotalConfirmedAfter:  sourceTotalConfirmedAfter,
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
						Commitments:           sourceCommitments,
					},
					targetLoc.ResourceName: {
						TotalConfirmedBefore: targetTotalConfirmed,
						TotalConfirmedAfter:  targetTotalConfirmedAfter,
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
						Commitments: []liquid.Commitment{
							{
								UUID:      convertedCommitment.UUID,
								OldStatus: None[liquid.CommitmentStatus](),
								NewStatus: Some(convertedCommitment.Status),
								Amount:    convertedCommitment.Amount,
								ConfirmBy: convertedCommitment.ConfirmBy,
								ExpiresAt: convertedCommitment.ExpiresAt,
							},
						},
					},
				},
			},
		},
	}
	commitmentChangeResponse, err := datamodel.DelegateChangeCommitments(r.Context(), p.Cluster, ccr, sourceLoc, serviceInfo, tx)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// only check acceptance by liquid when old commitment was confirmed, unconfirmed commitments can be moved without acceptance
	if ccr.RequiresConfirmation() && commitmentChangeRequestWasRejected(commitmentChangeResponse, w, true) {
		return
	}

	var (
		relatedCommitmentIDs   []db.ProjectCommitmentID
		relatedCommitmentUUIDs []liquid.CommitmentUUID
	)
	resourceInfo := core.InfoForResource(serviceInfo, sourceLoc.ResourceName)
	if remainingAmount > 0 {
		relatedCommitmentIDs = append(relatedCommitmentIDs, remainingCommitment.ID)
		relatedCommitmentUUIDs = append(relatedCommitmentUUIDs, remainingCommitment.UUID)
		err = tx.Insert(&remainingCommitment)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
	}

	relatedCommitmentIDs = append(relatedCommitmentIDs, convertedCommitment.ID)
	relatedCommitmentUUIDs = append(relatedCommitmentUUIDs, convertedCommitment.UUID)
	err = tx.Insert(&convertedCommitment)
	if respondwith.ObfuscatedErrorText(w, err) {
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
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	dbCommitment.Status = liquid.CommitmentStatusSuperseded
	dbCommitment.SupersededAt = Some(now)
	dbCommitment.SupersedeContextJSON = Some(json.RawMessage(buf))
	_, err = tx.Update(&dbCommitment)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	err = tx.Commit()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	c := datamodel.ConvertCommitmentToDisplayForm(convertedCommitment, targetLoc.AvailabilityZone, p.Cluster.BehaviorForResourceLocation(targetLoc).IdentityInV1API, datamodel.CanDeleteCommitment(token, convertedCommitment, p.timeNow), resourceInfo.Unit)

	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest: ccr,
	}.ReplicateForAllProjects(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusAccepted,
		Action:     cadf.UpdateAction,
	}, None[cadf.Action](), None[liquid.ProjectUUID]())
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}
	respondwith.JSON(w, http.StatusAccepted, map[string]any{"commitment": c})
}

// UpdateCommitmentDuration handles POST /v1/domains/{domain_id}/projects/{project_id}/commitments/{commitment_id}/update-duration
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
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	now := p.timeNow()
	if dbCommitment.ExpiresAt.Before(now) || dbCommitment.ExpiresAt.Equal(now) {
		http.Error(w, "unable to process expired commitment", http.StatusForbidden)
		return
	}

	if dbCommitment.Status == liquid.CommitmentStatusSuperseded {
		msg := fmt.Sprintf("unable to operate on commitment with a status of %s", dbCommitment.Status)
		http.Error(w, msg, http.StatusForbidden)
		return
	}

	var (
		loc            core.AZResourceLocation
		totalConfirmed uint64
	)
	err = p.DB.QueryRow(findAZResourceLocationByIDQuery, dbCommitment.AZResourceID, dbProject.ID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone, &totalConfirmed)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ObfuscatedErrorText(w, err) {
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

	maybeServiceInfo, err := p.Cluster.InfoForService(loc.ServiceType)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	// might only reject in the remote-case, locally we accept extensions as limes does not know future capacity
	ccr := liquid.CommitmentChangeRequest{
		AZ:          loc.AvailabilityZone,
		InfoVersion: serviceInfo.Version,
		ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
			dbProject.UUID: {
				ProjectMetadata: datamodel.LiquidProjectMetadataFromDBProject(*dbProject, *dbDomain),
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					loc.ResourceName: {
						TotalConfirmedBefore: totalConfirmed,
						TotalConfirmedAfter:  totalConfirmed,
						// TODO: change when introducing "guaranteed" commitments
						TotalGuaranteedBefore: 0,
						TotalGuaranteedAfter:  0,
						Commitments: []liquid.Commitment{
							{
								UUID:         dbCommitment.UUID,
								OldStatus:    Some(dbCommitment.Status),
								NewStatus:    Some(dbCommitment.Status),
								Amount:       dbCommitment.Amount,
								ConfirmBy:    dbCommitment.ConfirmBy,
								ExpiresAt:    newExpiresAt,
								OldExpiresAt: Some(dbCommitment.ExpiresAt.Local()),
							},
						},
					},
				},
			},
		},
	}
	commitmentChangeResponse, err := datamodel.DelegateChangeCommitments(r.Context(), p.Cluster, ccr, loc, serviceInfo, p.DB)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	dbCommitment.Duration = req.Duration
	dbCommitment.ExpiresAt = newExpiresAt
	if commitmentChangeRequestWasRejected(commitmentChangeResponse, w, true) {
		return
	}

	_, err = p.DB.Update(&dbCommitment)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	resourceInfo := core.InfoForResource(serviceInfo, loc.ResourceName)
	c := datamodel.ConvertCommitmentToDisplayForm(dbCommitment, loc.AvailabilityZone, p.Cluster.BehaviorForResourceLocation(loc).IdentityInV1API, datamodel.CanDeleteCommitment(token, dbCommitment, p.timeNow), resourceInfo.Unit)

	auditEvents := audit.CommitmentEventTarget{
		CommitmentChangeRequest: ccr,
	}.ReplicateForAllProjects(audittools.Event{
		Time:       p.timeNow(),
		Request:    r,
		User:       token,
		ReasonCode: http.StatusOK,
		Action:     cadf.UpdateAction,
	}, None[cadf.Action](), None[liquid.ProjectUUID]())
	for _, event := range auditEvents {
		p.auditor.Record(event)
	}

	respondwith.JSON(w, http.StatusOK, map[string]any{"commitment": c})
}

func commitmentChangeRequestWasRejected(response liquid.CommitmentChangeResponse, w http.ResponseWriter, withHTTPResponse bool) bool {
	if response.RejectionReason == "" {
		return false
	}
	if retryAt, exists := response.RetryAt.Unpack(); exists {
		w.Header().Set("Retry-After", retryAt.Format(time.RFC1123))
	}
	if withHTTPResponse {
		http.Error(w, response.RejectionReason, http.StatusConflict)
		return true // gocritic fears missing return otherwise
	}
	return true
}

// DeleteProjectCommitmentAsCloudAdmin handles DELETE /v1/commitments/:id.
func (p *v1Provider) DeleteProjectCommitmentAsCloudAdmin(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/commitments/:id")
	if !p.prepareClusterAdminCommitmentEditRequest(w, r) {
		return
	}
	p.DeleteProjectCommitment(w, r)
}

// StartCommitmentTransferAsCloudAdmin handles POST /v1/commitments/:id/start-transfer
func (p *v1Provider) StartCommitmentTransferAsCloudAdmin(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/commitments/:id/start-transfer")
	if !p.prepareClusterAdminCommitmentEditRequest(w, r) {
		return
	}
	p.StartCommitmentTransfer(w, r)
}

func (p *v1Provider) prepareClusterAdminCommitmentEditRequest(w http.ResponseWriter, r *http.Request) bool {
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:edit") {
		return false
	}
	commitmentID := mux.Vars(r)["id"]
	if commitmentID == "" {
		http.Error(w, "commitment ID missing", http.StatusBadRequest)
		return false
	}
	var request struct {
		ProjectUUID liquid.ProjectUUID `db:"projectUUID"`
		DomainUUID  string             `db:"domainUUID"`
	}
	err := p.DB.SelectOne(&request, `SELECT p.uuid projectUUID, d.uuid domainUUID FROM project_commitments pc JOIN projects p ON p.id = pc.project_id JOIN domains d ON d.id = p.domain_id where pc.id = $1`, commitmentID)
	if err != nil {
		http.Error(w, "unable to resolve the requested commitment", http.StatusBadRequest)
		return false
	}
	mux.Vars(r)["project_id"] = string(request.ProjectUUID)
	mux.Vars(r)["domain_id"] = request.DomainUUID
	return true
}
