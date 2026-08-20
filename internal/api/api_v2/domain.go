// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"net/http"

	"github.com/sapcc/go-api-declarations/opts"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"

	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/api/reports_v2"
	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
)

// handleGetResourcesDomains handles GET /resources/v2/domains.
func (p *v2Provider) handleGetResourcesDomains(r *http.Request, token *gopherpolicy.Token) (_ resourcesv2.DomainGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/domains")
	err = token.Enforce("v2:domain:report_multiple")
	if err != nil {
		return
	}
	return p.commonHandleGetResourcesDomain(r, token, None[string]())
}

// handleGetResourcesDomain handles GET /resources/v2/domains/:domain_uuid.
func (p *v2Provider) handleGetResourcesDomain(r *http.Request, token *gopherpolicy.Token) (_ resourcesv2.DomainGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/domains/:domain_uuid")
	err = token.Enforce("v2:domain:report_single")
	if err != nil {
		return
	}
	return p.commonHandleGetResourcesDomain(r, token, Some(token.Context.Request["domain_uuid"]))
}

// commonHandleGetResourcesDomain handles single- and multi-domain resource calls.
func (p *v2Provider) commonHandleGetResourcesDomain(r *http.Request, token *gopherpolicy.Token, domainUUID Option[string]) (_ resourcesv2.DomainGetResponse, err error) {
	none := resourcesv2.DomainGetResponse{}
	ctx := r.Context()

	scope, err := reports_v2.NewScope(ctx, domainUUID, None[string](), p.DB)
	if err != nil {
		return none, err
	}
	options, err := opts.ParseQueryString[common.DomainResourceReportOpts](r.URL.Query())
	if err != nil {
		return none, respondwith.CustomStatus(http.StatusBadRequest, err)
	}
	filter, err := reports_v2.FilterFromResourceOpts(p.Cluster, options.ResourceReportOpts)
	if err != nil {
		return none, respondwith.CustomStatus(http.StatusBadRequest, err)
	}
	return reports_v2.GetDomainResources(ctx, p.Cluster, token, filter, options, scope, p.timeNow())
}

// handleGetRatesDomains handles GET /rates/v2/domains.
func (p *v2Provider) handleGetRatesDomains(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.DomainGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/domains")
	err = token.Enforce("v2:domain:report_multiple")
	if err != nil {
		return
	}
	return p.commonHandleGetRatesDomain(r, token, None[string]())
}

// handleGetRatesDomain handles GET /rates/v2/domains/:domain_uuid.
func (p *v2Provider) handleGetRatesDomain(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.DomainGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/domains/:domain_uuid")
	err = token.Enforce("v2:domain:report_single")
	if err != nil {
		return
	}
	return p.commonHandleGetRatesDomain(r, token, Some(token.Context.Request["domain_uuid"]))
}

// commonHandleGetRatesDomain handles single- and multi-domain rate calls.
func (p *v2Provider) commonHandleGetRatesDomain(r *http.Request, token *gopherpolicy.Token, domainUUID Option[string]) (_ ratesv2.DomainGetResponse, err error) {
	none := ratesv2.DomainGetResponse{}
	ctx := r.Context()

	scope, err := reports_v2.NewScope(ctx, domainUUID, None[string](), p.DB)
	if err != nil {
		return none, err
	}
	options, err := opts.ParseQueryString[common.DomainRateReportOpts](r.URL.Query())
	if err != nil {
		return none, err
	}
	filter, err := reports_v2.FilterFromRateOpts(p.Cluster, options.RateReportOpts)
	if err != nil {
		return none, err
	}
	result, err := reports_v2.GetDomainRates(ctx, p.Cluster, token, filter, options, scope)
	if err != nil {
		return none, err
	}
	return result, nil
}
