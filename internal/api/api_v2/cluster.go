// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"net/http"

	"github.com/sapcc/go-api-declarations/opts"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"

	"github.com/sapcc/limes/internal/api/reports_v2"
	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
)

// handleGetResourcesCluster handles GET /resources/v2/cluster.
func (p *v2Provider) handleGetResourcesCluster(r *http.Request, token *gopherpolicy.Token) (_ resourcesv2.ClusterGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/cluster")
	none := resourcesv2.ClusterGetResponse{}

	err = token.Enforce("v2:cluster:report_single")
	if err != nil {
		return none, err
	}
	options, err := opts.ParseQueryString[common.ClusterResourceReportOpts](r.URL.Query())
	if err != nil {
		return none, err
	}
	_, err = reports_v2.FilterFromResourceOpts(p.Cluster, options.ResourceReportOpts)
	if err != nil {
		return none, err
	}
	return none, nil
}

// handleGetRatesCluster handles GET /rates/v2/cluster.
func (p *v2Provider) handleGetRatesCluster(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.ClusterGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/cluster")
	none := ratesv2.ClusterGetResponse{}

	err = token.Enforce("v2:cluster:report_single")
	if err != nil {
		return none, err
	}
	options, err := opts.ParseQueryString[common.ClusterRateReportOpts](r.URL.Query())
	if err != nil {
		return none, err
	}
	filter, err := reports_v2.FilterFromRateOpts(p.Cluster, options.RateReportOpts)
	if err != nil {
		return none, err
	}
	result, err := reports_v2.GetClusterRates(p.Cluster, token, filter, options)
	if err != nil {
		return none, err
	}
	return result, nil
}
