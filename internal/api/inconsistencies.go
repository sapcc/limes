// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/reports"
)

// ListInconsistencies handles GET /v1/inconsistencies.
func (p *v1Provider) ListInconsistencies(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/inconsistencies")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show") {
		return
	}

	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ErrorText(w, err) {
		return
	}

	inconsistencies, err := reports.GetInconsistencies(p.Cluster, p.DB, reports.ReadFilter(r, p.Cluster, serviceInfos), serviceInfos)
	if respondwith.ErrorText(w, err) {
		return
	}

	respondwith.JSON(w, 200, map[string]any{"inconsistencies": inconsistencies})
}
