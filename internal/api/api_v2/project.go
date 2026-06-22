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

// handleGetResourcesProjects handles GET /resources/v2/projects.
func (p *v2Provider) handleGetResourcesProjects(r *http.Request, token *gopherpolicy.Token) (_ resourcesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/projects")
	none := resourcesv2.ProjectGetResponse{}

	// important: validate scope before token.Enforce, so that URL/ query domain_uuid is written back to token scope
	options, err := opts.ParseQueryString[common.ProjectResourceReportOpts](r.URL.Query())
	if err != nil {
		return none, err
	}
	_, err = reports_v2.NewScope(true, r, options.DomainUUID, token, p.DB)
	if err != nil {
		return
	}
	err = token.Enforce("v2:project:report_multiple")
	if err != nil {
		return none, err
	}
	_, err = reports_v2.FilterFromResourceOpts(p.Cluster, options.ResourceReportOpts)
	if err != nil {
		return none, err
	}
	return none, nil
}

// handleGetResourcesProject handles GET /resources/v2/projects/:project_id.
func (p *v2Provider) handleGetResourcesProject(r *http.Request, token *gopherpolicy.Token) (_ resourcesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/projects/:project_id")
	none := resourcesv2.ProjectGetResponse{}

	// important: validate scope before token.Enforce, so that URL/ query domain_uuid is written back to token scope
	options, err := opts.ParseQueryString[common.ProjectResourceReportOpts](r.URL.Query())
	if err != nil {
		return none, err
	}
	_, err = reports_v2.NewScope(true, r, options.DomainUUID, token, p.DB)
	if err != nil {
		return
	}
	err = token.Enforce("v2:project:report_single")
	if err != nil {
		return none, err
	}
	_, err = reports_v2.FilterFromResourceOpts(p.Cluster, options.ResourceReportOpts)
	if err != nil {
		return none, err
	}
	return none, nil
}

// handleGetRatesProjects handles GET /rates/v2/projects.
func (p *v2Provider) handleGetRatesProjects(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/projects")
	none := ratesv2.ProjectGetResponse{}

	// important: validate scope before token.Enforce, so that URL/ query domain_uuid is written back to token scope
	options, err := opts.ParseQueryString[common.ProjectRateReportOpts](r.URL.Query())
	if err != nil {
		return none, err
	}
	_, err = reports_v2.NewScope(true, r, options.DomainUUID, token, p.DB)
	if err != nil {
		return
	}
	err = token.Enforce("v2:project:report_multiple")
	if err != nil {
		return none, err
	}
	_, err = reports_v2.FilterFromRateOpts(p.Cluster, options.RateReportOpts)
	if err != nil {
		return none, err
	}
	return none, nil
}

// handleGetRatesProject handles GET /rates/v2/projects/:project_id.
func (p *v2Provider) handleGetRatesProject(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/projects/:project_id")
	none := ratesv2.ProjectGetResponse{}

	// important: validate scope before token.Enforce, so that URL/ query domain_uuid is written back to token scope
	options, err := opts.ParseQueryString[common.ProjectRateReportOpts](r.URL.Query())
	if err != nil {
		return none, err
	}
	_, err = reports_v2.NewScope(true, r, options.DomainUUID, token, p.DB)
	if err != nil {
		return
	}
	err = token.Enforce("v2:project:report_single")
	if err != nil {
		return none, err
	}
	_, err = reports_v2.FilterFromRateOpts(p.Cluster, options.RateReportOpts)
	if err != nil {
		return none, err
	}
	return none, nil
}
