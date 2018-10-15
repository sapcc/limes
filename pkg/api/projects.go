/*******************************************************************************
*
* Copyright 2017 SAP SE
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
	"fmt"
	"net/http"
	"strings"
	"time"

	gorp "gopkg.in/gorp.v2"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/reports"
	"github.com/sapcc/limes/pkg/util"
)

//ListProjects handles GET /v1/domains/:domain_id/projects.
func (p *v1Provider) ListProjects(w http.ResponseWriter, r *http.Request) {
	token := p.CheckToken(r)
	if !token.Require(w, "project:list") {
		return
	}
	cluster := p.FindClusterFromRequest(w, r, token)
	if cluster == nil {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r, cluster)
	if dbDomain == nil {
		return
	}

	_, withSubresources := r.URL.Query()["detail"]
	projects, err := reports.GetProjects(cluster, dbDomain.ID, nil, db.DB, reports.ReadFilter(r), withSubresources)
	if respondwith.ErrorText(w, err) {
		return
	}

	respondwith.JSON(w, 200, map[string]interface{}{"projects": projects})
}

//GetProject handles GET /v1/domains/:domain_id/projects/:project_id.
func (p *v1Provider) GetProject(w http.ResponseWriter, r *http.Request) {
	token := p.CheckToken(r)
	if !token.Require(w, "project:show") {
		return
	}
	cluster := p.FindClusterFromRequest(w, r, token)
	if cluster == nil {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r, cluster)
	if dbDomain == nil {
		return
	}
	dbProject := p.FindProjectFromRequest(w, r, dbDomain)
	if dbProject == nil {
		return
	}

	_, withSubresources := r.URL.Query()["detail"]
	project, err := GetProjectReport(cluster, *dbDomain, *dbProject, db.DB, reports.ReadFilter(r), withSubresources)
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]interface{}{"project": project})
}

//DiscoverProjects handles POST /v1/domains/:domain_id/projects/discover.
func (p *v1Provider) DiscoverProjects(w http.ResponseWriter, r *http.Request) {
	token := p.CheckToken(r)
	if !token.Require(w, "project:discover") {
		return
	}
	cluster := p.FindClusterFromRequest(w, r, token)
	if cluster == nil {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r, cluster)
	if dbDomain == nil {
		return
	}

	newProjectUUIDs, err := collector.ScanProjects(cluster, dbDomain)
	if respondwith.ErrorText(w, err) {
		return
	}

	if len(newProjectUUIDs) == 0 {
		w.WriteHeader(204)
		return
	}
	respondwith.JSON(w, 202, map[string]interface{}{"new_projects": util.IDsToJSON(newProjectUUIDs)})
}

//SyncProject handles POST /v1/domains/:domain_id/projects/sync.
func (p *v1Provider) SyncProject(w http.ResponseWriter, r *http.Request) {
	token := p.CheckToken(r)
	if !token.Require(w, "project:show") {
		return
	}
	cluster := p.FindClusterFromRequest(w, r, token)
	if cluster == nil {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r, cluster)
	if dbDomain == nil {
		return
	}
	dbProject, ok := p.FindProjectFromRequestIfExists(w, r, dbDomain)
	if !ok {
		return
	}

	//check if project needs to be discovered
	if dbProject == nil {
		newProjectUUIDs, err := collector.ScanProjects(cluster, dbDomain)
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
			http.Error(w, "no such project", 404)
			return
		}

		//now we should find it in the DB
		dbProject = p.FindProjectFromRequest(w, r, dbDomain)
		if dbProject == nil {
			return //wtf
		}
	}

	//mark all project services as stale to force limes-collect to sync ASAP
	_, err := db.DB.Exec(`UPDATE project_services SET stale = '1' WHERE project_id = $1`, dbProject.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	w.WriteHeader(202)
}

//PutProject handles PUT /v1/domains/:domain_id/projects/:project_id.
func (p *v1Provider) PutProject(w http.ResponseWriter, r *http.Request) {
	requestTime := time.Now()
	token := p.CheckToken(r)
	canRaise := token.Check("project:raise")
	canLower := token.Check("project:lower")
	if !canRaise && !canLower {
		token.Require(w, "project:raise") //produce standard Unauthorized response
		return
	}

	updater := QuotaUpdater{CanRaise: canRaise, CanLower: canLower}
	updater.Cluster = p.FindClusterFromRequest(w, r, token)
	if updater.Cluster == nil {
		return
	}
	updater.Domain = p.FindDomainFromRequest(w, r, updater.Cluster)
	if updater.Domain == nil {
		return
	}
	updater.Project = p.FindProjectFromRequest(w, r, updater.Domain)
	if updater.Project == nil {
		return
	}

	//parse request body
	var parseTarget struct {
		Project struct {
			Services ServiceQuotas `json:"services"`
		} `json:"project"`
	}
	parseTarget.Project.Services = make(ServiceQuotas)
	if !RequireJSON(w, r, &parseTarget) {
		return
	}

	//start a transaction for the quota updates
	tx, err := db.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer db.RollbackUnlessCommitted(tx)

	//validate inputs (within the DB transaction, to ensure that we do not apply
	//inconsistent values later)
	err = updater.ValidateInput(parseTarget.Project.Services, tx)
	if respondwith.ErrorText(w, err) {
		return
	}
	if !updater.IsValid() {
		updater.CommitAuditTrail(token, r, requestTime)
		http.Error(w, updater.ErrorMessage(), http.StatusUnprocessableEntity)
		return
	}

	//check all services for resources to update
	var services []db.ProjectService
	_, err = tx.Select(&services,
		`SELECT * FROM project_services WHERE project_id = $1 ORDER BY type`, updater.Project.ID)
	if respondwith.ErrorText(w, err) {
		return
	}
	var resourcesToUpdate []interface{}
	servicesToUpdate := make(map[string]bool)

	for _, srv := range services {
		serviceRequests, exists := updater.Requests[srv.Type]
		if !exists {
			continue
		}

		//check all resources
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
			if res.Quota == req.NewValue {
				continue //nothing to do
			}

			//take a copy of the loop variable (it will be updated by the loop, so if
			//we didn't take a copy manually, the resourcesToUpdate list would
			//contain only identical pointers)
			res := res

			res.Quota = req.NewValue
			resourcesToUpdate = append(resourcesToUpdate, &res)
			servicesToUpdate[srv.Type] = true
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

		plugin := updater.Cluster.QuotaPlugins[srv.Type]
		if plugin == nil {
			errors = append(errors, fmt.Sprintf("no quota plugin registered for service type %s", srv.Type))
			continue
		}

		//collect all resource quotas for this service (NOT only the ones that were
		//updated just now; the QuotaPlugin.SetQuota method requires as input *all*
		//quotas for that plugin's service)
		quotaValues := make(map[string]uint64)
		var resources []db.ProjectResource
		_, err = db.DB.Select(&resources,
			`SELECT * FROM project_resources WHERE service_id = $1`, srv.ID)
		if respondwith.ErrorText(w, err) {
			return
		}
		for _, res := range resources {
			quotaValues[res.Name] = res.Quota
		}
		provider, eo := updater.Cluster.ProviderClientForService(srv.Type)
		err = plugin.SetQuota(provider, eo, updater.Cluster.ID, updater.Domain.UUID, updater.Project.UUID, quotaValues)
		if err != nil {
			errors = append(errors, err.Error())
			continue
		}

		//on success, we now know that the backend has all the correct quotas
		_, err = db.DB.Exec(
			`UPDATE project_resources SET backend_quota = quota WHERE service_id = $1`,
			srv.ID)
		if respondwith.ErrorText(w, err) {
			return
		}
	}

	//report any backend errors to the user
	if len(errors) > 0 {
		msg := "quotas have been accepted, but some error(s) occurred while trying to write the quotas into the backend services:"
		http.Error(w, msg+"\n"+strings.Join(errors, "\n"), 202)
		return
	}
	//otherwise, report success
	w.WriteHeader(202)
}
