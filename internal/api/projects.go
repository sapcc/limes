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
	"fmt"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/collector"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/reports"
	"github.com/sapcc/limes/internal/util"
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

	// This endpoint can generate reports so large, we shouldn't be rendering
	// more than one at the same time in order to keep our memory usage in check.
	// (For example, a full project list with all resources for a domain with 2000
	// projects runs as large as 160 MiB for the pure JSON.)
	p.listProjectsMutex.Lock()
	defer p.listProjectsMutex.Unlock()

	filter := reports.ReadFilter(r, p.Cluster)
	stream := NewJSONListStream[*limesresources.ProjectReport](w, r, "projects")
	stream.FinalizeDocument(reports.GetProjectResources(p.Cluster, *dbDomain, nil, p.timeNow(), p.DB, filter, stream.WriteItem))
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

	project, err := GetProjectResourceReport(p.Cluster, *dbDomain, *dbProject, p.timeNow(), p.DB, reports.ReadFilter(r, p.Cluster))
	if respondwith.ErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]any{"project": project})
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

	c := collector.NewCollector(p.Cluster, p.DB)
	newProjectUUIDs, err := c.ScanProjects(r.Context(), dbDomain)
	if respondwith.ErrorText(w, err) {
		return
	}

	if len(newProjectUUIDs) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	respondwith.JSON(w, 202, map[string]any{"new_projects": util.IDsToJSON(newProjectUUIDs)})
}

// SyncProject handles POST /v1/domains/:domain_id/projects/:project_id/sync.
func (p *v1Provider) SyncProject(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/sync")
	p.doSyncProject(w, r, "stale")
}

func (p *v1Provider) doSyncProject(w http.ResponseWriter, r *http.Request, staleField string) {
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

	// check if project needs to be discovered
	if dbProject == nil {
		c := collector.NewCollector(p.Cluster, p.DB)
		newProjectUUIDs, err := c.ScanProjects(r.Context(), dbDomain)
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

		// now we should find it in the DB
		dbProject = p.FindProjectFromRequest(w, r, dbDomain)
		if dbProject == nil {
			return // wtf
		}
	}

	// mark all project services as stale to force limes-collect to sync ASAP
	_, err := p.DB.Exec(`UPDATE project_services SET `+staleField+` = '1' WHERE project_id = $1`, dbProject.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// PutProject handles PUT /v1/domains/:domain_id/projects/:project_id.
func (p *v1Provider) PutProject(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id")
	http.Error(w, "support for setting quotas manually has been removed", http.StatusMethodNotAllowed)
}

// SimulatePutProject handles POST /v1/domains/:domain_id/projects/:project_id/simulate-put.
func (p *v1Provider) SimulatePutProject(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/simulate-put")
	http.Error(w, "support for setting quotas manually has been removed", http.StatusMethodNotAllowed)
}

// PutProjectMaxQuota handles PUT /v1/domains/:domain_id/projects/:project_id/max-quota.
func (p *v1Provider) PutProjectMaxQuota(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/projects/:id/max-quota")
	requestTime := p.timeNow()
	token := p.CheckToken(r)
	if !token.Require(w, "project:edit_max_quota") {
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

	// parse request body
	var parseTarget struct {
		Project struct {
			Services []struct {
				Type      limes.ServiceType `json:"type"`
				Resources []struct {
					Name     limesresources.ResourceName `json:"name"`
					MaxQuota *uint64                     `json:"max_quota"`
					Unit     *limes.Unit                 `json:"unit"`
				} `json:"resources"`
			} `json:"services"`
		} `json:"project"`
	}
	if !RequireJSON(w, r, &parseTarget) {
		return
	}

	// validate request
	requested := make(map[limes.ServiceType]map[limesresources.ResourceName]*maxQuotaChange)
	for _, srvRequest := range parseTarget.Project.Services {
		if !p.Cluster.HasService(srvRequest.Type) {
			msg := "no such service: " + string(srvRequest.Type)
			http.Error(w, msg, http.StatusUnprocessableEntity)
			return
		}

		requested[srvRequest.Type] = make(map[limesresources.ResourceName]*maxQuotaChange)
		for _, resRequest := range srvRequest.Resources {
			if !p.Cluster.HasResource(srvRequest.Type, resRequest.Name) {
				msg := fmt.Sprintf("no such resource: %s/%s", srvRequest.Type, resRequest.Name)
				http.Error(w, msg, http.StatusUnprocessableEntity)
				return
			}

			if resRequest.MaxQuota == nil {
				requested[srvRequest.Type][resRequest.Name] = &maxQuotaChange{NewValue: nil}
			} else {
				resInfo := p.Cluster.InfoForResource(srvRequest.Type, resRequest.Name)
				if !resInfo.HasQuota {
					msg := fmt.Sprintf("resource %s/%s does not track quota", srvRequest.Type, resRequest.Name)
					http.Error(w, msg, http.StatusUnprocessableEntity)
					return
				}

				// convert given value to correct unit
				requestedMaxQuota := limes.ValueWithUnit{
					Unit:  limes.UnitUnspecified,
					Value: *resRequest.MaxQuota,
				}
				if resRequest.Unit != nil {
					requestedMaxQuota.Unit = *resRequest.Unit
				}
				convertedMaxQuota, err := core.ConvertUnitFor(p.Cluster, srvRequest.Type, resRequest.Name, requestedMaxQuota)
				if err != nil {
					msg := fmt.Sprintf("invalid input for %s/%s: %s", srvRequest.Type, resRequest.Name, err.Error())
					http.Error(w, msg, http.StatusUnprocessableEntity)
					return
				}
				requested[srvRequest.Type][resRequest.Name] = &maxQuotaChange{NewValue: &convertedMaxQuota}
			}
		}
	}

	// write requested values to DB
	tx, err := p.DB.Begin()
	if respondwith.ErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	var services []db.ProjectService
	_, err = tx.Select(&services,
		`SELECT * FROM project_services WHERE project_id = $1 ORDER BY type`, dbProject.ID)
	if respondwith.ErrorText(w, err) {
		return
	}

	for _, srv := range services {
		requestedInService, exists := requested[srv.Type]
		if !exists {
			continue
		}

		_, err := datamodel.ProjectResourceUpdate{
			UpdateResource: func(res *db.ProjectResource) error {
				requestedChange := requestedInService[res.Name]
				if requestedChange != nil {
					requestedChange.OldValue = res.MaxQuotaFromAdmin // remember for audit event
					res.MaxQuotaFromAdmin = requestedChange.NewValue
				}
				return nil
			},
		}.Run(tx, p.Cluster, p.timeNow(), *dbDomain, *dbProject, srv.Ref())
		if respondwith.ErrorText(w, err) {
			return
		}
	}

	err = tx.Commit()
	if respondwith.ErrorText(w, err) {
		return
	}

	// write audit trail
	for serviceType, requestedInService := range requested {
		for resourceName, requestedChange := range requestedInService {
			logAndPublishEvent(requestTime, r, token, http.StatusOK,
				maxQuotaEventTarget{
					DomainID:        dbDomain.UUID,
					DomainName:      dbDomain.Name,
					ProjectID:       dbProject.UUID, // is empty for domain quota updates, see above
					ProjectName:     dbProject.Name,
					ServiceType:     serviceType,
					ResourceName:    resourceName,
					RequestedChange: *requestedChange,
				},
			)
		}
	}

	w.WriteHeader(http.StatusAccepted)
}
