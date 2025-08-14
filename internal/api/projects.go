// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/gorilla/mux"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
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

	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	filter := reports.ReadFilter(r, p.Cluster, serviceInfos)
	stream := NewJSONListStream[*limesresources.ProjectReport](w, r, "projects")
	stream.FinalizeDocument(reports.GetProjectResources(p.Cluster, *dbDomain, nil, p.timeNow(), p.DB, filter, serviceInfos, stream.WriteItem))
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

	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	project, err := GetProjectResourceReport(p.Cluster, *dbDomain, *dbProject, p.timeNow(), p.DB, reports.ReadFilter(r, p.Cluster, serviceInfos), serviceInfos)
	if respondwith.ObfuscatedErrorText(w, err) {
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

	c := collector.NewCollector(p.Cluster)
	newProjectUUIDs, err := c.ScanProjects(r.Context(), dbDomain)
	if respondwith.ObfuscatedErrorText(w, err) {
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
	p.doSyncProject(w, r)
}

func (p *v1Provider) doSyncProject(w http.ResponseWriter, r *http.Request) {
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
		c := collector.NewCollector(p.Cluster)
		newProjectUUIDs, err := c.ScanProjects(r.Context(), dbDomain)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
		projectUUID := mux.Vars(r)["project_id"]
		found := slices.Contains(newProjectUUIDs, projectUUID)
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
	_, err := p.DB.Exec(`UPDATE project_services SET stale = '1' WHERE project_id = $1`, dbProject.ID)
	if respondwith.ObfuscatedErrorText(w, err) {
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
	if !token.Require(w, "project:edit") {
		return
	}
	// domain admins have project edit rights by inheritance.
	domainAccess := token.Check("project:edit_as_outside_admin")
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

	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// validate request
	nm := core.BuildResourceNameMapping(p.Cluster, serviceInfos)
	requested := make(map[db.ServiceType]map[liquid.ResourceName]*maxQuotaChange)
	for _, srvRequest := range parseTarget.Project.Services {
		for _, resRequest := range srvRequest.Resources {
			dbServiceType, dbResourceName, exists := nm.MapFromV1API(srvRequest.Type, resRequest.Name)
			if !exists {
				msg := fmt.Sprintf("no such service and/or resource: %s/%s", srvRequest.Type, resRequest.Name)
				http.Error(w, msg, http.StatusUnprocessableEntity)
				return
			}

			if requested[dbServiceType] == nil {
				requested[dbServiceType] = make(map[liquid.ResourceName]*maxQuotaChange)
			}
			if resRequest.MaxQuota == nil {
				requested[dbServiceType][dbResourceName] = &maxQuotaChange{NewValue: None[uint64]()}
			} else {
				serviceInfo := core.InfoForService(serviceInfos, dbServiceType)
				resInfo := core.InfoForResource(serviceInfo, dbResourceName)
				if !resInfo.HasQuota {
					msg := fmt.Sprintf("resource %s/%s does not track quota", dbServiceType, dbResourceName)
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
				convertedMaxQuota, err := core.ConvertUnitFor(serviceInfo, dbResourceName, requestedMaxQuota)
				if err != nil {
					msg := fmt.Sprintf("invalid input for %s/%s: %s", dbServiceType, dbResourceName, err.Error())
					http.Error(w, msg, http.StatusUnprocessableEntity)
					return
				}
				requested[dbServiceType][dbResourceName] = &maxQuotaChange{NewValue: Some(convertedMaxQuota)}
			}
		}
	}

	// write requested values to DB
	tx, err := p.DB.Begin()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	var services []db.Service
	_, err = tx.Select(&services,
		`SELECT cs.* FROM services cs JOIN project_services ps ON ps.service_id = cs.id and ps.project_id = $1 ORDER BY cs.type`, dbProject.ID)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	for _, srv := range services {
		requestedInService, exists := requested[srv.Type]
		if !exists {
			continue
		}

		_, err := datamodel.ProjectResourceUpdate{
			UpdateResource: func(res *db.ProjectResource, resName liquid.ResourceName) error {
				requestedChange := requestedInService[resName]
				if requestedChange != nil && domainAccess {
					requestedChange.OldValue = res.MaxQuotaFromOutsideAdmin // remember for audit event
					res.MaxQuotaFromOutsideAdmin = requestedChange.NewValue
					return nil
				}
				if requestedChange != nil {
					requestedChange.OldValue = res.MaxQuotaFromLocalAdmin
					res.MaxQuotaFromLocalAdmin = requestedChange.NewValue
				}
				return nil
			},
		}.Run(tx, serviceInfos[srv.Type], p.timeNow(), *dbDomain, *dbProject, srv)
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
	}

	err = tx.Commit()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// write audit trail
	for dbServiceType, requestedInService := range requested {
		for dbResourceName, requestedChange := range requestedInService {
			apiServiceType, apiResourceName, exists := nm.MapToV1API(dbServiceType, dbResourceName)
			if exists {
				p.auditor.Record(audittools.Event{
					Time:       requestTime,
					Request:    r,
					User:       token,
					ReasonCode: http.StatusAccepted,
					Action:     cadf.UpdateAction,
					Target: maxQuotaEventTarget{
						DomainID:        dbDomain.UUID,
						DomainName:      dbDomain.Name,
						ProjectID:       dbProject.UUID, // is empty for domain quota updates, see above
						ProjectName:     dbProject.Name,
						ServiceType:     apiServiceType,
						ResourceName:    apiResourceName,
						RequestedChange: *requestedChange,
					},
				})
			}
		}
	}

	w.WriteHeader(http.StatusAccepted)
}
