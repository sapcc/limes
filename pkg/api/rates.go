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
	"net/http"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/reports"
)

// GetClusterRates handles GET /v1/clusters/:cluster_id.
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

	cluster, err := reports.GetClusterRates(p.Cluster, db.DB, reports.ReadFilter(r))
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]interface{}{"cluster": cluster})
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

	filter := reports.ReadFilter(r)
	stream := NewJSONListStream[*limes.ProjectReport](w, r, "projects")
	stream.FinalizeDocument(reports.GetProjectRates(p.Cluster, *dbDomain, nil, db.DB, filter, stream.WriteItem))
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

	project, err := GetProjectRateReport(p.Cluster, *dbDomain, *dbProject, db.DB, reports.ReadFilter(r))
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]interface{}{"project": project})
}

// SyncProjectRates handles POST /v1/domains/:domain_id/projects/:project_id/sync.
func (p *v1Provider) SyncProjectRates(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v1/domains/:id/projects/:id/sync")
	p.doSyncProject(w, r, "rates_stale")
}
