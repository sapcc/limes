// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"

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

	maybeServiceInfo, err := p.Cluster.InfoForService(serviceType)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		http.Error(w, "unknown service type", http.StatusBadRequest)
		return
	}

	backchannel := datamodel.NewCapacityScrapeBackchannel(p.Cluster, p.DB)
	serviceCapacityRequest, err := core.BuildServiceCapacityRequest(backchannel, p.Cluster.Config.AvailabilityZones, serviceType, serviceInfo.Resources)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	respondwith.JSON(w, http.StatusOK, serviceCapacityRequest)
}

var getServiceUsageRequestAttributesQuery = sqlext.SimplifyWhitespace(`
	SELECT s.usage_report_needs_project_metadata, ps.serialized_scrape_state
	  FROM project_services ps
	  JOIN services s ON ps.service_id = s.id
	  WHERE ps.project_id = $1 AND s.type = $2
`)

// GetServiceUsageRequest handles GET /admin/liquid/service-usage-request?service_type=:type&project_id=:id.
func (p *v1Provider) GetServiceUsageRequest(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/admin/liquid/service-usage-request")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show") {
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
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	var dbDomain db.Domain
	err = p.DB.SelectOne(&dbDomain, `SELECT * FROM domains WHERE id = $1`, dbProject.DomainID)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	domain := core.KeystoneDomainFromDB(dbDomain)
	project := core.KeystoneProjectFromDB(dbProject, domain)

	serviceType := db.ServiceType(r.URL.Query().Get("service_type"))
	if serviceType == "" {
		http.Error(w, "missing required parameter: service_type", http.StatusBadRequest)
		return
	}

	var (
		usageReportNeedsProjectMetadata bool
		prevSerializedState             string
	)
	err = p.DB.QueryRow(getServiceUsageRequestAttributesQuery, dbProject.ID, serviceType).
		Scan(&usageReportNeedsProjectMetadata, &prevSerializedState)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "unknown service type", http.StatusBadRequest)
		return
	} else if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	serviceUsageRequest := core.BuildServiceUsageRequest(project, p.Cluster.Config.AvailabilityZones, usageReportNeedsProjectMetadata, prevSerializedState)
	respondwith.JSON(w, http.StatusOK, serviceUsageRequest)
}
