// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"net/http"

	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"

	"github.com/sapcc/limes/internal/api/reports_v2"
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
)

// handleGetResourcesInfo handles GET /resources/v2/info.
func (p *v2Provider) handleGetResourcesInfo(r *http.Request, token *gopherpolicy.Token) (resourcesv2.InfoReport, error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/info")
	return reports_v2.GetResourcesInfo(p.Cluster, token, p.timeNow(), p.Cluster.SIC.GetSnapshot())
}

// handleGetRatesInfo handles GET /rates/v2/info.
func (p *v2Provider) handleGetRatesInfo(r *http.Request, token *gopherpolicy.Token) (ratesv2.InfoReport, error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/info")
	return reports_v2.GetRatesInfo(p.Cluster, token, p.Cluster.SIC.GetSnapshot())
}
