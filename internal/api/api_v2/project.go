// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"net/http"

	"github.com/sapcc/go-bits/httpapi"

	"github.com/sapcc/limes/internal/api/reports_v2"
)

// GetResourcesProjects handles GET /resources/v2/projects.
func (p *v2Provider) GetResourcesProjects(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/projects")

	token := p.CheckToken(r)
	if !token.Require(w, "v2:project:report") {
		return
	}
	_, ok := reports_v2.ScopeConfig{AllowQueryDomainID: true}.NewScope(w, r, p.DB)
	if !ok {
		return
	}
}

// GetResourcesProject handles GET /resources/v2/projects/:project_id.
func (p *v2Provider) GetResourcesProject(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/projects/:project_id")

	token := p.CheckToken(r)
	// important: validate scope before token.Require, else "rule:domain_scope" will fail!
	scope, ok := reports_v2.ScopeConfig{RequirePathProjectID: true}.NewScope(w, r, p.DB)
	if !ok {
		return
	}
	scope.UpdateTokenContext(token)
	if !token.Require(w, "v2:project:report") {
		return
	}
}

// GetRatesProjects handles GET /rates/v2/projects.
func (p *v2Provider) GetRatesProjects(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/projects")

	token := p.CheckToken(r)
	if !token.Require(w, "v2:project:report") {
		return
	}
	_, ok := reports_v2.ScopeConfig{AllowQueryDomainID: true}.NewScope(w, r, p.DB)
	if !ok {
		return
	}
}

// GetRatesProject handles GET /rates/v2/projects/:project_id.
func (p *v2Provider) GetRatesProject(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/projects/:project_id")

	token := p.CheckToken(r)
	// important: validate scope before token.Require, else "rule:domain_scope" will fail!
	scope, ok := reports_v2.ScopeConfig{RequirePathProjectID: true}.NewScope(w, r, p.DB)
	if !ok {
		return
	}
	scope.UpdateTokenContext(token)
	if !token.Require(w, "v2:project:report") {
		return
	}
}
