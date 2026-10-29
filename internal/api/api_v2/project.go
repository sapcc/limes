// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-api-declarations/opts"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/must"
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
	return reports_v2.GetProjectResources(ctx, p.Cluster, token, filter, options, scope, p.timeNow())
}

// handleGetRatesProjects handles GET /rates/v2/projects.
func (p *v2Provider) handleGetRatesProjects(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/projects")
	scope, err := p.checkAuthZForReportWithMultipleProjects(r, token)
	if err != nil {
		return
	}
	return p.commonHandleGetRatesProject(r, token, scope)
}

// handleGetRatesProject handles GET /rates/v2/projects/:project_uuid.
func (p *v2Provider) handleGetRatesProject(r *http.Request, token *gopherpolicy.Token) (_ ratesv2.ProjectGetResponse, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/projects/:project_uuid")
	scope, err := p.checkAuthZForReportWithSingleProject(r.Context(), token)
	if err != nil {
		return
	}
	return p.commonHandleGetRatesProject(r, token, scope)
}

// commonHandleGetRatesProject handles single- and multi-project rate calls.
func (p *v2Provider) commonHandleGetRatesProject(r *http.Request, token *gopherpolicy.Token, scope reports_v2.Scope) (_ ratesv2.ProjectGetResponse, err error) {
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
	result, err := reports_v2.GetProjectRates(ctx, p.Cluster, token, filter, options, scope)
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

// (p *v2Provider) handlePutMaxQuota handles PUT /resources/v2/projects/:project_uuid/max-quota.
func (p *v2Provider) handlePutMaxQuota(r *http.Request, token *gopherpolicy.Token) (none any, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/projects/:project_uuid/max-quota")
	var (
		ctx = r.Context()
		sis = p.Cluster.SIC.GetSnapshot()
		now = p.timeNow()
	)

	scope, err := p.checkProjectAccess(ctx, token, liquid.ProjectUUID(token.Context.Request["project_uuid"]), "v2:project:max_quota_update")
	if err != nil {
		return none, err
	}

	// validate request contents
	req, err := parseRequestBodyAs[resourcesv2.ProjectResourceMaxQuotaRequest](r)
	if err != nil {
		return none, err
	}
	_, sExists := sis.GetServiceForType(req.ServiceType)
	if !sExists {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchService)
	}
	res, rExists := sis.GetResourceForPath(db.ResourcePath{ServiceType: req.ServiceType, ResourceName: req.ResourceName})
	if !rExists {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchResource)
	}
	if !res.HasQuota {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errDoesNotTrackQuota)
	}
	if req.MaxQuota.IsSome() != req.MaxQuotaUnit.IsSome() {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("unit should be set when max_quota is set"))
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

	// modifications
	oldValue := projectResource.MaxQuotaFromOutsideAdmin
	if maxQuota, ok := req.MaxQuota.Unpack(); ok {
		unit := must.BeOK(req.MaxQuotaUnit.Unpack()) // already checked above
		converted, err := limes.ValueWithUnit{
			Unit:  unit,
			Value: maxQuota,
		}.ConvertTo(res.Unit)
		if err != nil {
			return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("given unit was not convertible to the resource's unit"))
		}
		projectResource.MaxQuotaFromOutsideAdmin = Some(converted.Value)
	} else {
		projectResource.MaxQuotaFromOutsideAdmin = None[uint64]()
	}

	// early return or update
	if oldValue == projectResource.MaxQuotaFromOutsideAdmin {
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
		Target: audit.V2MaxQuotaEventTarget{
			DomainID:     scope.Domain.UUID,
			DomainName:   scope.Domain.Name,
			ProjectID:    scope.Project.UUID,
			ProjectName:  scope.Project.Name,
			ServiceType:  req.ServiceType,
			ResourceName: req.ResourceName,
			RequestedChange: audit.MaxQuotaChange{
				OldValue: oldValue,
				NewValue: projectResource.MaxQuotaFromOutsideAdmin,
			},
		},
	})

	return none, nil
}

// (p *v2Provider) handlePutForbidAutogrowth handles PUT /resources/v2/projects/:project_uuid/forbid-autogrowth.
func (p *v2Provider) handlePutForbidAutogrowth(r *http.Request, token *gopherpolicy.Token) (none any, err error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/projects/:project_uuid/forbid-autogrowth")
	var (
		ctx = r.Context()
		sis = p.Cluster.SIC.GetSnapshot()
		now = p.timeNow()
	)

	scope, err := p.checkProjectAccess(ctx, token, liquid.ProjectUUID(token.Context.Request["project_uuid"]), "v2:project:forbid_autogrowth_update")
	if err != nil {
		return none, err
	}

	// validate request contents
	req, err := parseRequestBodyAs[resourcesv2.ProjectResourceForbidAutogrowthRequest](r)
	if err != nil {
		return none, err
	}
	_, sExists := sis.GetServiceForType(req.ServiceType)
	if !sExists {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchService)
	}
	res, rExists := sis.GetResourceForPath(db.ResourcePath{ServiceType: req.ServiceType, ResourceName: req.ResourceName})
	if !rExists {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchResource)
	}
	if !res.HasQuota {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errDoesNotTrackQuota)
	}
	behavior := p.Cluster.CommitmentBehaviorForResourcePath(res.Path).ForDomain(scope.Domain.Name)
	if len(behavior.Durations) == 0 {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errDoesNotHaveCommitments)
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

	// modifications
	oldValue := projectResource.ForbidAutogrowth
	projectResource.ForbidAutogrowth = req.ForbidAutogrowth

	// early return or update
	if oldValue == projectResource.ForbidAutogrowth {
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
		Target: audit.V2AutogrowthEventTarget{
			DomainID:     scope.Domain.UUID,
			DomainName:   scope.Domain.Name,
			ProjectID:    scope.Project.UUID,
			ProjectName:  scope.Project.Name,
			ServiceType:  req.ServiceType,
			ResourceName: req.ResourceName,
			AutogrowthChange: audit.AutogrowthChange{
				ForbidAutogrowth: projectResource.ForbidAutogrowth,
			},
		},
	})

	return none, nil
}

// (p *v2Provider) handlePutRateLimitProject handles PUT /rates/v2/projects/:project_uuid/limit.
func (p *v2Provider) handlePutRateLimitProject(r *http.Request, token *gopherpolicy.Token) (none any, err error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/projects/:project_uuid/limit")
	var (
		ctx = r.Context()
		sis = p.Cluster.SIC.GetSnapshot()
		now = p.timeNow()
	)

	scope, err := p.checkProjectAccess(ctx, token, liquid.ProjectUUID(token.Context.Request["project_uuid"]), "v2:project:rate_limit_update")
	if err != nil {
		return none, err
	}

	// validate request contents
	req, err := parseRequestBodyAs[ratesv2.ProjectRateLimitRequest](r)
	if err != nil {
		return none, err
	}
	_, sExists := sis.GetServiceForType(req.ServiceType)
	if !sExists {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchService)
	}
	rate, rExists := sis.GetRateForPath(db.RatePath{ServiceType: req.ServiceType, RateName: req.RateName})
	if !rExists {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchRate)
	}
	if req.Limit.IsSome() != req.Unit.IsSome() {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("unit should be set when limit is set"))
	}
	if req.Limit.IsSome() != req.Window.IsSome() {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("window should be set when limit is set"))
	}
	serviceConfig, cExists := p.Cluster.Config.GetLiquidConfigurationForType(req.ServiceType)
	if !cExists {
		// defense in depth, should be caught above
		return none, respondwith.CustomStatus(http.StatusBadRequest, errNoSuchService)
	}
	_, defaultExists := serviceConfig.RateLimits.GetProjectDefaultRateLimit(req.RateName).Unpack()
	if !defaultExists {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("for this rate, no custom rate limits can be set"))
	}
	if window, wExists := req.Window.Unpack(); wExists && window == 0 {
		return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("a window of 0 does not make sense"))
	}

	// get projectRate
	projectRate, err := db.ProjectRateStore.SelectOneWhere(ctx, p.DB, "project_id = $1 AND rate_id = $2", scope.Project.ID, rate.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// we ignore not-yet created projectRates here and instead insert them!
		return none, err
	}

	// modifications
	oldLimit := projectRate.Limit
	oldWindow := projectRate.Window
	if limit, ok := req.Limit.Unpack(); ok {
		unit := must.BeOK(req.Unit.Unpack())                      // already checked above
		projectRate.Window = Some(must.BeOK(req.Window.Unpack())) // already checked above
		converted, err := limes.ValueWithUnit{
			Unit:  unit,
			Value: limit,
		}.ConvertTo(rate.Unit)
		if err != nil {
			return none, respondwith.CustomStatus(http.StatusBadRequest, errors.New("given unit was not convertible to the rate's unit"))
		}
		projectRate.Limit = Some(converted.Value)
	} else {
		projectRate.Limit = None[uint64]()
		projectRate.Window = None[limesrates.Window]()
	}

	// early return or update
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
		Target: audit.V2RateLimitEventTarget{
			DomainID:    scope.Domain.UUID,
			DomainName:  scope.Domain.Name,
			ProjectID:   scope.Project.UUID,
			ProjectName: scope.Project.Name,
			ServiceType: req.ServiceType,
			RateName:    req.RateName,
			Payload: audit.V2RateLimitChange{
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
