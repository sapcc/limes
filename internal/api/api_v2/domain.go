// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"net/http"

	"github.com/sapcc/go-api-declarations/opts"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"

	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/api/reports_v2"
	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
)

// handleGetResourcesDomains handles GET /resources/v2/domains.
func (p *v2Provider) handleGetResourcesDomains(r *http.Request, token *gopherpolicy.Token) (_ resourcesv2.DomainGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/domains")
	none := resourcesv2.DomainGetResponse{}

	err = token.Enforce("v2:domain:report")
	if err != nil {
		return none, err
	}
	_, err = reports_v2.NewScope(false, r, None[string](), token, p.DB)
	if err != nil {
		return none, err
	}
	options, err := opts.ParseQueryString[common.DomainResourceReportOpts](r.URL.Query())
	if err != nil {
		return none, err
	}
	_, err = reports_v2.FilterFromResourceOpts(p.Cluster, options.ResourceReportOpts)
	if err != nil {
		return none, err
	}
	return none, nil
}

// handleGetResourcesDomain handles GET /resources/v2/domains/:domain_uuid.
func (p *v2Provider) handleGetResourcesDomain(r *http.Request, token *gopherpolicy.Token) (_ resourcesv2.DomainGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/domains/:domain_uuid")
	none := resourcesv2.DomainGetResponse{}

	err = token.Enforce("v2:domain:report")
	if err != nil {
		return none, err
	}
	_, err = reports_v2.NewScope(false, r, None[string](), token, p.DB)
	if err != nil {
		return none, err
	}
	options, err := opts.ParseQueryString[common.DomainResourceReportOpts](r.URL.Query())
	if err != nil {
		return none, err
	}
	_, err = reports_v2.FilterFromResourceOpts(p.Cluster, options.ResourceReportOpts)
	if err != nil {
		return none, err
	}
	return none, nil
}

// handleGetRatesDomains handles GET /rates/v2/domains.
func (p *v2Provider) handleGetRatesDomains(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.DomainGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/domains")
	none := ratesv2.DomainGetResponse{}

	err = token.Enforce("v2:domain:report")
	if err != nil {
		return none, err
	}
	_, err = reports_v2.NewScope(false, r, None[string](), token, p.DB)
	if err != nil {
		return none, err
	}
	options, err := opts.ParseQueryString[common.DomainRateReportOpts](r.URL.Query())
	if err != nil {
		return none, err
	}
	_, err = reports_v2.FilterFromRateOpts(p.Cluster, options.RateReportOpts)
	if err != nil {
		return none, err
	}
	return none, nil
}

// handleGetRatesDomain handles GET /rates/v2/domains/:domain_uuid.
func (p *v2Provider) handleGetRatesDomain(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.DomainGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/domains/:domain_uuid")
	none := ratesv2.DomainGetResponse{}

	err = token.Enforce("v2:domain:report")
	if err != nil {
		return none, err
	}
	_, err = reports_v2.NewScope(false, r, None[string](), token, p.DB)
	if err != nil {
		return none, err
	}
	options, err := opts.ParseQueryString[common.DomainRateReportOpts](r.URL.Query())
	if err != nil {
		return none, err
	}
	_, err = reports_v2.FilterFromRateOpts(p.Cluster, options.RateReportOpts)
	if err != nil {
		return none, err
	}
	return none, nil
}
