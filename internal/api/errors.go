// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"net/http"

	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/reports"
)

// ListScrapeErrors handles GET /v1/admin/scrape-errors.
func (p *v1Provider) ListScrapeErrors(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v1/admin/scrape-errors")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show_errors") {
		return
	}

	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ErrorText(w, err) {
		return
	}

	scrapeErrors, err := reports.GetScrapeErrors(p.DB, reports.ReadFilter(r, p.Cluster, serviceInfos))
	if respondwith.ErrorText(w, err) {
		return
	}

	respondwith.JSON(w, http.StatusOK, map[string]any{"scrape_errors": scrapeErrors})
}

// ListRateScrapeErrors handles GET /rates/v1/admin/scrape-errors.
//
// Deprecated:
func (p *v1Provider) ListRateScrapeErrors(w http.ResponseWriter, r *http.Request) {
	p.ListScrapeErrors(w, r)
}
