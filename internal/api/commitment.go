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
	"time"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
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
	`)

	findProjectCommitmentByIDQuery = sqlext.SimplifyWhitespace(`
		SELECT pc.*
		  FROM project_commitments pc
		  JOIN project_az_resources par ON pc.az_resource_id = par.id
		  JOIN project_resources pr ON par.resource_id = pr.id
		  JOIN project_services ps ON pr.service_id = ps.id
		 WHERE pc.id = $1 AND ps.project_id = $2
	`)

	findProjectAZResourceIDByLocationQuery = sqlext.SimplifyWhitespace(`
		SELECT par.id
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
	getCommitmentWithMatchingTransferToken = sqlext.SimplifyWhitespace(`
		SELECT * FROM project_commitments WHERE transfer_token = $1
	`)
	findTargetAZResourceIDBySourceID = sqlext.SimplifyWhitespace(`
	  SELECT par.id 
	    FROM project_az_resources as par
		JOIN project_resources pr ON par.resource_id = pr.id
	    JOIN project_services ps ON pr.service_id = ps.id
	  WHERE ps.project_id = $1 AND az = (
		SELECT az 
		  FROM project_az_resources
		  WHERE id = $2
	  ) 
	`)

	forceImmediateCapacityScrapeQuery = sqlext.SimplifyWhitespace(`
		UPDATE cluster_capacitors SET next_scrape_at = $1 WHERE capacitor_id = (
			SELECT capacitor_id FROM cluster_services cs JOIN cluster_resources cr ON cs.id = cr.service_id
			WHERE cs.type = $2 AND cr.name = $3
		)
	`)
	updateCommitmentTransferState = sqlext.SimplifyWhitespace(`
		UPDATE project_commitments SET transfer_status = $1, transfer_token = $2 WHERE id = $3
	`)
	updateCommitmentSuperseded = sqlext.SimplifyWhitespace(`
		UPDATE project_commitments SET superseded_at = $1 WHERE id = $2
	`)
	updateCommitmentAZResourceID = sqlext.SimplifyWhitespace(`
		UPDATE project_commitments SET az_resource_id = $1 WHERE id = $2
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

	//enumerate project AZ resources
	filter := reports.ReadFilter(r, p.Cluster.GetServiceTypesForArea)
	queryStr, joinArgs := filter.PrepareQuery(getProjectAZResourceLocationsQuery)
	azResourceLocationsByID := make(map[db.ProjectAZResourceID]azResourceLocation)
	err := sqlext.ForeachRow(p.DB, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			id  db.ProjectAZResourceID
			loc azResourceLocation
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

	//enumerate relevant project commitments
	queryStr, joinArgs = filter.PrepareQuery(getProjectCommitmentsQuery)
	whereStr, whereArgs := db.BuildSimpleWhereClause(map[string]any{"ps.project_id": dbProject.ID}, len(joinArgs))
	var dbCommitments []db.ProjectCommitment
	_, err = p.DB.Select(&dbCommitments, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...)...)
	if respondwith.ErrorText(w, err) {
		return
	}

	//render response
	result := make([]limesresources.Commitment, 0, len(dbCommitments))
	for _, c := range dbCommitments {
		loc, exists := azResourceLocationsByID[c.AZResourceID]
		if !exists {
			// defense in depth (the DB should not change that much between those two queries above)
			continue
		}
		result = append(result, p.convertCommitmentToDisplayForm(c, loc))
	}

	respondwith.JSON(w, http.StatusOK, map[string]any{"commitments": result})
}

type azResourceLocation struct {
	ServiceType      string
	ResourceName     string
	AvailabilityZone limes.AvailabilityZone
}

func (p *v1Provider) convertCommitmentToDisplayForm(c db.ProjectCommitment, loc azResourceLocation) limesresources.Commitment {
	resInfo := p.Cluster.InfoForResource(loc.ServiceType, loc.ResourceName)
	return limesresources.Commitment{
		ID:               int64(c.ID),
		ServiceType:      loc.ServiceType,
		ResourceName:     loc.ResourceName,
		AvailabilityZone: loc.AvailabilityZone,
		Amount:           c.Amount,
		Unit:             resInfo.Unit,
		Duration:         c.Duration,
		CreatedAt:        limes.UnixEncodedTime{Time: c.CreatedAt},
		CreatorUUID:      c.CreatorUUID,
		CreatorName:      c.CreatorName,
		ConfirmBy:        maybeUnixEncodedTime(c.ConfirmBy),
		ConfirmedAt:      maybeUnixEncodedTime(c.ConfirmedAt),
		ExpiresAt:        limes.UnixEncodedTime{Time: c.ExpiresAt},
		TransferStatus:   c.TransferStatus,
		TransferToken:    c.TransferToken,
	}
}

func (p *v1Provider) parseAndValidateCommitmentRequest(w http.ResponseWriter, r *http.Request) (*limesresources.CommitmentRequest, *core.ResourceBehavior) {
	//parse request
	var parseTarget struct {
		Request limesresources.CommitmentRequest `json:"commitment"`
	}
	if !RequireJSON(w, r, &parseTarget) {
		return nil, nil
	}
	req := parseTarget.Request

	//validate request
	if !p.Cluster.HasService(req.ServiceType) {
		http.Error(w, "no such service", http.StatusUnprocessableEntity)
		return nil, nil
	}
	if !p.Cluster.HasResource(req.ServiceType, req.ResourceName) {
		http.Error(w, "no such resource", http.StatusUnprocessableEntity)
		return nil, nil
	}
	behavior := p.Cluster.BehaviorForResource(req.ServiceType, req.ResourceName, "")
	if len(behavior.CommitmentDurations) == 0 {
		http.Error(w, "commitments are not enabled for this resource", http.StatusUnprocessableEntity)
		return nil, nil
	}
	if behavior.CommitmentIsAZAware {
		if !slices.Contains(p.Cluster.Config.AvailabilityZones, req.AvailabilityZone) {
			http.Error(w, "no such availability zone", http.StatusUnprocessableEntity)
			return nil, nil
		}
	} else {
		if req.AvailabilityZone != limes.AvailabilityZoneAny {
			http.Error(w, `resource does not accept AZ-aware commitments, so the AZ must be set to "any"`, http.StatusUnprocessableEntity)
			return nil, nil
		}
	}
	if !slices.Contains(behavior.CommitmentDurations, req.Duration) {
		buf := must.Return(json.Marshal(behavior.CommitmentDurations)) //panic on error is acceptable here, marshals should never fail
		msg := "unacceptable commitment duration for this resource, acceptable values: " + string(buf)
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return nil, nil
	}
	if req.Amount == 0 {
		http.Error(w, "amount of committed resource must be greater than zero", http.StatusUnprocessableEntity)
		return nil, nil
	}

	return &req, &behavior
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
	req, behavior := p.parseAndValidateCommitmentRequest(w, r)
	if req == nil {
		return
	}

	//commitments can never be confirmed immediately if we are before the min_confirm_date
	now := p.timeNow()
	if behavior.CommitmentMinConfirmDate != nil && behavior.CommitmentMinConfirmDate.After(now) {
		respondwith.JSON(w, http.StatusOK, map[string]bool{"result": false})
		return
	}

	//check for committable capacity
	result, err := datamodel.CanConfirmNewCommitment(*req, *dbProject, p.Cluster, p.DB)
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
	req, behavior := p.parseAndValidateCommitmentRequest(w, r)
	if req == nil {
		return
	}

	loc := azResourceLocation{
		ServiceType:      req.ServiceType,
		ResourceName:     req.ResourceName,
		AvailabilityZone: req.AvailabilityZone,
	}
	var azResourceID db.ProjectAZResourceID
	err := p.DB.QueryRow(findProjectAZResourceIDByLocationQuery, dbProject.ID, req.ServiceType, req.ResourceName, req.AvailabilityZone).
		Scan(&azResourceID)
	if respondwith.ErrorText(w, err) {
		return
	}

	//if given, confirm_by must definitely after time.Now(), and also after the MinConfirmDate if configured
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

	//we want to validate committable capacity in the same transaction that creates the commitment
	tx, err := p.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	//prepare commitment
	confirmBy := maybeUnpackUnixEncodedTime(req.ConfirmBy)
	dbCommitment := db.ProjectCommitment{
		AZResourceID: azResourceID,
		Amount:       req.Amount,
		Duration:     req.Duration,
		CreatedAt:    now,
		CreatorUUID:  token.UserUUID(),
		CreatorName:  fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
		ConfirmBy:    confirmBy,
		ConfirmedAt:  nil, //may be set below
		ExpiresAt:    req.Duration.AddTo(unwrapOrDefault(confirmBy, now)),
	}
	if req.ConfirmBy == nil {
		//if not planned for confirmation in the future, confirm immediately (or fail)
		ok, err := datamodel.CanConfirmNewCommitment(*req, *dbProject, p.Cluster, tx)
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

	//create commitment
	err = tx.Insert(&dbCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}
	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}
	logAndPublishEvent(now, r, token, http.StatusCreated, commitmentEventTarget{
		DomainID:    dbDomain.UUID,
		DomainName:  dbDomain.Name,
		ProjectID:   dbProject.UUID,
		ProjectName: dbProject.Name,
		Commitment:  p.convertCommitmentToDisplayForm(dbCommitment, loc),
	})

	//if the commitment is immediately confirmed, trigger a capacity scrape in
	//order to ApplyComputedProjectQuotas based on the new commitment
	if dbCommitment.ConfirmedAt != nil {
		_, err := p.DB.Exec(forceImmediateCapacityScrapeQuery, now, loc.ServiceType, loc.ResourceName)
		if respondwith.ErrorText(w, err) {
			return
		}
	}

	//display the possibly confirmed commitment to the user
	err = p.DB.SelectOne(&dbCommitment, `SELECT * FROM project_commitments WHERE id = $1`, dbCommitment.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	c := p.convertCommitmentToDisplayForm(dbCommitment, loc)
	respondwith.JSON(w, http.StatusCreated, map[string]any{"commitment": c})
}

// DeleteProjectCommitment handles DELETE /v1/domains/:domain_id/projects/:project_id/commitments/:id.
func (p *v1Provider) DeleteProjectCommitment(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/commitments/:id")
	token := p.CheckToken(r)
	if !token.Require(w, "project:uncommit") {
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

	//load commitment
	var dbCommitment db.ProjectCommitment
	err := p.DB.SelectOne(&dbCommitment, findProjectCommitmentByIDQuery, mux.Vars(r)["id"], dbProject.ID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}
	var loc azResourceLocation
	err = p.DB.QueryRow(findProjectAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		//defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	//perform deletion
	_, err = p.DB.Delete(&dbCommitment)
	if respondwith.ErrorText(w, err) {
		return
	}

	logAndPublishEvent(p.timeNow(), r, token, http.StatusCreated, commitmentEventTarget{
		DomainID:    dbDomain.UUID,
		DomainName:  dbDomain.Name,
		ProjectID:   dbProject.UUID,
		ProjectName: dbProject.Name,
		Commitment:  p.convertCommitmentToDisplayForm(dbCommitment, loc),
	})
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
		http.Error(w, "project not found.", http.StatusNotFound)
		return
	}
	var parseTarget struct {
		Request limesresources.Commitment `json:"commitment"`
	}
	if !RequireJSON(w, r, &parseTarget) {
		return
	}
	req := parseTarget.Request

	if req.TransferStatus != limesresources.CommitmentTransferStatusUnlisted && req.TransferStatus != limesresources.CommitmentTransferStatusPublic {
		respondwith.JSON(w, http.StatusBadRequest, fmt.Sprintf("Invalid transfer_status code. Must be %s or %s.", limesresources.CommitmentTransferStatusUnlisted, limesresources.CommitmentTransferStatusPublic))
		return
	}

	if req.Amount <= 0 {
		respondwith.JSON(w, http.StatusBadRequest, "Delivered amount needs to be a positive value.")
		return
	}

	//load commitment
	var dbCommitment db.ProjectCommitment
	err := p.DB.SelectOne(&dbCommitment, findProjectCommitmentByIDQuery, mux.Vars(r)["id"], dbProject.ID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no such commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	// reject commitments that are not confirmed yet.
	if dbCommitment.ConfirmedAt == nil {
		http.Error(w, "commitment needs to be confirmed in order to transfer it.", http.StatusUnprocessableEntity)
		return
	}

	// Mark whole commitment or a newly created, splitted one as transferrable.
	tx, err := p.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)
	transferToken := p.generateTransferToken()
	if req.Amount >= dbCommitment.Amount {
		_, err = tx.Exec(updateCommitmentTransferState, req.TransferStatus, transferToken, dbCommitment.ID)
		if respondwith.ErrorText(w, err) {
			return
		}
	} else {
		now := p.timeNow()
		transferAmount := req.Amount
		remainingAmount := dbCommitment.Amount - req.Amount
		transferCommitment := p.splitCommitment(dbCommitment, transferAmount)
		remainingCommitment := p.splitCommitment(dbCommitment, remainingAmount)
		err = tx.Insert(&transferCommitment)
		if respondwith.ErrorText(w, err) {
			return
		}
		err = tx.Insert(&remainingCommitment)
		if respondwith.ErrorText(w, err) {
			return
		}
		_, err = tx.Exec(updateCommitmentSuperseded, now, dbCommitment.ID)
		if respondwith.ErrorText(w, err) {
			return
		}
		dbCommitment = transferCommitment
	}
	dbCommitment.TransferStatus = req.TransferStatus
	dbCommitment.TransferToken = transferToken
	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	var loc azResourceLocation
	err = p.DB.QueryRow(findProjectAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		//defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	c := p.convertCommitmentToDisplayForm(dbCommitment, loc)
	logAndPublishEvent(p.timeNow(), r, token, http.StatusAccepted, commitmentEventTarget{
		DomainID:    dbDomain.UUID,
		DomainName:  dbDomain.Name,
		ProjectID:   dbProject.UUID,
		ProjectName: dbProject.Name,
		Commitment:  c,
	})
	respondwith.JSON(w, http.StatusAccepted, map[string]any{"commitment": c})
}

func (p *v1Provider) splitCommitment(dbCommitment db.ProjectCommitment, amount uint64) db.ProjectCommitment {
	now := p.timeNow()
	return db.ProjectCommitment{
		AZResourceID:  dbCommitment.AZResourceID,
		Amount:        amount,
		Duration:      dbCommitment.Duration,
		CreatedAt:     now,
		CreatorUUID:   dbCommitment.CreatorUUID,
		CreatorName:   dbCommitment.CreatorName,
		ConfirmBy:     dbCommitment.ConfirmBy,
		ConfirmedAt:   dbCommitment.ConfirmedAt,
		ExpiresAt:     dbCommitment.ExpiresAt,
		PredecessorID: &dbCommitment.ID,
	}
}

// TransferCommitment handles POST /v1/domains/{domain_id}/projects/{project_id}/transfer-commitment/{id}?token={token}
func (p *v1Provider) TransferCommitment(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/transfer-commitment/:id?token=:token")
	token := p.CheckToken(r)
	if !token.Require(w, "project:edit") {
		return
	}
	transferToken := r.URL.Query().Get("token")
	if transferToken == "" {
		respondwith.ErrorText(w, errors.New("no transfer token provided"))
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
	err := p.DB.SelectOne(&dbCommitment, getCommitmentWithMatchingTransferToken, transferToken)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "no matching commitment found", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	// get target AZ_RESOURCE_ID
	var targetResourceID db.ProjectAZResourceID
	err = p.DB.QueryRow(findTargetAZResourceIDBySourceID, targetProject.ID, dbCommitment.AZResourceID).Scan(&targetResourceID)
	if respondwith.ErrorText(w, err) {
		return
	}

	tx, err := p.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// update AZ_RESOURCE_ID of commitment
	_, err = tx.Exec(updateCommitmentAZResourceID, targetResourceID, dbCommitment.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	// reset transfer_status and transfer_token
	_, err = tx.Exec(updateCommitmentTransferState, "", "", dbCommitment.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	dbCommitment.TransferStatus = ""
	dbCommitment.TransferToken = ""

	var loc azResourceLocation
	err = p.DB.QueryRow(findProjectAZResourceLocationByIDQuery, dbCommitment.AZResourceID).
		Scan(&loc.ServiceType, &loc.ResourceName, &loc.AvailabilityZone)
	if errors.Is(err, sql.ErrNoRows) {
		//defense in depth: this should not happen because all the relevant tables are connected by FK constraints
		http.Error(w, "no route to this commitment", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	c := p.convertCommitmentToDisplayForm(dbCommitment, loc)
	logAndPublishEvent(p.timeNow(), r, token, http.StatusAccepted, commitmentEventTarget{
		DomainID:    dbDomain.UUID,
		DomainName:  dbDomain.Name,
		ProjectID:   targetProject.UUID,
		ProjectName: targetProject.Name,
		Commitment:  c,
	})

	respondwith.JSON(w, http.StatusAccepted, map[string]any{"commitment": c})
}
