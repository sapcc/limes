// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"context"
	"errors"
	"net/http"

	"github.com/sapcc/go-api-declarations/liquid"
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

// handleGetResourcesProjects handles GET /resources/v2/projects.
func (p *v2Provider) handleGetResourcesProjects(r *http.Request, token *gopherpolicy.Token) (_ resourcesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/projects")
	var scopeDomainUUID Option[string]
	scopeDomainUUID, err = p.checkAuthZForReportWithMultipleProjects(r, token)
	if err != nil {
		return
	}
	return p.commonHandleGetResourcesProject(r, token, scopeDomainUUID, None[string]())
}

// handleGetResourcesProject handles GET /resources/v2/projects/:project_uuid.
func (p *v2Provider) handleGetResourcesProject(r *http.Request, token *gopherpolicy.Token) (_ resourcesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/projects/:project_uuid")
	var scopeDomainUUID Option[string]
	scopeDomainUUID, err = p.checkAuthZForReportWithSingleProject(r.Context(), token)
	if err != nil {
		return
	}
	return p.commonHandleGetResourcesProject(r, token, scopeDomainUUID, Some(token.Context.Request["project_uuid"]))
}

// commonHandleGetResourcesProject handles single- and multi-project rate calls.
func (p *v2Provider) commonHandleGetResourcesProject(r *http.Request, token *gopherpolicy.Token, domainUUID, projectUUID Option[string]) (_ resourcesv2.ProjectGetResponse, err error) {
	none := resourcesv2.ProjectGetResponse{}
	ctx := r.Context()

	scope, err := reports_v2.NewScope(ctx, domainUUID, projectUUID, p.DB)
	if err != nil {
		return none, err
	}
	options, err := opts.ParseQueryString[common.ProjectResourceReportOpts](r.URL.Query())
	if err != nil {
		return none, respondwith.CustomStatus(http.StatusBadRequest, err)
	}
	if options.DomainUUID.IsSome() && projectUUID.IsSome() {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("query domain_uuid cannot be set, when URL project_uuid is set"))
	}

	// important: project-resources have special ?with= params which are subject to special permissions
	// we can only return one error, so
	if options.WithTiming {
		err = token.Enforce("v2:project:with_timing")
		if err != nil {
			return none, err
		}
	}
	if options.WithSubresources {
		err = token.Enforce("v2:project:with_subresources")
		if err != nil {
			return none, err
		}
	}
	if options.WithHistoricalUsage {
		err = token.Enforce("v2:project:with_historical_usage")
		if err != nil {
			return none, err
		}
	}

	filter, err := reports_v2.FilterFromResourceOpts(p.Cluster, options.ResourceReportOpts)
	if err != nil {
		return none, respondwith.CustomStatus(http.StatusBadRequest, err)
	}
	return reports_v2.GetProjectResources(ctx, p.Cluster, token, filter, options, scope, p.timeNow())
}

// handleGetRatesProjects handles GET /rates/v2/projects.
func (p *v2Provider) handleGetRatesProjects(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/projects")
	var scopeDomainUUID Option[string]
	scopeDomainUUID, err = p.checkAuthZForReportWithMultipleProjects(r, token)
	if err != nil {
		return
	}
	return p.commonHandleGetRatesProject(r, token, scopeDomainUUID, None[string]())
}

// handleGetRatesProject handles GET /rates/v2/projects/:project_uuid.
func (p *v2Provider) handleGetRatesProject(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/projects/:project_uuid")
	var scopeDomainUUID Option[string]
	scopeDomainUUID, err = p.checkAuthZForReportWithSingleProject(r.Context(), token)
	if err != nil {
		return
	}
	return p.commonHandleGetRatesProject(r, token, scopeDomainUUID, Some(token.Context.Request["project_uuid"]))
}

// commonHandleGetRatesProject handles single- and multi-project rate calls.
func (p *v2Provider) commonHandleGetRatesProject(r *http.Request, token *gopherpolicy.Token, domainUUID, projectUUID Option[string]) (_ ratesv2.ProjectGetResponse, err error) {
	none := ratesv2.ProjectGetResponse{}
	ctx := r.Context()

	scope, err := reports_v2.NewScope(ctx, domainUUID, projectUUID, p.DB)
	if err != nil {
		return none, err
	}
	options, err := opts.ParseQueryString[common.ProjectRateReportOpts](r.URL.Query())
	if err != nil {
		return none, respondwith.CustomStatus(http.StatusBadRequest, err)
	}
	if options.DomainUUID.IsSome() && projectUUID.IsSome() {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("query domain_uuid cannot be set, when URL project_uuid is set"))
	}

	filter, err := reports_v2.FilterFromRateOpts(p.Cluster, options.RateReportOpts)
	if err != nil {
		return none, respondwith.CustomStatus(http.StatusBadRequest, err)
	}
	result, err := reports_v2.GetProjectRates(ctx, p.Cluster, token, filter, options, scope)
	if err != nil {
		return none, err
	}
	return result, nil
}

// checkAuthZForReportWithMultipleProjects handles AuthZ for GET /{resources,rates}/v2/projects.
func (p *v2Provider) checkAuthZForReportWithMultipleProjects(r *http.Request, token *gopherpolicy.Token) (scopeDomainUUID Option[string], err error) {
	// full query parsing will be done later; for now we just need ?domain_uuid= because it influences AuthZ
	if queryDomainUUID := r.URL.Query().Get("domain_uuid"); queryDomainUUID != "" {
		if token.Context.Request == nil {
			token.Context.Request = make(map[string]string, 1)
		}
		token.Context.Request["domain_uuid"] = queryDomainUUID
		return Some(queryDomainUUID), token.Enforce("v2:project:report_multiple")
	} else {
		return None[string](), token.Enforce("v2:project:report_multiple")
	}
}

// checkAuthZForReportWithSingleProject handles AuthZ for GET /{resources,rates}/v2/projects/:project_uuid.
func (p *v2Provider) checkAuthZForReportWithSingleProject(ctx context.Context, token *gopherpolicy.Token) (scopeDomainUUID Option[string], err error) {
	domain, _, err := p.checkProjectAccess(ctx, token, liquid.ProjectUUID(token.Context.Request["project_uuid"]), "v2:project:report_single")
	if err != nil {
		return None[string](), err
	}
	return Some(domain.UUID), nil
}
