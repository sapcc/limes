/*******************************************************************************
*
* Copyright 2017-2020 SAP SE
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
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"
	gorp "gopkg.in/gorp.v2"

	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/datamodel"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/reports"
	"github.com/sapcc/limes/pkg/util"
)

// ListProjects handles GET /v1/domains/:domain_id/projects.
func (p *v1Provider) ListProjects(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects")
	token := p.CheckToken(r)
	if !token.Require(w, "project:list") {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}

	filter := reports.ReadFilter(r)
	if filter.WithRates {
		http.Error(w, `query parameter "rates" is not supported anymore`, http.StatusBadRequest)
		return
	}

	//This endpoint can generate reports so large, we shouldn't be rendering
	//more than one at the same time in order to keep our memory usage in check.
	//(For example, a full project list with all resources for a domain with 2000
	//projects runs as large as 160 MiB for the pure JSON.)
	p.listProjectsMutex.Lock()
	defer p.listProjectsMutex.Unlock()

	stream := NewJSONListStream[*limes.ProjectReport](w, r, "projects")
	stream.FinalizeDocument(reports.GetProjectResources(p.Cluster, *dbDomain, nil, db.DB, filter, stream.WriteItem))
}

// GetProject handles GET /v1/domains/:domain_id/projects/:project_id.
func (p *v1Provider) GetProject(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id")
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

	filter := reports.ReadFilter(r)
	if filter.WithRates {
		http.Error(w, `query parameter "rates" is not supported anymore`, http.StatusBadRequest)
		return
	}

	project, err := GetProjectResourceReport(p.Cluster, *dbDomain, *dbProject, db.DB, filter)
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]interface{}{"project": project})
}

// DiscoverProjects handles POST /v1/domains/:domain_id/projects/discover.
func (p *v1Provider) DiscoverProjects(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/discover")
	token := p.CheckToken(r)
	if !token.Require(w, "project:discover") {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}

	newProjectUUIDs, err := collector.ScanProjects(p.Cluster, dbDomain)
	if respondwith.ErrorText(w, err) {
		return
	}

	if len(newProjectUUIDs) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	respondwith.JSON(w, 202, map[string]interface{}{"new_projects": util.IDsToJSON(newProjectUUIDs)})
}

// SyncProject handles POST /v1/domains/:domain_id/projects/:project_id/sync.
func (p *v1Provider) SyncProject(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/sync")
	token := p.CheckToken(r)
	if !token.Require(w, "project:show") {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}
	dbProject, ok := p.FindProjectFromRequestIfExists(w, r, dbDomain)
	if !ok {
		return
	}

	//check if project needs to be discovered
	if dbProject == nil {
		newProjectUUIDs, err := collector.ScanProjects(p.Cluster, dbDomain)
		if respondwith.ErrorText(w, err) {
			return
		}
		projectUUID := mux.Vars(r)["project_id"]
		found := false
		for _, newUUID := range newProjectUUIDs {
			if projectUUID == newUUID {
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "no such project", http.StatusNotFound)
			return
		}

		//now we should find it in the DB
		dbProject = p.FindProjectFromRequest(w, r, dbDomain)
		if dbProject == nil {
			return //wtf
		}
	}

	//mark all project services as stale to force limes-collect to sync ASAP
	_, err := db.DB.Exec(`UPDATE project_services SET stale = '1', rates_stale = '1' WHERE project_id = $1`, dbProject.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// PutProject handles PUT /v1/domains/:domain_id/projects/:project_id.
func (p *v1Provider) PutProject(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id")
	p.putOrSimulatePutProject(w, r, false)
}

// SimulatePutProject handles POST /v1/domains/:domain_id/projects/:project_id/simulate-put.
func (p *v1Provider) SimulatePutProject(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/simulate-put")
	p.putOrSimulatePutProject(w, r, true)
}

func (p *v1Provider) putOrSimulatePutProject(w http.ResponseWriter, r *http.Request, simulate bool) {
	//parse request body
	var parseTarget struct {
		Project struct {
			Bursting struct {
				Enabled *bool `json:"enabled"`
			} `json:"bursting"`
			Services limes.QuotaRequest `json:"services"`
		} `json:"project"`
	}
	parseTarget.Project.Services = make(limes.QuotaRequest)
	if !RequireJSON(w, r, &parseTarget) {
		return
	}

	//branch out into the specialized subfunctions
	if parseTarget.Project.Bursting.Enabled == nil {
		p.putOrSimulatePutProjectQuotas(w, r, simulate, parseTarget.Project.Services)
	} else {
		if len(parseTarget.Project.Services) == 0 {
			p.putOrSimulateProjectAttributes(w, r, simulate, *parseTarget.Project.Bursting.Enabled)
		} else {
			http.Error(w, "it is currently not allowed to set bursting.enabled and quotas in the same request", http.StatusBadRequest)
		}
	}
}

func (p *v1Provider) putOrSimulatePutProjectQuotas(w http.ResponseWriter, r *http.Request, simulate bool, serviceQuotas limes.QuotaRequest) {
	requestTime := time.Now()
	token := p.CheckToken(r)
	if !token.Require(w, "project:show") {
		return
	}
	checkToken := func(policy string) func(string) bool {
		return func(serviceType string) bool {
			token.Context.Request["service_type"] = serviceType
			return token.Check(policy)
		}
	}

	updater := QuotaUpdater{
		Cluster:         p.Cluster,
		CanRaise:        checkToken("project:raise"),
		CanRaiseLP:      checkToken("project:raise_lowpriv"),
		CanLower:        checkToken("project:lower"),
		CanSetRateLimit: checkToken("project:set_rate_limit"),
	}
	updater.Domain = p.FindDomainFromRequest(w, r)
	if updater.Domain == nil {
		return
	}
	updater.Project = p.FindProjectFromRequest(w, r, updater.Domain)
	if updater.Project == nil {
		return
	}

	//start a transaction for the quota updates
	var tx *gorp.Transaction
	var dbi db.Interface
	if simulate {
		dbi = db.DB
	} else {
		var err error
		tx, err = db.DB.Begin()
		if respondwith.ErrorText(w, err) {
			return
		}
		defer sqlext.RollbackUnlessCommitted(tx)
		dbi = tx
	}

	//validate inputs (within the DB transaction, to ensure that we do not apply
	//inconsistent values later)
	err := updater.ValidateInput(serviceQuotas, dbi)
	if _, ok := err.(MissingProjectReportError); ok {
		//MissingProjectReportError indicates that the project is new and initial
		//scraping is not yet done -> ask the user to wait until that's done, with
		//a 4xx status code instead of a 5xx one so that this does not trigger
		//alerts on the operator side
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusLocked)
		fmt.Fprintf(w, "%s (please retry in a few seconds after initial scraping is done)", err.Error())
		return
	}
	if respondwith.ErrorText(w, err) {
		return
	}

	//stop now if we're only simulating
	if simulate {
		updater.WriteSimulationReport(w)
		return
	}

	if !updater.IsValid() {
		updater.CommitAuditTrail(token, r, requestTime)
		updater.WritePutErrorResponse(w)
		return
	}

	//check all services for resources to update
	var services []db.ProjectService
	_, err = tx.Select(&services,
		`SELECT * FROM project_services WHERE project_id = $1 ORDER BY type`, updater.Project.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	var (
		resourcesToUpdate []interface{}
		ratesToUpdate     []db.ProjectRate
	)
	servicesToUpdate := make(map[string]bool)

	for _, srv := range services {
		if serviceRequests, exists := updater.ResourceRequests[srv.Type]; exists {
			//Check all resources.
			var resources []db.ProjectResource
			_, err = tx.Select(&resources,
				`SELECT * FROM project_resources WHERE service_id = $1 ORDER BY name`, srv.ID)
			if respondwith.ErrorText(w, err) {
				return
			}

			for _, res := range resources {
				req, exists := serviceRequests[res.Name]
				if !exists {
					continue
				}
				if res.Quota != nil && *res.Quota == req.NewValue {
					continue //nothing to do
				}

				//take a copy of the loop variable (it will be updated by the loop, so if
				//we didn't take a copy manually, the resourcesToUpdate list would
				//contain only identical pointers)
				res := res

				res.Quota = &req.NewValue
				resourcesToUpdate = append(resourcesToUpdate, &res)
				servicesToUpdate[srv.Type] = true
			}
		}

		if rateLimitRequests, exists := updater.RateLimitRequests[srv.Type]; exists {
			//Check all rate limits.
			var rates []db.ProjectRate
			_, err = tx.Select(&rates, `SELECT * FROM project_rates WHERE service_id = $1 ORDER BY name`, srv.ID)
			if respondwith.ErrorText(w, err) {
				return
			}
			ratesByName := make(map[string]db.ProjectRate)
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

				rate.Limit = &req.NewLimit
				rate.Window = &req.NewWindow
				ratesToUpdate = append(ratesToUpdate, rate)
			}
		}
	}
	//update the DB with the new quotas
	onlyQuota := func(c *gorp.ColumnMap) bool {
		return c.ColumnName == "quota"
	}
	_, err = tx.UpdateColumns(onlyQuota, resourcesToUpdate...)
	if respondwith.ErrorText(w, err) {
		return
	}

	//Update the DB with the new rate limits.
	stmt, err := dbi.Prepare(`INSERT INTO project_rates (service_id, name, rate_limit, window_ns) VALUES ($1,$2,$3,$4) ON CONFLICT (service_id, name) DO UPDATE SET rate_limit = EXCLUDED.rate_limit, window_ns = EXCLUDED.window_ns`)
	if respondwith.ErrorText(w, err) {
		return
	}
	for _, rate := range ratesToUpdate {
		_, err := stmt.Exec(rate.ServiceID, rate.Name, rate.Limit, rate.Window)
		if respondwith.ErrorText(w, err) {
			return
		}
	}

	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	updater.CommitAuditTrail(token, r, requestTime)

	//attempt to write the quotas into the backend
	//
	//It is not a mistake that this happens after tx.Commit(). If this operation
	//fails, then subsequent scraping tasks will try to apply the quota again
	//until the operation succeeds. What's important is that the approved quota
	//budget inside Limes is redistributed.
	var errors []string
	for _, srv := range services {
		if !servicesToUpdate[srv.Type] {
			continue
		}
		targetDomain := core.KeystoneDomain{
			Name: updater.Domain.Name,
			UUID: updater.Domain.UUID,
		}
		err := datamodel.ApplyBackendQuota(
			db.DB,
			updater.Cluster, targetDomain, *updater.Project,
			srv.ID, srv.Type,
		)
		if err != nil {
			logg.Info("while applying new %s quota for project %s: %s", srv.Type, updater.Project.UUID, err.Error())
			errors = append(errors, err.Error())
			continue
		}
	}

	//report any backend errors to the user
	if len(errors) > 0 {
		msg := "quotas have been accepted, but some error(s) occurred while trying to write the quotas into the backend services:"
		http.Error(w, msg+"\n"+strings.Join(errors, "\n"), http.StatusAccepted)
		return
	}
	//otherwise, report success
	w.WriteHeader(http.StatusAccepted)
}

func (p *v1Provider) putOrSimulateProjectAttributes(w http.ResponseWriter, r *http.Request, simulate, hasBursting bool) {
	requestTime := time.Now()
	token := p.CheckToken(r)
	if !token.Require(w, "project:edit") {
		return
	}
	domain := p.FindDomainFromRequest(w, r)
	if domain == nil {
		return
	}
	project := p.FindProjectFromRequest(w, r, domain)
	if project == nil {
		return
	}
	if p.Cluster.Config.Bursting.MaxMultiplier == 0 {
		msg := "bursting is not available for this cluster"
		http.Error(w, msg, http.StatusBadRequest)
		logAndPublishEvent(requestTime, r, token, http.StatusBadRequest,
			burstEventTarget{
				DomainID:     domain.UUID,
				DomainName:   domain.Name,
				ProjectID:    project.UUID,
				ProjectName:  project.Name,
				RejectReason: msg,
			})
		return
	}

	//start a transaction for the attribute updates
	var tx *gorp.Transaction
	var dbi db.Interface
	if simulate {
		dbi = db.DB
	} else {
		var err error
		tx, err = db.DB.Begin()
		if respondwith.ErrorText(w, err) {
			return
		}
		defer sqlext.RollbackUnlessCommitted(tx)
		dbi = tx
	}

	//anything to do?
	if project.HasBursting == hasBursting {
		if simulate {
			respondwith.JSON(w, http.StatusOK, map[string]interface{}{"success": true})
		} else {
			w.WriteHeader(http.StatusAccepted)
		}
		return
	}

	//When enabling bursting, we do not need to validate anything else.
	//When disabling bursting, we need to ensure `usage < quota`.
	if project.HasBursting {
		var overbookedResources []string
		query := `
			SELECT ps.type, pr.name
				FROM project_services ps
				JOIN project_resources pr ON ps.id = pr.service_id
			 WHERE ps.project_id = $1 AND pr.usage > pr.quota`
		err := sqlext.ForeachRow(dbi, query, []interface{}{project.ID}, func(rows *sql.Rows) error {
			var serviceType, resourceName string
			err := rows.Scan(&serviceType, &resourceName)
			overbookedResources = append(overbookedResources, serviceType+"/"+resourceName)
			return err
		})
		if respondwith.ErrorText(w, err) {
			return
		}
		if len(overbookedResources) > 0 {
			msg := fmt.Sprintf(
				"cannot disable bursting because %d resources are currently bursted: %s",
				len(overbookedResources), strings.Join(overbookedResources, ", "))
			if len(overbookedResources) == 1 {
				msg = "cannot disable bursting because 1 resource is currently bursted: " +
					overbookedResources[0]
			}
			http.Error(w, msg, http.StatusConflict)
			logAndPublishEvent(requestTime, r, token, http.StatusConflict,
				burstEventTarget{
					DomainID:     domain.UUID,
					DomainName:   domain.Name,
					ProjectID:    project.UUID,
					ProjectName:  project.Name,
					RejectReason: msg,
				})
			return
		}
	}

	//we're about to change stuff
	if simulate {
		respondwith.JSON(w, http.StatusOK, map[string]interface{}{"success": true})
		return
	}

	//update project
	project.HasBursting = hasBursting
	_, err := tx.Exec(`UPDATE projects SET has_bursting = $1 WHERE id = $2`, hasBursting, project.ID)
	if respondwith.ErrorText(w, err) {
		return
	}
	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	//update backend quotas to match new bursting mode
	var services []db.ProjectService
	_, err = db.DB.Select(&services, `SELECT * FROM project_services WHERE project_id = $1`, project.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	var errors []string
	for _, srv := range services {
		_, exists := p.Cluster.QuotaPlugins[srv.Type]
		if !exists {
			continue
		}
		targetDomain := core.KeystoneDomain{
			Name: domain.Name,
			UUID: domain.UUID,
		}
		err := datamodel.ApplyBackendQuota(
			db.DB,
			p.Cluster, targetDomain, *project,
			srv.ID, srv.Type,
		)
		if err != nil {
			errors = append(errors, err.Error())
			continue
		}
	}

	logAndPublishEvent(requestTime, r, token, http.StatusConflict,
		burstEventTarget{
			DomainID:    domain.UUID,
			DomainName:  domain.Name,
			ProjectID:   project.UUID,
			ProjectName: project.Name,
			NewStatus:   hasBursting,
		})

	//report any backend errors to the user
	if len(errors) > 0 {
		msg := "bursting mode has been updated, but some error(s) occurred while trying to write quotas into the backend services:"
		http.Error(w, msg+"\n"+strings.Join(errors, "\n"), http.StatusAccepted)
		return
	}
	//otherwise, report success
	w.WriteHeader(http.StatusAccepted)
}
