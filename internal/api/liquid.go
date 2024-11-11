/******************************************************************************
*
*  Copyright 2024 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
)

// GetServiceCapacityRequest handles GET /admin/liquid/service-capacity-request?service_type=:type.
func (p *v1Provider) GetServiceCapacityRequest(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/admin/liquid/service-capacity-request")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show") {
		return
	}

	serviceType := r.URL.Query().Get("service_type")
	if serviceType == "" {
		http.Error(w, "missing required parameter: service_type", http.StatusBadRequest)
		return
	}

	plugin, ok := p.Cluster.CapacityPlugins[serviceType]
	if !ok {
		http.Error(w, "invalid service type", http.StatusBadRequest)
		return
	}

	backchannel := datamodel.NewCapacityPluginBackchannel(p.Cluster, p.DB)
	serviceCapacityRequest, err := plugin.BuildServiceCapacityRequest(backchannel, p.Cluster.Config.AvailabilityZones)
	if errors.Is(err, core.ErrNotALiquid) {
		http.Error(w, "capacity plugin does not support LIQUID requests", http.StatusNotImplemented)
		return
	}
	if respondwith.ErrorText(w, err) {
		return
	}

	respondwith.JSON(w, http.StatusOK, serviceCapacityRequest)
}

// p.GetServiceUsageRequest handles GET /admin/liquid/service-usage-request?service_type=:type&project_id=:id.
func (p *v1Provider) GetServiceUsageRequest(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/admin/liquid/service-usage-request")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show") {
		return
	}

	serviceType := r.URL.Query().Get("service_type")
	if serviceType == "" {
		http.Error(w, "missing required parameter: service_type", http.StatusBadRequest)
		return
	}

	plugin, ok := p.Cluster.QuotaPlugins[db.ServiceType(serviceType)]
	if !ok {
		http.Error(w, "invalid service type", http.StatusBadRequest)
		return
	}

	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		http.Error(w, "missing required parameter: project_id", http.StatusBadRequest)
		return
	}

	var dbProject db.Project
	err := p.DB.SelectOne(&dbProject, `SELECT * FROM projects WHERE uuid = $1`, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	} else if respondwith.ErrorText(w, err) {
		return
	}

	var dbDomain db.Domain
	err = p.DB.SelectOne(&dbDomain, `SELECT * FROM domains WHERE id = $1`, dbProject.DomainID)
	if respondwith.ErrorText(w, err) {
		return
	}

	domain := core.KeystoneDomainFromDB(dbDomain)
	project := core.KeystoneProjectFromDB(dbProject, domain)

	serviceUsageRequest, err := plugin.BuildServiceUsageRequest(project, p.Cluster.Config.AvailabilityZones)
	if errors.Is(err, core.ErrNotALiquid) {
		http.Error(w, "quota plugin does not support LIQUID requests", http.StatusNotImplemented)
		return
	}
	if respondwith.ErrorText(w, err) {
		return
	}

	respondwith.JSON(w, http.StatusOK, serviceUsageRequest)
}
