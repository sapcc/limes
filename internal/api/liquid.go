// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

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

	serviceType := db.ServiceType(r.URL.Query().Get("service_type"))
	if serviceType == "" {
		http.Error(w, "missing required parameter: service_type", http.StatusBadRequest)
		return
	}

	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ErrorText(w, err) {
		return
	}

	// TODO prevent requests with `liquid-$SERVICE_TYPE` when limesctl was adjusted
	normalizedServiceType := db.ServiceType(strings.TrimPrefix(string(serviceType), "liquid-"))
	if !core.HasService(serviceInfos, serviceType) && !core.HasService(serviceInfos, normalizedServiceType) {
		http.Error(w, "invalid service type", http.StatusBadRequest)
		return
	}

	backchannel := datamodel.NewCapacityScrapeBackchannel(p.Cluster, p.DB)
	serviceCapacityRequest, err := core.BuildServiceCapacityRequest(backchannel, p.Cluster.Config.AvailabilityZones, normalizedServiceType, serviceInfos[normalizedServiceType].Resources)
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

	serviceType := db.ServiceType(r.URL.Query().Get("service_type"))
	if serviceType == "" {
		http.Error(w, "missing required parameter: service_type", http.StatusBadRequest)
		return
	}

	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ErrorText(w, err) {
		return
	}

	// TODO prevent requests with `liquid-$SERVICE_TYPE` when limesctl was adjusted
	normalizedServiceType := db.ServiceType(strings.TrimPrefix(string(serviceType), "liquid-"))
	if !core.HasService(serviceInfos, serviceType) && !core.HasService(serviceInfos, normalizedServiceType) {
		http.Error(w, "invalid service type", http.StatusBadRequest)
		return
	}

	projectID := r.URL.Query().Get("project_id")
	if projectID == "" {
		http.Error(w, "missing required parameter: project_id", http.StatusBadRequest)
		return
	}

	var dbProject db.Project
	err = p.DB.SelectOne(&dbProject, `SELECT * FROM projects WHERE uuid = $1`, projectID)
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

	serviceUsageRequest, err := core.BuildServiceUsageRequest(project, p.Cluster.Config.AvailabilityZones, serviceInfos[normalizedServiceType].UsageReportNeedsProjectMetadata)
	if respondwith.ErrorText(w, err) {
		return
	}

	respondwith.JSON(w, http.StatusOK, serviceUsageRequest)
}
