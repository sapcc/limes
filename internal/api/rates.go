// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"database/sql"
	"net/http"

	"github.com/go-gorp/gorp/v3"
	. "github.com/majewsky/gg/option"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/reports"
)

// GetClusterRates handles GET /rates/v1/clusters/current.
func (p *v1Provider) GetClusterRates(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v1/clusters/current")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show") {
		return
	}

	if _, ok := r.URL.Query()["rates"]; ok {
		http.Error(w, "the `rates` query parameter is not allowed here", http.StatusBadRequest)
		return
	}

	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ErrorText(w, err) {
		return
	}

	cluster, err := reports.GetClusterRates(p.Cluster, p.DB, reports.ReadFilter(r, p.Cluster, serviceInfos), serviceInfos)
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]any{"cluster": cluster})
}

// ListProjectRates handles GET /rates/v1/domains/:domain_id/projects.
func (p *v1Provider) ListProjectRates(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v1/domains/:id/projects")
	token := p.CheckToken(r)
	if !token.Require(w, "project:list") {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}

	if _, ok := r.URL.Query()["rates"]; ok {
		http.Error(w, "the `rates` query parameter is not allowed here", http.StatusBadRequest)
		return
	}

	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ErrorText(w, err) {
		return
	}

	filter := reports.ReadFilter(r, p.Cluster, serviceInfos)
	stream := NewJSONListStream[*limesrates.ProjectReport](w, r, "projects")
	stream.FinalizeDocument(reports.GetProjectRates(p.Cluster, *dbDomain, nil, p.DB, filter, serviceInfos, stream.WriteItem))
}

// GetProjectRates handles GET /rates/v1/domains/:domain_id/projects/:project_id.
func (p *v1Provider) GetProjectRates(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v1/domains/:id/projects/:id")
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

	if _, ok := r.URL.Query()["rates"]; ok {
		http.Error(w, "the `rates` query parameter is not allowed here", http.StatusBadRequest)
		return
	}

	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ErrorText(w, err) {
		return
	}

	project, err := GetProjectRateReport(p.Cluster, *dbDomain, *dbProject, p.DB, reports.ReadFilter(r, p.Cluster, serviceInfos), serviceInfos)
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]any{"project": project})
}

// SyncProjectRates handles POST /v1/domains/:domain_id/projects/:project_id/sync.
//
// Deprecated:
func (p *v1Provider) SyncProjectRates(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v1/domains/:id/projects/:id/sync")
	p.SyncProject(w, r)
}

// PutProjectRates handles PUT /rates/v1/domains/:domain_id/projects/:project_id.
func (p *v1Provider) PutProjectRates(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v1/domains/:id/projects/:id")
	p.putOrSimulatePutProjectRates(w, r, false)
}

// SimulatePutProjectRates handles POST /rates/v1/domains/:domain_id/projects/:project_id/simulate-put.
func (p *v1Provider) SimulatePutProjectRates(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v1/domains/:id/projects/:id/simulate-put")
	p.putOrSimulatePutProjectRates(w, r, true)
}

func (p *v1Provider) putOrSimulatePutProjectRates(w http.ResponseWriter, r *http.Request, simulate bool) {
	// parse request body
	var parseTarget struct {
		Project struct {
			Services limesrates.RateRequest `json:"services"`
		} `json:"project"`
	}
	parseTarget.Project.Services = make(limesrates.RateRequest)
	if !RequireJSON(w, r, &parseTarget) {
		return
	}

	requestTime := p.timeNow()
	token := p.CheckToken(r)
	if !token.Require(w, "project:show") {
		return
	}

	updater := RateLimitUpdater{
		Cluster: p.Cluster,
		CanSetRateLimit: func(serviceType db.ServiceType) bool {
			token.Context.Request["service_type"] = string(serviceType)
			return token.Check("project:set_rate_limit")
		},
		Auditor: p.auditor,
	}
	updater.Domain = p.FindDomainFromRequest(w, r)
	if updater.Domain == nil {
		return
	}
	updater.Project = p.FindProjectFromRequest(w, r, updater.Domain)
	if updater.Project == nil {
		return
	}

	// start a transaction for the rate limit updates
	var tx *gorp.Transaction
	var dbi db.Interface
	if simulate {
		dbi = p.DB
	} else {
		var err error
		tx, err = p.DB.Begin()
		if respondwith.ErrorText(w, err) {
			return
		}
		defer sqlext.RollbackUnlessCommitted(tx)
		dbi = tx
	}

	// validate inputs (within the DB transaction, to ensure that we do not apply
	// inconsistent values later)
	err := updater.ValidateInput(parseTarget.Project.Services, dbi)
	if respondwith.ErrorText(w, err) {
		return
	}

	// stop now if we're only simulating
	if simulate {
		updater.WriteSimulationReport(w)
		return
	}

	if !updater.IsValid() {
		updater.CommitAuditTrail(token, r, requestTime)
		updater.WritePutErrorResponse(w)
		return
	}

	// get all project_rates and make them accessible quickly by ID
	var projectRates []db.ProjectRateV2
	_, err = tx.Select(&projectRates, `SELECT * FROM project_rates_v2 WHERE project_id = $1`, updater.Project.ID)
	projectRateByClusterRateID := make(map[db.ClusterRateID]db.ProjectRateV2)
	if respondwith.ErrorText(w, err) {
		return
	}
	for _, rate := range projectRates {
		projectRateByClusterRateID[rate.RateID] = rate
	}

	// check all services for resources to update
	var services []db.ClusterService
	_, err = tx.Select(&services, `SELECT * FROM cluster_services ORDER BY type`)
	if respondwith.ErrorText(w, err) {
		return
	}

	var ratesToUpdate []db.ProjectRateV2
	for _, srv := range services {
		rateLimitRequests, exists := updater.Requests[srv.Type]
		if !exists {
			continue // no rate limits for this service
		}
		var rates []db.ClusterRate
		_, err = tx.Select(&rates, `SELECT * FROM cluster_rates ORDER BY NAME`)
		if respondwith.ErrorText(w, err) {
			return
		}

		for _, rate := range rates {
			rateLimitRequest, exists := rateLimitRequests[rate.Name]
			if !exists {
				continue // no rate limit request for this rate
			}
			var projectRate db.ProjectRateV2
			if existingRate, exists := projectRateByClusterRateID[rate.ID]; exists {
				projectRate = existingRate
			} else {
				projectRate = db.ProjectRateV2{
					ProjectID: updater.Project.ID,
					RateID:    rate.ID,
				}
			}
			projectRate.Limit = Some(rateLimitRequest.NewLimit)
			projectRate.Window = Some(rateLimitRequest.NewWindow)
			ratesToUpdate = append(ratesToUpdate, projectRate)
		}
	}
	// update the DB with the new rate limits
	queryStr := `INSERT INTO project_rates_v2 (project_id, rate_id, rate_limit, window_ns, usage_as_bigint) VALUES ($1,$2,$3,$4,'') ON CONFLICT (project_id, rate_id) DO UPDATE SET rate_limit = EXCLUDED.rate_limit, window_ns = EXCLUDED.window_ns`
	err = sqlext.WithPreparedStatement(tx, queryStr, func(stmt *sql.Stmt) error {
		for _, rate := range ratesToUpdate {
			_, err := stmt.Exec(rate.ProjectID, rate.RateID, rate.Limit, rate.Window)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if respondwith.ErrorText(w, err) {
		return
	}
	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	updater.CommitAuditTrail(token, r, requestTime)
	w.WriteHeader(http.StatusAccepted)
}
