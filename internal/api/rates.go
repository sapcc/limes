/*******************************************************************************
*
* Copyright 2022 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package api

import (
	"database/sql"
	"net/http"

	"github.com/go-gorp/gorp/v3"
	. "github.com/majewsky/gg/option"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-api-declarations/liquid"
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

	cluster, err := reports.GetClusterRates(p.Cluster, p.DB, reports.ReadFilter(r, p.Cluster))
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

	filter := reports.ReadFilter(r, p.Cluster)
	stream := NewJSONListStream[*limesrates.ProjectReport](w, r, "projects")
	stream.FinalizeDocument(reports.GetProjectRates(p.Cluster, *dbDomain, nil, p.DB, filter, stream.WriteItem))
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

	project, err := GetProjectRateReport(p.Cluster, *dbDomain, *dbProject, p.DB, reports.ReadFilter(r, p.Cluster))
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]any{"project": project})
}

// SyncProjectRates handles POST /v1/domains/:domain_id/projects/:project_id/sync.
func (p *v1Provider) SyncProjectRates(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v1/domains/:id/projects/:id/sync")
	p.doSyncProject(w, r, "rates_stale")
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

	// check all services for resources to update
	var services []db.ProjectService
	_, err = tx.Select(&services,
		`SELECT * FROM project_services WHERE project_id = $1 ORDER BY type`, updater.Project.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	var ratesToUpdate []db.ProjectRate
	for _, srv := range services {
		if rateLimitRequests, exists := updater.Requests[srv.Type]; exists {
			// Check all rate limits.
			var rates []db.ProjectRate
			_, err = tx.Select(&rates, `SELECT * FROM project_rates WHERE service_id = $1 ORDER BY name`, srv.ID)
			if respondwith.ErrorText(w, err) {
				return
			}
			ratesByName := make(map[liquid.RateName]db.ProjectRate)
			for _, rate := range rates {
				ratesByName[rate.Name] = rate
			}

			for rateName, req := range rateLimitRequests {
				rate, exists := ratesByName[rateName]
				if !exists {
					rate = db.ProjectRate{
						ServiceID: srv.ID,
						Name:      rateName,
					}
				}

				rate.Limit = Some(req.NewLimit)
				rate.Window = Some(req.NewWindow)
				ratesToUpdate = append(ratesToUpdate, rate)
			}
		}
	}
	// update the DB with the new rate limits
	queryStr := `INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES ($1,$2,$3,$4,'') ON CONFLICT (service_id, name) DO UPDATE SET rate_limit = EXCLUDED.rate_limit, window_ns = EXCLUDED.window_ns`
	err = sqlext.WithPreparedStatement(tx, queryStr, func(stmt *sql.Stmt) error {
		for _, rate := range ratesToUpdate {
			_, err := stmt.Exec(rate.ServiceID, rate.Name, rate.Limit, rate.Window)
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
