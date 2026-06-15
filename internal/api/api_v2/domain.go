// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"net/http"

	"github.com/sapcc/go-bits/httpapi"

	"github.com/sapcc/limes/internal/api/reports_v2"
)

// GetResourcesDomains handles GET /resources/v2/domains.
func (p *v2Provider) GetResourcesDomains(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/domains")

	token := p.CheckToken(r)
	if !token.Require(w, "v2:domain:report") {
		return
	}
	_, ok := reports_v2.ScopeConfig{}.NewScope(w, r, p.DB)
	if !ok {
		return
	}
}

// GetResourcesDomain handles GET /resources/v2/domains/:domain_id?.
func (p *v2Provider) GetResourcesDomain(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/domains/:domain_id")

	token := p.CheckToken(r)
	if !token.Require(w, "v2:domain:report") {
		return
	}
	_, ok := reports_v2.ScopeConfig{RequirePathDomainID: true}.NewScope(w, r, p.DB)
	if !ok {
		return
	}
}

// GetRatesDomains handles GET /rates/v2/domains.
func (p *v2Provider) GetRatesDomains(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/domains")

	token := p.CheckToken(r)
	if !token.Require(w, "v2:domain:report") {
		return
	}
	_, ok := reports_v2.ScopeConfig{}.NewScope(w, r, p.DB)
	if !ok {
		return
	}
}

// GetRatesDomain handles GET /rates/v2/domains/:domain_id.
func (p *v2Provider) GetRatesDomain(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/domains/:domain_id")

	token := p.CheckToken(r)
	if !token.Require(w, "v2:domain:report") {
		return
	}
	_, ok := reports_v2.ScopeConfig{RequirePathDomainID: true}.NewScope(w, r, p.DB)
	if !ok {
		return
	}
}
