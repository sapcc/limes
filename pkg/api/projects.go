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

	"github.com/sapcc/limes/pkg/api/output"
	"github.com/sapcc/limes/pkg/db"
)

var listProjectsQuery = `
	SELECT p.uuid, ps.type, ps.scraped_at, pr.name, pr.quota, pr.usage, pr.backend_quota
	  FROM projects p
	  JOIN project_services ps ON ps.project_id = p.id
	  JOIN project_resources pr ON pr.service_id = ps.id
	 WHERE %s
`

//ListProjects handles GET /v1/domains/:domain_id/projects.
func (p *v1Provider) ListProjects(w http.ResponseWriter, r *http.Request) {
	if !p.HasPermission("project:list", w, r) {
		return
	}
	domain := FindDomain(w, r)
	if domain == nil {
		return
	}

	//collect conditions for SQL query
	fields := map[string]interface{}{"p.domain_id": domain.ID}
	queryValues := r.URL.Query()
	if services, ok := queryValues["service"]; ok {
		fields["ps.type"] = services
	}
	if resources, ok := queryValues["resource"]; ok {
		fields["pr.name"] = resources
	}

	//execute SQL query
	whereStr, queryArgs := db.BuildSimpleWhereClause(fields)
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

	ReturnJSON(w, 200, map[string]interface{}{"projects": projects.Scopes})
}
