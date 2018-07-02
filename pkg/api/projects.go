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

	gorp "gopkg.in/gorp.v2"

	"github.com/gorilla/mux"
	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
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
	if ReturnError(w, err) {
		return
	}

	ReturnJSON(w, 200, map[string]interface{}{"projects": projects})
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
	projects, err := reports.GetProjects(cluster, dbDomain.ID, &dbProject.ID, db.DB, reports.ReadFilter(r), withSubresources)
	if ReturnError(w, err) {
		return
	}
	if len(projects) == 0 {
		http.Error(w, "no resource data found for project", 500)
		return
	}

	ReturnJSON(w, 200, map[string]interface{}{"project": projects[0]})
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
	if ReturnError(w, err) {
		return
	}

	if len(newProjectUUIDs) == 0 {
		w.WriteHeader(204)
		return
	}
	ReturnJSON(w, 202, map[string]interface{}{"new_projects": util.IDsToJSON(newProjectUUIDs)})
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
		if ReturnError(w, err) {
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
	if ReturnError(w, err) {
		return
	}

	w.WriteHeader(202)
}

//PutProject handles PUT /v1/domains/:domain_id/projects/:project_id.
func (p *v1Provider) PutProject(w http.ResponseWriter, r *http.Request) {
	token := p.CheckToken(r)
	canRaise := token.Check("project:raise")
	canLower := token.Check("project:lower")
	if !canRaise && !canLower {
		token.Require(w, "project:raise") //produce standard Unauthorized response
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
	serviceQuotas := parseTarget.Project.Services

	//start a transaction for the quota updates
	tx, err := db.DB.Begin()
	if ReturnError(w, err) {
		return
	}
	defer db.RollbackUnlessCommitted(tx)

	//gather a report on the domain's quotas to decide whether a quota update is legal
	domainReports, err := reports.GetDomains(cluster, &dbDomain.ID, db.DB, reports.Filter{})
	if ReturnError(w, err) {
		return
	}
	if len(domainReports) == 0 {
		http.Error(w, "no resource data found for domain", 500)
		return
	}
	domainReport := domainReports[0]

	var constraints limes.QuotaConstraints
	if cluster.QuotaConstraints != nil {
		constraints = cluster.QuotaConstraints.Projects[dbDomain.Name][dbProject.Name]
	}

	//check all services for resources to update
	var services []db.ProjectService
	_, err = tx.Select(&services,
		`SELECT * FROM project_services WHERE project_id = $1 ORDER BY type`, dbProject.ID)
	if ReturnError(w, err) {
		return
	}
	var resourcesToUpdate []db.ProjectResource
	var resourcesToUpdateAsUntyped []interface{}
	servicesToUpdate := make(map[string]bool)
	var errors []string

	var auditTrail util.AuditTrail
	for _, srv := range services {
		resourceQuotas, exists := serviceQuotas[srv.Type]
		if !exists {
			continue
		}

		//check all resources
		var resources []db.ProjectResource
		_, err = tx.Select(&resources,
			`SELECT * FROM project_resources WHERE service_id = $1 ORDER BY name`, srv.ID)
		if ReturnError(w, err) {
			return
		}
		for _, res := range resources {
			newQuotaInput, exists := resourceQuotas[res.Name]
			if !exists {
				continue
			}
			newQuota, err := newQuotaInput.ConvertFor(cluster, srv.Type, res.Name)
			if err != nil {
				errors = append(errors, fmt.Sprintf("cannot change %s/%s quota: %s", srv.Type, res.Name, err.Error()))
				continue
			}
			if res.Quota == newQuota {
				continue //nothing to do
			}

			resInfo := cluster.InfoForResource(srv.Type, res.Name)
			constraint := constraints[srv.Type][res.Name]
			err = checkProjectQuotaUpdate(srv, res, resInfo.Unit, domainReport, constraint, newQuota, canRaise, canLower)
			if err != nil {
				errors = append(errors, err.Error())
				continue
			}

			//take a copy of the loop variable (it will be updated by the loop, so if
			//we didn't take a copy manually, the resourcesToUpdateAsUntyped list
			//would contain only identical pointers)
			res := res
			auditTrail.Add("set quota %s.%s = %d -> %d for project %s by user %s (%s)",
				srv.Type, res.Name, res.Quota, newQuota,
				dbProject.UUID, token.UserUUID, token.UserName,
			)
			res.Quota = newQuota
			resourcesToUpdate = append(resourcesToUpdate, res)
			resourcesToUpdateAsUntyped = append(resourcesToUpdateAsUntyped, &res)
			servicesToUpdate[srv.Type] = true
		}
	}

	//if not legal, report errors to the user
	if len(errors) > 0 {
		http.Error(w, strings.Join(errors, "\n"), 422)
		return
	}

	//update the DB with the new quotas
	onlyQuota := func(c *gorp.ColumnMap) bool {
		return c.ColumnName == "quota"
	}
	_, err = tx.UpdateColumns(onlyQuota, resourcesToUpdateAsUntyped...)
	if ReturnError(w, err) {
		return
	}
	err = tx.Commit()
	if ReturnError(w, err) {
		return
	}
	auditTrail.Commit()

	//attempt to write the quotas into the backend
	//
	//It is not a mistake that this happens after tx.Commit(). If this operation
	//fails, then subsequent scraping tasks will try to apply the quota again
	//until the operation succeeds. What's important is that the approved quota
	//budget inside Limes is redistributed.
	errors = nil
	for _, srv := range services {
		if !servicesToUpdate[srv.Type] {
			continue
		}

		plugin := cluster.QuotaPlugins[srv.Type]
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
		if ReturnError(w, err) {
			return
		}
		for _, res := range resources {
			quotaValues[res.Name] = res.Quota
		}
		err = plugin.SetQuota(
			cluster.ProviderClientForService(srv.Type),
			cluster.ID, dbDomain.UUID, dbProject.UUID, quotaValues,
		)
		if err != nil {
			errors = append(errors, err.Error())
			continue
		}

		//on success, we now know that the backend has all the correct quotas
		_, err = db.DB.Exec(
			`UPDATE project_resources SET backend_quota = quota WHERE service_id = $1`,
			srv.ID)
		if ReturnError(w, err) {
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
	projects, err := reports.GetProjects(cluster, dbDomain.ID, &dbProject.ID, db.DB, reports.Filter{}, false)
	if ReturnError(w, err) {
		return
	}
	if len(projects) == 0 {
		http.Error(w, "no resource data found for project", 500)
		return
	}

	ReturnJSON(w, 200, map[string]interface{}{"project": projects[0]})
}

func checkProjectQuotaUpdate(srv db.ProjectService, res db.ProjectResource, unit limes.Unit, domain *reports.Domain, constraint limes.QuotaConstraint, newQuota uint64, canRaise, canLower bool) error {
	if !constraint.Allows(newQuota) {
		return fmt.Errorf("cannot change %s/%s quota: requested value %q contradicts constraint %q for this project and resource",
			srv.Type, res.Name, limes.ValueWithUnit{Value: newQuota, Unit: unit}, constraint.ToString(unit))
	}

	//if quota is being reduced, permission is required and usage must fit into quota
	//(note that both res.Quota and newQuota are uint64, so we do not need to
	//cover the case of infinite quotas)
	if res.Quota > newQuota {
		if !canLower {
			return fmt.Errorf("cannot change %s/%s quota: user is not allowed to lower quotas in this project", srv.Type, res.Name)
		}
		if res.Usage > newQuota {
			return fmt.Errorf("cannot change %s/%s quota: quota may not be lower than current usage", srv.Type, res.Name)
		}
		return nil
	}

	//if quota is being raised, permission is required and also the domain quota may not be exceeded
	if !canRaise {
		return fmt.Errorf("cannot change %s/%s quota: user is not allowed to raise quotas in this project", srv.Type, res.Name)
	}
	domainQuota := uint64(0)
	projectsQuota := uint64(0)
	if domainService, exists := domain.Services[srv.Type]; exists {
		if domainResource, exists := domainService.Resources[res.Name]; exists {
			domainQuota = domainResource.DomainQuota
			projectsQuota = domainResource.ProjectsQuota
		}
	}
	//NOTE: It looks like an arithmetic overflow (or rather, underflow) is
	//possible here, but it isn't. projectsQuota is the sum over all current
	//project quotas, including res.Quota, and thus is always bigger (since these
	//quotas are all unsigned). Also, we're doing everything in a transaction, so
	//an overflow because of concurrent quota changes is also out of the
	//question.
	newProjectsQuota := projectsQuota - res.Quota + newQuota
	if newProjectsQuota > domainQuota {
		maxQuota := domainQuota - (projectsQuota - res.Quota)
		if domainQuota < projectsQuota-res.Quota {
			maxQuota = 0
		}
		return fmt.Errorf("cannot change %s/%s quota: domain quota exceeded (maximum acceptable project quota is %s)",
			srv.Type, res.Name,
			limes.ValueWithUnit{Value: maxQuota, Unit: unit},
		)
	}

	return nil
}
