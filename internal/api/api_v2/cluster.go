// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"net/http"

	"github.com/sapcc/go-bits/httpapi"

	"github.com/sapcc/limes/internal/api/reports_v2"
)

// GetResourcesCluster handles GET /resources/v2/cluster.
func (p *v2Provider) GetResourcesCluster(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/cluster")

	token := p.CheckToken(r)
	if !token.Require(w, "v2:cluster:report") {
		return
	}
	_, ok := reports_v2.ScopeConfig{}.NewScope(w, r, p.DB)
	if !ok {
		return
	}
}

// GetRatesCluster handles GET /rates/v2/cluster.
func (p *v2Provider) GetRatesCluster(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/cluster")

	token := p.CheckToken(r)
	if !token.Require(w, "v2:cluster:report") {
		return
	}
	_, ok := reports_v2.ScopeConfig{}.NewScope(w, r, p.DB)
	if !ok {
		return
	}
}
