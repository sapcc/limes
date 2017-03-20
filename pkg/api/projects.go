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
	"time"

	"github.com/gorilla/mux"
	"github.com/sapcc/limes/pkg/api/output"
	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/db"
)

var listProjectsQuery = `
	SELECT p.uuid, ps.type, ps.scraped_at, pr.name, pr.quota, pr.usage, pr.backend_quota
	  FROM projects p
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN project_resources pr ON pr.service_id = ps.id
	 WHERE %s
`

var showProjectQuery = `
	SELECT ps.type, ps.scraped_at, pr.name, pr.quota, pr.usage, pr.backend_quota
	  FROM project_services ps
	  JOIN project_resources pr ON pr.service_id = ps.id
	 WHERE %s
`

//ListProjects handles GET /v1/domains/:domain_id/projects.
func (p *v1Provider) ListProjects(w http.ResponseWriter, r *http.Request) {
	if !p.HasPermission("project:list", w, r) {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}

	//execute SQL query
	filters := map[string]interface{}{"p.domain_id": dbDomain.ID}
	p.AddStandardFiltersFromURLQuery(filters, r)
	whereStr, queryArgs := db.BuildSimpleWhereClause(filters)
	queryStr := fmt.Sprintf(listProjectsQuery, whereStr)
	rows, err := db.DB.Query(queryStr, queryArgs...)
	if ReturnError(w, err) {
		return
	}

	//accumulate data into result
	var (
		projects             output.Scopes
		projectUUID          string
		serviceType          string
		serviceScrapedAt     time.Time
		resourceName         string
		resourceQuota        int64
		resourceUsage        uint64
		resourceBackendQuota int64
	)
	for rows.Next() {
		err := rows.Scan(
			&projectUUID, &serviceType, &serviceScrapedAt,
			&resourceName, &resourceQuota, &resourceUsage, &resourceBackendQuota,
		)
		if ReturnError(w, err) {
			return
		}

		proj := projects.FindScope(projectUUID)
		srv := proj.FindService(serviceType)
		srv.ScrapedAt = serviceScrapedAt.Unix()
		res := srv.FindResource(resourceName)
		res.Set(resourceQuota, resourceUsage, resourceBackendQuota)
	}
	if ReturnError(w, rows.Err()) {
		return
	}
	if ReturnError(w, rows.Close()) {
		return
	}

	//TODO: ensure that service list is equal to the one in the cluster configuration (can be off in both directions!)
	ReturnJSON(w, 200, map[string]interface{}{"projects": projects.Scopes})
}

//GetProject handles GET /v1/domains/:domain_id/projects/:project_id.
func (p *v1Provider) GetProject(w http.ResponseWriter, r *http.Request) {
	if !p.HasPermission("project:show", w, r) {
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

	//execute SQL query
	filters := map[string]interface{}{"ps.project_id": dbProject.ID}
	p.AddStandardFiltersFromURLQuery(filters, r)
	whereStr, queryArgs := db.BuildSimpleWhereClause(filters)
	queryStr := fmt.Sprintf(showProjectQuery, whereStr)
	rows, err := db.DB.Query(queryStr, queryArgs...)
	if ReturnError(w, err) {
		return
	}

	//accumulate data into result
	var (
		project              output.Scope
		serviceType          string
		serviceScrapedAt     time.Time
		resourceName         string
		resourceQuota        int64
		resourceUsage        uint64
		resourceBackendQuota int64
	)
	project.ID = dbProject.UUID
	for rows.Next() {
		err := rows.Scan(
			&serviceType, &serviceScrapedAt,
			&resourceName, &resourceQuota, &resourceUsage, &resourceBackendQuota,
		)
		if ReturnError(w, err) {
			return
		}

		srv := project.FindService(serviceType)
		srv.ScrapedAt = serviceScrapedAt.Unix()
		res := srv.FindResource(resourceName)
		res.Set(resourceQuota, resourceUsage, resourceBackendQuota)
	}
	if ReturnError(w, rows.Err()) {
		return
	}
	if ReturnError(w, rows.Close()) {
		return
	}

	//TODO: ensure that service list is equal to the one in the cluster configuration (can be off in both directions!)
	ReturnJSON(w, 200, map[string]interface{}{"project": project})
}

//DiscoverProjects handles POST /v1/domains/:domain_id/projects/discover.
func (p *v1Provider) DiscoverProjects(w http.ResponseWriter, r *http.Request) {
	if !p.HasPermission("project:discover", w, r) {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}

	newProjectUUIDs, err := collector.ScanProjects(p.Driver, dbDomain)
	if ReturnError(w, err) {
		return
	}

	result := output.NewScopesFromIDList(newProjectUUIDs)
	ReturnJSON(w, 202, map[string]interface{}{"new_projects": result})
}

//SyncProject handles POST /v1/domains/:domain_id/projects/sync.
func (p *v1Provider) SyncProject(w http.ResponseWriter, r *http.Request) {
	if !p.HasPermission("project:show", w, r) {
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
		newProjectUUIDs, err := collector.ScanProjects(p.Driver, dbDomain)
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
		dbProject := p.FindProjectFromRequest(w, r, dbDomain)
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
