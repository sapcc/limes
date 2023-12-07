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
		  JOIN project_services ps ON pc.service_id = ps.id {{AND ps.type = $service_type}}
		 WHERE %s {{AND pc.resource_name = $resource_name}}
		 ORDER BY pc.id
	`)
	getProjectCommitmentsWhereClause = "ps.project_id = $%d AND pc.superseded_at IS NULL AND (pc.expires_at IS NULL OR pc.expires_at > $%d)"

	findProjectCommitmentByIDQuery = sqlext.SimplifyWhitespace(`
		SELECT pc.*
		  FROM project_commitments pc
		  JOIN project_services ps ON pc.service_id = ps.id
		 WHERE pc.id = $1 AND ps.project_id = $2
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

	//enumerate project services
	serviceTypeByID := make(map[int64]string)
	query := `SELECT id, type FROM project_services WHERE project_id = $1`
	err := sqlext.ForeachRow(p.DB, query, []any{dbProject.ID}, func(rows *sql.Rows) error {
		var (
			serviceID   int64
			serviceType string
		)
		err := rows.Scan(&serviceID, &serviceType)
		serviceTypeByID[serviceID] = serviceType
		return err
	})
	if respondwith.ErrorText(w, err) {
		return
	}

	//enumerate relevant project commitments
	filter := reports.ReadFilter(r, p.Cluster.GetServiceTypesForArea)
	queryStr, joinArgs := filter.PrepareQuery(getProjectCommitmentsQuery)
	whereStr := fmt.Sprintf(getProjectCommitmentsWhereClause, len(joinArgs)+1, len(joinArgs)+2)
	queryStr = fmt.Sprintf(queryStr, whereStr)
	var dbCommitments []db.ProjectCommitment
	_, err = p.DB.Select(&dbCommitments, queryStr, append(joinArgs, dbProject.ID, p.timeNow())...)
	if respondwith.ErrorText(w, err) {
		return
	}

	//render response
	result := make([]limesresources.Commitment, 0, len(dbCommitments))
	for _, c := range dbCommitments {
		serviceType := serviceTypeByID[c.ServiceID]
		if serviceType == "" {
			// defense in depth (the DB should not change that much between those two queries above)
			continue
		}
		if !p.Cluster.HasResource(serviceType, c.ResourceName) {
			//defense in depth
			continue
		}
		result = append(result, p.convertCommitmentToDisplayForm(c, serviceType))
	}

	respondwith.JSON(w, http.StatusOK, map[string]any{"commitments": result})
}

func (p *v1Provider) convertCommitmentToDisplayForm(c db.ProjectCommitment, serviceType string) limesresources.Commitment {
	resInfo := p.Cluster.InfoForResource(serviceType, c.ResourceName)
	return limesresources.Commitment{
		ID:               c.ID,
		ServiceType:      serviceType,
		ResourceName:     c.ResourceName,
		AvailabilityZone: c.AvailabilityZone,
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

func (p *v1Provider) parseAndValidateCommitmentRequest(w http.ResponseWriter, r *http.Request, project db.Project, domain db.Domain) (*limesresources.CommitmentRequest, *core.ResourceBehavior) {
	//parse request
	var parseTarget struct {
		Request limesresources.CommitmentRequest `json:"commitment"`
	}
	if !RequireJSON(w, r, &parseTarget) {
		return nil, nil
	}
	req := parseTarget.Request

	//validate request
	if !slices.Contains(p.Cluster.Config.AvailabilityZones, req.AvailabilityZone) {
		http.Error(w, "no such availability zone", http.StatusUnprocessableEntity)
		return nil, nil
	}
	if !p.Cluster.HasService(req.ServiceType) {
		http.Error(w, "no such service", http.StatusUnprocessableEntity)
		return nil, nil
	}
	if !p.Cluster.HasResource(req.ServiceType, req.ResourceName) {
		http.Error(w, "no such resource", http.StatusUnprocessableEntity)
		return nil, nil
	}
	scopeName := fmt.Sprintf("%s/%s", domain.Name, project.Name)
	behavior := p.Cluster.BehaviorForResource(req.ServiceType, req.ResourceName, scopeName)
	if len(behavior.CommitmentDurations) == 0 {
		http.Error(w, "commitments are not enabled for this resource", http.StatusUnprocessableEntity)
		return nil, nil
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
	req, behavior := p.parseAndValidateCommitmentRequest(w, r, *dbProject, *dbDomain)
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
	result, err := datamodel.CanConfirmNewCommitment(*req, *dbProject, p.Cluster, p.DB, now)
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
	req, behavior := p.parseAndValidateCommitmentRequest(w, r, *dbProject, *dbDomain)
	if req == nil {
		return
	}

	var dbService db.ProjectService
	err := p.DB.SelectOne(&dbService, `SELECT * FROM project_services WHERE project_id = $1 AND type = $2`,
		dbProject.ID, req.ServiceType)
	if respondwith.ErrorText(w, err) {
		return
	}

	//enforce min_confirm_date constraint if set
	now := p.timeNow()
	if minConfirmBy := behavior.CommitmentMinConfirmDate; minConfirmBy != nil && minConfirmBy.After(now) {
		if req.ConfirmBy == nil || req.ConfirmBy.Before(*minConfirmBy) {
			msg := fmt.Sprintf("this commitment needs a `confirm_by` timestamp at or after %s", behavior.CommitmentMinConfirmDate.Format(time.RFC3339))
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
		ServiceID:        dbService.ID,
		ResourceName:     req.ResourceName,
		AvailabilityZone: req.AvailabilityZone,
		Amount:           req.Amount,
		Duration:         req.Duration,
		CreatedAt:        now,
		CreatorUUID:      token.UserUUID(),
		CreatorName:      fmt.Sprintf("%s@%s", token.UserName(), token.UserDomainName()),
		ConfirmBy:        confirmBy,
		ConfirmedAt:      nil, //may be set below
		ExpiresAt:        req.Duration.AddTo(unwrapOrDefault(confirmBy, now)),
	}
	if req.ConfirmBy == nil {
		//if not planned for confirmation in the future, confirm immediately (or fail)
		ok, err := datamodel.CanConfirmNewCommitment(*req, *dbProject, p.Cluster, tx, now)
		if respondwith.ErrorText(w, err) {
			return
		}
		if !ok {
			http.Error(w, "not enough capacity available for immediate confirmation", http.StatusConflict)
			return
		}
		dbCommitment.ConfirmedAt = &now
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
		Commitment:  p.convertCommitmentToDisplayForm(dbCommitment, dbService.Type),
	})

	//display the possibly confirmed commitment to the user
	err = p.DB.SelectOne(&dbCommitment, `SELECT * FROM project_commitments WHERE id = $1`, dbCommitment.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	c := p.convertCommitmentToDisplayForm(dbCommitment, dbService.Type)
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
	var dbService db.ProjectService
	err = p.DB.SelectOne(&dbService, `SELECT * FROM project_services WHERE id = $1`, dbCommitment.ServiceID)
	if respondwith.ErrorText(w, err) {
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
		Commitment:  p.convertCommitmentToDisplayForm(dbCommitment, dbService.Type),
	})
	w.WriteHeader(http.StatusNoContent)
}
