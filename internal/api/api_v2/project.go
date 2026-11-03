// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-api-declarations/opts"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"

	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/api/reports_v2"
	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/audit"
	"github.com/sapcc/limes/internal/db"
)

// handleGetResourcesProjects handles GET /resources/v2/projects.
func (p *v2Provider) handleGetResourcesProjects(r *http.Request, token *gopherpolicy.Token) (_ resourcesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/projects")
	scope, err := p.checkAuthZForReportWithMultipleProjects(r, token)
	if err != nil {
		return
	}
	return p.commonHandleGetResourcesProject(r, token, scope)
}

// handleGetResourcesProject handles GET /resources/v2/projects/:project_uuid.
func (p *v2Provider) handleGetResourcesProject(r *http.Request, token *gopherpolicy.Token) (_ resourcesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/projects/:project_uuid")
	scope, err := p.checkAuthZForReportWithSingleProject(r.Context(), token)
	if err != nil {
		return
	}
	return p.commonHandleGetResourcesProject(r, token, scope)
}

// commonHandleGetResourcesProject handles single- and multi-project resource calls.
func (p *v2Provider) commonHandleGetResourcesProject(r *http.Request, token *gopherpolicy.Token, scope reports_v2.Scope) (_ resourcesv2.ProjectGetResponse, err error) {
	none := resourcesv2.ProjectGetResponse{}
	ctx := r.Context()

	options, err := opts.ParseQueryString[common.ProjectResourceReportOpts](r.URL.Query())
	if err != nil {
		return none, respondwith.CustomStatus(http.StatusBadRequest, err)
	}
	if _, isProjectScope := scope.(reports_v2.ProjectScope); options.DomainUUID.IsSome() && isProjectScope {
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
	return reports_v2.GetProjectResources(ctx, p.Cluster, filter, options, scope, p.timeNow())
}

// handleGetRatesProjects handles GET /rates/v2/projects.
func (p *v2Provider) handleGetRatesProjects(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/projects")
	scope, err := p.checkAuthZForReportWithMultipleProjects(r, token)
	if err != nil {
		return
	}
	return p.commonHandleGetRatesProject(r, scope)
}

// handleGetRatesProject handles GET /rates/v2/projects/:project_uuid.
func (p *v2Provider) handleGetRatesProject(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/projects/:project_uuid")
	scope, err := p.checkAuthZForReportWithSingleProject(r.Context(), token)
	if err != nil {
		return
	}
	return p.commonHandleGetRatesProject(r, scope)
}

// commonHandleGetRatesProject handles single- and multi-project rate calls.
func (p *v2Provider) commonHandleGetRatesProject(r *http.Request, scope reports_v2.Scope) (_ ratesv2.ProjectGetResponse, err error) {
	none := ratesv2.ProjectGetResponse{}
	ctx := r.Context()

	options, err := opts.ParseQueryString[common.ProjectRateReportOpts](r.URL.Query())
	if err != nil {
		return none, respondwith.CustomStatus(http.StatusBadRequest, err)
	}
	if _, isProjectScope := scope.(reports_v2.ProjectScope); options.DomainUUID.IsSome() && isProjectScope {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("query domain_uuid cannot be set, when URL project_uuid is set"))
	}

	filter, err := reports_v2.FilterFromRateOpts(p.Cluster, options.RateReportOpts)
	if err != nil {
		return none, respondwith.CustomStatus(http.StatusBadRequest, err)
	}
	result, err := reports_v2.GetProjectRates(ctx, p.Cluster, filter, options, scope)
	if err != nil {
		return none, err
	}
	return result, nil
}

// checkAuthZForReportWithMultipleProjects handles AuthZ for GET /{resources,rates}/v2/projects.
func (p *v2Provider) checkAuthZForReportWithMultipleProjects(r *http.Request, token *gopherpolicy.Token) (reports_v2.Scope, error) {
	// full query parsing will be done later; for now we just need ?domain_uuid= because it influences AuthZ
	if queryDomainUUID := r.URL.Query().Get("domain_uuid"); queryDomainUUID != "" {
		if token.Context.Request == nil {
			token.Context.Request = make(map[string]string, 1)
		}
		return p.checkDomainAccess(r.Context(), token, queryDomainUUID, "v2:project:report_multiple")
	} else {
		return reports_v2.ClusterScope{}, token.Enforce("v2:project:report_multiple")
	}
}

// checkAuthZForReportWithSingleProject handles AuthZ for GET /{resources,rates}/v2/projects/:project_uuid.
func (p *v2Provider) checkAuthZForReportWithSingleProject(ctx context.Context, token *gopherpolicy.Token) (reports_v2.Scope, error) {
	return p.checkProjectAccess(ctx, token, liquid.ProjectUUID(token.Context.Request["project_uuid"]), "v2:project:report_single")
}

// (p *v2Provider) handlePatchResourcesProject handles PATCH /resources/v2/projects/:project_uuid/:service_type/:resource_name.
func (p *v2Provider) handlePatchResourcesProject(r *http.Request, token *gopherpolicy.Token) (none any, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/projects/:project_uuid/:service_type/:resource_name")
	var (
		ctx = r.Context()
		sis = p.Cluster.SIC.GetSnapshot()
		now = p.timeNow()
	)

	scope, err := p.checkProjectAccess(ctx, token, liquid.ProjectUUID(token.Context.Request["project_uuid"]), "v2:project:resource_update")
	if err != nil {
		return none, err
	}

	// validate request contents
	serviceType := db.ServiceType(mux.Vars(r)["service_type"])
	resourceName := liquid.ResourceName(mux.Vars(r)["resource_name"])
	req, err := parseRequestBodyAs[resourcesv2.ProjectResourcePatchRequest](r)
	if err != nil {
		return none, err
	}
	_, sExists := sis.GetServiceForType(serviceType)
	if !sExists {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchService)
	}
	res, rExists := sis.GetResourceForPath(db.ResourcePath{ServiceType: serviceType, ResourceName: resourceName})
	if !rExists {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchResource)
	}
	if !res.HasQuota {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errDoesNotTrackQuota)
	}

	// get projectResource
	projectResource, err := db.ProjectResourceStore.SelectOneWhere(ctx, p.DB, "project_id = $1 AND resource_id = $2", scope.Project.ID, res.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return none, respondwith.CustomStatus(http.StatusNotFound, errNoSuchProjectResource)
	}
	if err != nil {
		return none, err
	}
	if projectResource.Forbidden {
		return none, respondwith.CustomStatus(http.StatusForbidden, errResourceForbidden)
	}

	// handle autogrowth change
	oldAutogrowth := projectResource.ForbidAutogrowth
	dirty := false
	if newValue, requested := req.ForbidAutogrowth.Unpack(); requested {
		behavior := p.Cluster.CommitmentBehaviorForResourcePath(res.Path).ForDomain(scope.Domain.Name)
		if len(behavior.Durations) == 0 {
			return none, respondwith.CustomStatus(http.StatusBadRequest, errDoesNotHaveCommitments)
		}
		if projectResource.ForbidAutogrowth != newValue {
			dirty = true
			projectResource.ForbidAutogrowth = newValue
		}
	}

	// handle max-quota change
	oldMaxQuota := None[uint64]()
	newMaxQuota := None[uint64]()
	if newValueUnconverted, requested := req.MaxQuota.Unpack(); requested {
		err = token.Enforce("v2:project:resource_update_admin")
		if err != nil {
			return none, err
		}
		newMaxQuota = newValueUnconverted.AsOption()
		if projectResource.MaxQuotaFromOutsideAdmin != newMaxQuota {
			dirty = true
			oldMaxQuota = projectResource.MaxQuotaFromOutsideAdmin
			projectResource.MaxQuotaFromOutsideAdmin = newMaxQuota
		}
	}

	// early return or update
	if !dirty {
		return none, nil
	}
	err = db.ProjectResourceStore.Update(ctx, p.DB, projectResource)
	if err != nil {
		return none, err
	}

	// audit log entry
	p.auditor.Record(audittools.Event{
		Time:       now,
		Request:    r,
		User:       token,
		ReasonCode: http.StatusNoContent,
		Action:     cadf.UpdateAction,
		Target: audit.V2ProjectResourceEventTarget{
			DomainID:     scope.Domain.UUID,
			DomainName:   scope.Domain.Name,
			ProjectID:    scope.Project.UUID,
			ProjectName:  scope.Project.Name,
			ServiceType:  serviceType,
			ResourceName: resourceName,
			RequestedChange: audit.V2ProjectResourceChange{
				OldMaxQuota:         oldMaxQuota,
				NewMaxQuota:         newMaxQuota,
				OldForbidAutogrowth: oldAutogrowth,
				NewForbidAutogrowth: projectResource.ForbidAutogrowth,
			},
		},
	})

	return none, nil
}

// (p *v2Provider) handlePatchRatesProject handles PATCH /rates/v2/projects/:project_uuid/:service_type/:rate_name.
func (p *v2Provider) handlePatchRatesProject(r *http.Request, token *gopherpolicy.Token) (none any, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/projects/:project_uuid/:service_type/:rate_name")
	var (
		ctx = r.Context()
		sis = p.Cluster.SIC.GetSnapshot()
		now = p.timeNow()
	)

	scope, err := p.checkProjectAccess(ctx, token, liquid.ProjectUUID(token.Context.Request["project_uuid"]), "v2:project:rate_update")
	if err != nil {
		return none, err
	}

	// validate request contents
	serviceType := db.ServiceType(mux.Vars(r)["service_type"])
	rateName := liquid.RateName(mux.Vars(r)["rate_name"])
	req, err := parseRequestBodyAs[ratesv2.ProjectRatePatchRequest](r)
	if err != nil {
		return none, err
	}
	_, sExists := sis.GetServiceForType(serviceType)
	if !sExists {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchService)
	}
	rate, rExists := sis.GetRateForPath(db.RatePath{ServiceType: serviceType, RateName: rateName})
	if !rExists {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchRate)
	}
	if req.Limit != -1 && req.Window.IsNone() {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("window should be set when limit != -1"))
	}
	if req.Limit == -1 && req.Window.IsSome() {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("window should not be set when limit == -1"))
	}
	serviceConfig, cExists := p.Cluster.Config.GetLiquidConfigurationForType(serviceType)
	if !cExists {
		// defense in depth, should be caught above
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchService)
	}
	_, defaultExists := serviceConfig.RateLimits.GetProjectDefaultRateLimit(rateName).Unpack()
	if !defaultExists {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("for this rate, no custom rate limits can be set"))
	}
	if window, wExists := req.Window.Unpack(); wExists && window == 0 {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("a window of 0 does not make sense"))
	}

	// get projectRate
	// NOTE: we don't return when the projectRate does not exist!
	projectRate, err := db.ProjectRateStore.SelectOneWhere(ctx, p.DB, "project_id = $1 AND rate_id = $2", scope.Project.ID, rate.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// we ignore not-yet created projectRates here and instead insert them!
		return none, err
	}

	// modifications
	oldLimit := projectRate.Limit
	oldWindow := projectRate.Window
	projectRate.Limit = req.Limit.AsOption()
	projectRate.Window = req.Window

	// early return or insert/update
	if oldLimit == projectRate.Limit && oldWindow == projectRate.Window {
		return none, nil
	}
	if projectRate.ID == 0 {
		projectRate.ProjectID = scope.Project.ID
		projectRate.RateID = rate.ID
		err = db.ProjectRateStore.Insert(ctx, p.DB, &projectRate)
	} else {
		err = db.ProjectRateStore.Update(ctx, p.DB, projectRate)
	}
	if err != nil {
		return none, err
	}

	// audit log entry
	p.auditor.Record(audittools.Event{
		Time:       now,
		Request:    r,
		User:       token,
		ReasonCode: http.StatusNoContent,
		Action:     cadf.UpdateAction,
		Target: audit.V2ProjectRateEventTarget{
			DomainID:    scope.Domain.UUID,
			DomainName:  scope.Domain.Name,
			ProjectID:   scope.Project.UUID,
			ProjectName: scope.Project.Name,
			ServiceType: serviceType,
			RateName:    rateName,
			RequestedChange: audit.V2RateLimitChange{
				Unit:      rate.Unit,
				OldLimit:  oldLimit,
				NewLimit:  projectRate.Limit,
				OldWindow: oldWindow,
				NewWindow: projectRate.Window,
			},
		},
	})

	return none, nil
}
