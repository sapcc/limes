// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/collector"
	"github.com/sapcc/limes/internal/reports"
	"github.com/sapcc/limes/internal/util"
)

// ListDomains handles GET /v1/domains.
func (p *v1Provider) ListDomains(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains")
	token := p.CheckToken(r)
	if !token.Require(w, "domain:list") {
		return
	}

	// missing services/ resources will lead to empty filter --> empty report
	resources := p.Cluster.SIC.GetResources()
	filter := reports.ReadFilter(r, p.Cluster, resources)
	p.recordReportSpecificity("domain_list", filter)
	domains, err := reports.GetDomains(p.Cluster, nil, p.timeNow(), p.DB, filter, resources)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	respondwith.JSON(w, 200, map[string]any{"domains": domains})
}

// GetDomain handles GET /v1/domains/:domain_id.
func (p *v1Provider) GetDomain(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id")
	token := p.CheckToken(r)
	if !token.Require(w, "domain:show") {
		return
	}
	dbDomain := p.FindDomainFromRequest(w, r)
	if dbDomain == nil {
		return
	}

	resources := p.Cluster.SIC.GetResources()

	filter := reports.ReadFilter(r, p.Cluster, resources)
	p.recordReportSpecificity("domain_show", filter)
	domain, err := GetDomainReport(p.Cluster, *dbDomain, p.timeNow(), p.DB, filter, resources)
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}
	respondwith.JSON(w, 200, map[string]any{"domain": domain})
}

// DiscoverDomains handles POST /v1/domains/discover.
func (p *v1Provider) DiscoverDomains(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/discover")
	token := p.CheckToken(r)
	if !token.Require(w, "domain:discover") {
		return
	}

	c := collector.NewCollector(p.Cluster, p.auditor)
	newDomainUUIDs, err := c.ScanDomains(r.Context(), collector.ScanDomainsOpts{})
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	if len(newDomainUUIDs) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	respondwith.JSON(w, 202, map[string]any{"new_domains": util.IDsToJSON(newDomainUUIDs)})
}

// PutDomain handles PUT /v1/domains/:domain_id.
func (p *v1Provider) PutDomain(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id")
	http.Error(w, "support for setting quotas manually has been removed", http.StatusMethodNotAllowed)
}

// SimulatePutDomain handles POST /v1/domains/:domain_id/simulate-put.
func (p *v1Provider) SimulatePutDomain(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/domains/:id/simulate-put")
	http.Error(w, "support for setting quotas manually has been removed", http.StatusMethodNotAllowed)
}
