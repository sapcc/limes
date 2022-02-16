/*******************************************************************************
*
* Copyright 2017-2020 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/reports"
)

//QuotaUpdater contains the shared code for domain and project PUT requests.
//See func PutDomain and func PutProject for how it's used.
type QuotaUpdater struct {
	//scope
	Cluster *core.Cluster
	Domain  *db.Domain  //always set (for project quota updates, contains the project's domain)
	Project *db.Project //nil for domain quota updates

	//AuthZ info
	CanRaise        func(serviceType string) bool
	CanRaiseLP      func(serviceType string) bool //low-privilege raise
	CanLower        func(serviceType string) bool
	CanSetRateLimit func(serviceType string) bool

	//Filled by ValidateInput() with the keys being the service type and the resource name.
	ResourceRequests map[string]map[string]QuotaRequest
	//Filled by ValidateInput() with the keys being the service type and the rate name.
	RateLimitRequests map[string]map[string]RateLimitRequest
}

//QuotaRequest describes a single quota value that a PUT request wants to
//change. It appears in type QuotaUpdater.
type QuotaRequest struct {
	OldValue        uint64
	NewValue        uint64
	Unit            limes.Unit
	NewUnit         limes.Unit
	ValidationError *core.QuotaValidationError
}

//RateLimitRequest describes a single rate limit that a PUT requests wants to
//change. It appears in type QuotaUpdater.
type RateLimitRequest struct {
	Unit            limes.Unit
	OldLimit        uint64
	NewLimit        uint64
	OldWindow       limes.Window
	NewWindow       limes.Window
	ValidationError *core.QuotaValidationError
}

//ScopeType is used for constructing error messages.
func (u QuotaUpdater) ScopeType() string {
	if u.Project == nil {
		return "domain"
	}
	return "project"
}

//ScopeName is "$DOMAIN_NAME" for domains and "$DOMAIN_NAME/$PROJECT_NAME" for projects.
func (u QuotaUpdater) ScopeName() string {
	if u.Project == nil {
		return u.Domain.Name
	}
	return u.Domain.Name + "/" + u.Project.Name
}

//QuotaConstraints returns the quota constraints that apply to this updater's scope.
func (u QuotaUpdater) QuotaConstraints() core.QuotaConstraints {
	if u.Cluster.QuotaConstraints == nil {
		return nil
	}
	if u.Project == nil {
		return u.Cluster.QuotaConstraints.Domains[u.Domain.Name]
	}
	return u.Cluster.QuotaConstraints.Projects[u.Domain.Name][u.Project.Name]
}

////////////////////////////////////////////////////////////////////////////////
// validation phase

//MissingProjectReportError is returned by QuotaUpdater.ValidateInput() when a
//project report is incomplete. This usually happens when a user tries to PUT a
//quota on a new project that has not been scraped yet.
type MissingProjectReportError struct {
	ServiceType  string
	ResourceName string
}

//Error implements the builtin/error interface.
func (e MissingProjectReportError) Error() string {
	return fmt.Sprintf("no project report for resource %s/%s", e.ServiceType, e.ResourceName)
}

//ValidateInput reads the given input and validates the quotas contained therein.
//Results are collected into u.Requests. The return value is only set for unexpected
//errors, not for validation errors.
func (u *QuotaUpdater) ValidateInput(input limes.QuotaRequest, dbi db.Interface) error {
	//gather reports on the cluster's capacity and domain's quotas to decide whether a quota update is legal
	clusterReport, err := reports.GetCluster(u.Cluster, dbi, reports.Filter{})
	if err != nil {
		return err
	}
	domainReport, err := GetDomainReport(u.Cluster, *u.Domain, dbi, reports.Filter{})
	if err != nil {
		return err
	}
	//for project scope, we also need a project report for validation
	var projectReport *limes.ProjectReport
	if u.Project != nil {
		projectReport, err = GetProjectReport(u.Cluster, *u.Domain, *u.Project, dbi, reports.Filter{WithRates: true})
		if err != nil {
			return err
		}
	}

	//go through all services and resources and validate the requested quotas
	u.ResourceRequests = make(map[string]map[string]QuotaRequest)
	for _, quotaPlugin := range u.Cluster.QuotaPlugins {
		srv := quotaPlugin.ServiceInfo()
		u.ResourceRequests[srv.Type] = map[string]QuotaRequest{}

		for _, res := range quotaPlugin.Resources() {
			//find the report data for this resource
			var (
				clusterRes *limes.ClusterResourceReport
				domRes     *limes.DomainResourceReport
				projRes    *limes.ProjectResourceReport
			)
			if clusterService, exists := clusterReport.Services[srv.Type]; exists {
				clusterRes = clusterService.Resources[res.Name]
			}
			if clusterRes == nil {
				return fmt.Errorf("no cluster report for resource %s/%s", srv.Type, res.Name)
			}
			if domainService, exists := domainReport.Services[srv.Type]; exists {
				domRes = domainService.Resources[res.Name]
			}
			if domRes == nil {
				return fmt.Errorf("no domain report for resource %s/%s", srv.Type, res.Name)
			}
			if u.Project == nil {
			} else {
				if projectService, exists := projectReport.Services[srv.Type]; exists {
					projRes = projectService.Resources[res.Name]
				}
				if projRes == nil {
					return MissingProjectReportError{
						ServiceType:  srv.Type,
						ResourceName: res.Name,
					}
				}
			}

			//skip resources where no new quota was requested
			newQuota, exists := input[srv.Type].Resources[res.Name]
			if !exists {
				continue
			}

			req := QuotaRequest{
				Unit: domRes.Unit,
			}
			var oldValueAsPtr *uint64
			if u.Project == nil {
				oldValueAsPtr = domRes.DomainQuota
			} else {
				oldValueAsPtr = projRes.Quota
			}
			if oldValueAsPtr == nil || domRes.NoQuota {
				req.ValidationError = &core.QuotaValidationError{
					Status:  http.StatusForbidden,
					Message: "resource does not track quota",
				}
			} else {
				req.OldValue = *oldValueAsPtr
			}

			//convert given value to correct unit
			if req.ValidationError == nil {
				req.NewValue, err = core.ConvertUnitFor(u.Cluster, srv.Type, res.Name, newQuota)
				if err != nil {
					req.ValidationError = &core.QuotaValidationError{
						Status:  http.StatusUnprocessableEntity,
						Message: err.Error(),
					}
				} else {
					//skip this resource entirely if no change is requested
					if req.OldValue == req.NewValue {
						continue //with next resource
					}
					//value is valid and novel -> perform further validation
					behavior := u.Cluster.BehaviorForResource(srv.Type, res.Name, u.ScopeName())
					req.ValidationError = u.validateQuota(srv, res, behavior, *clusterRes, *domRes, projRes, req.OldValue, req.NewValue)
				}
			}

			u.ResourceRequests[srv.Type][res.Name] = req
		}
	}

	//Rate limits are only available on project level.
	if u.Project != nil {
		u.RateLimitRequests = make(map[string]map[string]RateLimitRequest)

		//Go through all services and validate the requested rate limits.
		for svcType, in := range input {
			svcConfig, err := u.Cluster.Config.GetServiceConfigurationForType(svcType)
			if err != nil {
				//Skip service if not configured.
				continue
			}
			if _, ok := u.RateLimitRequests[svcType]; !ok {
				u.RateLimitRequests[svcType] = make(map[string]RateLimitRequest)
			}

			for rateName, newRateLimit := range in.Rates {
				req := RateLimitRequest{
					NewLimit:  newRateLimit.Limit,
					NewWindow: newRateLimit.Window,
				}

				//Allow only setting rate limits for which a default exists.
				defaultRateLimit, exists := svcConfig.RateLimits.GetProjectDefaultRateLimit(rateName)
				if exists {
					req.Unit = defaultRateLimit.Unit
				} else {
					req.ValidationError = &core.QuotaValidationError{
						Status:  http.StatusForbidden,
						Message: "user is not allowed to create new rate limits",
					}
					u.RateLimitRequests[svcType][rateName] = req
					continue
				}

				if projectService, exists := projectReport.Services[svcType]; exists {
					projectRate, exists := projectService.Rates[rateName]
					if exists && projectRate.Limit != 0 && projectRate.Window != nil {
						req.OldLimit = projectRate.Limit
						req.OldWindow = *projectRate.Window
					} else {
						req.OldLimit = defaultRateLimit.Limit
						req.OldWindow = defaultRateLimit.Window
					}
				}

				//skip if rate limit was not changed
				if req.OldLimit == req.NewLimit && req.OldWindow == req.NewWindow {
					continue
				}

				//value is valid and novel -> perform further validation
				req.ValidationError = u.validateRateLimit(u.Cluster.InfoForService(svcType))
				u.RateLimitRequests[svcType][rateName] = req
			}
		}
	}

	//check if the request contains any services/resources that are not known to us
	for srvType, srvInput := range input {
		isUnknownService := !u.Cluster.HasService(srvType)
		if isUnknownService {
			u.ResourceRequests[srvType] = make(map[string]QuotaRequest)
		}
		for resName := range srvInput.Resources {
			if !u.Cluster.HasResource(srvType, resName) {
				msg := "no such resource"
				if isUnknownService {
					msg = "no such service"
				}

				u.ResourceRequests[srvType][resName] = QuotaRequest{
					ValidationError: &core.QuotaValidationError{
						Status:  http.StatusUnprocessableEntity,
						Message: msg,
					},
				}
			}
		}
	}

	//perform project-specific checks via QuotaPlugin.IsQuotaAcceptableForProject
	if u.Project != nil {
		for srvType, srvInput := range input {
			//only check if there were no other validation errors
			hasAnyPreviousErrors := false
			for resName := range srvInput.Resources {
				if u.ResourceRequests[srvType][resName].ValidationError != nil {
					hasAnyPreviousErrors = true
					break
				}
			}
			if hasAnyPreviousErrors {
				continue
			}

			//collect the full set of quotas for this service as requested by the user
			quotaValues := make(map[string]uint64)
			if projectService, exists := projectReport.Services[srvType]; exists {
				for resName, res := range projectService.Resources {
					if !res.ExternallyManaged && !res.NoQuota && res.Quota != nil {
						quotaValues[resName] = *res.Quota
					}
				}
			}
			for resName := range srvInput.Resources {
				quotaValues[resName] = u.ResourceRequests[srvType][resName].NewValue
			}

			//perform validation
			if plugin, exists := u.Cluster.QuotaPlugins[srvType]; exists {
				provider, eo := u.Cluster.ProviderClient()
				domain := core.KeystoneDomainFromDB(*u.Domain)
				project := core.KeystoneProjectFromDB(*u.Project, domain)
				err := plugin.IsQuotaAcceptableForProject(provider, eo, project, quotaValues)
				if err != nil {
					for resName := range srvInput.Resources {
						u.ResourceRequests[srvType][resName] = QuotaRequest{
							ValidationError: &core.QuotaValidationError{
								Status:  http.StatusUnprocessableEntity,
								Message: "not acceptable for this project: " + err.Error(),
							},
						}
					}
				}
			}
		}
	}

	return nil
}

func (u QuotaUpdater) validateQuota(srv limes.ServiceInfo, res limes.ResourceInfo, behavior core.ResourceBehavior, clusterRes limes.ClusterResourceReport, domRes limes.DomainResourceReport, projRes *limes.ProjectResourceReport, oldQuota, newQuota uint64) *core.QuotaValidationError {
	//can we change this quota at all?
	if res.ExternallyManaged {
		return &core.QuotaValidationError{
			Status:  http.StatusUnprocessableEntity,
			Message: "resource is managed externally",
		}
	}

	//check quota constraints
	constraint := u.QuotaConstraints()[srv.Type][res.Name]
	verr := constraint.Validate(newQuota)
	if verr != nil {
		verr.Message += fmt.Sprintf(" for this %s and resource", u.ScopeType())
		return verr
	}
	if behavior.MinNonZeroProjectQuota > 0 && newQuota > 0 && behavior.MinNonZeroProjectQuota > newQuota {
		return &core.QuotaValidationError{
			Status:       http.StatusUnprocessableEntity,
			MinimumValue: &behavior.MinNonZeroProjectQuota,
			Unit:         res.Unit,
			Message: fmt.Sprintf("must allocate at least %s quota",
				limes.ValueWithUnit{Value: behavior.MinNonZeroProjectQuota, Unit: res.Unit},
			),
		}
	}

	//check authorization for quota change
	var lprLimit uint64
	if u.Project == nil {
		limitSpec := u.Cluster.LowPrivilegeRaise.LimitsForDomains[srv.Type][res.Name]
		lprLimit = limitSpec.Evaluate(clusterRes, oldQuota)
	} else {
		if u.Cluster.Config.LowPrivilegeRaise.IsAllowedForProjectsIn(u.Domain.Name) {
			limitSpec := u.Cluster.LowPrivilegeRaise.LimitsForProjects[srv.Type][res.Name]
			lprLimit = limitSpec.Evaluate(clusterRes, oldQuota)
		} else {
			lprLimit = 0
		}
	}
	verr = u.validateAuthorization(srv, oldQuota, newQuota, lprLimit, res.Unit)
	if verr != nil {
		verr.Message += fmt.Sprintf(" in this %s", u.ScopeType())
		return verr
	}

	//specific rules for domain quotas vs. project quotas
	if u.Project == nil {
		return u.validateDomainQuota(domRes, newQuota)
	}
	return u.validateProjectQuota(domRes, *projRes, newQuota)
}

func (u QuotaUpdater) validateRateLimit(srv limes.ServiceInfo) *core.QuotaValidationError {
	if u.CanSetRateLimit(srv.Type) {
		return nil
	}
	return &core.QuotaValidationError{
		Status:  http.StatusForbidden,
		Message: fmt.Sprintf("user is not allowed to set %q rate limits", srv.Type),
	}
}

func (u QuotaUpdater) validateAuthorization(srv limes.ServiceInfo, oldQuota, newQuota, lprLimit uint64, unit limes.Unit) *core.QuotaValidationError {
	if oldQuota >= newQuota {
		if u.CanLower(srv.Type) {
			return nil
		}
		return &core.QuotaValidationError{
			Status:  http.StatusForbidden,
			Message: fmt.Sprintf("user is not allowed to lower %q quotas", srv.Type),
		}
	}

	if u.CanRaise(srv.Type) {
		return nil
	}
	if u.CanRaiseLP(srv.Type) && lprLimit > 0 {
		if newQuota <= lprLimit {
			return nil
		}
		return &core.QuotaValidationError{
			Status:       http.StatusForbidden,
			Message:      fmt.Sprintf("user is not allowed to raise %q quotas that high", srv.Type),
			MaximumValue: &lprLimit,
			Unit:         unit,
		}
	}
	return &core.QuotaValidationError{
		Status:  http.StatusForbidden,
		Message: fmt.Sprintf("user is not allowed to raise %q quotas", srv.Type),
	}
}

func (u QuotaUpdater) validateDomainQuota(report limes.DomainResourceReport, newQuota uint64) *core.QuotaValidationError {
	if report.DomainQuota == nil || report.ProjectsQuota == nil {
		//defense in depth: we should have detected NoQuota resources a long time ago
		return &core.QuotaValidationError{
			Status:  http.StatusInternalServerError,
			Message: "missing input data for quota validation (please report this problem!)",
		}
	}

	//when reducing domain quota, existing project quotas must fit into new domain quota
	oldQuota := *report.DomainQuota
	if newQuota < oldQuota && newQuota < *report.ProjectsQuota {
		return &core.QuotaValidationError{
			Status:       http.StatusConflict,
			Message:      "domain quota may not be smaller than sum of project quotas in that domain",
			MinimumValue: report.ProjectsQuota,
			Unit:         report.Unit,
		}
	}

	return nil
}

func (u QuotaUpdater) validateProjectQuota(domRes limes.DomainResourceReport, projRes limes.ProjectResourceReport, newQuota uint64) *core.QuotaValidationError {
	if projRes.Quota == nil || domRes.ProjectsQuota == nil || domRes.DomainQuota == nil {
		//defense in depth: we should have detected NoQuota resources a long time ago
		return &core.QuotaValidationError{
			Status:  http.StatusInternalServerError,
			Message: "missing input data for quota validation (please report this problem!)",
		}
	}

	//when reducing project quota, existing usage must fit into new quotaj
	oldQuota := *projRes.Quota
	if newQuota < oldQuota && newQuota < projRes.Usage {
		min := projRes.Usage
		return &core.QuotaValidationError{
			Status:       http.StatusConflict,
			Message:      "quota may not be lower than current usage",
			MinimumValue: &min,
			Unit:         projRes.Unit,
		}
	}

	//check that domain quota is not exceeded
	//
	//NOTE: It looks like an arithmetic overflow (or rather, underflow) is
	//possible here, but it isn't. projectsQuota is the sum over all current
	//project quotas, including res.Quota, and thus is always bigger (since these
	//quotas are all unsigned). Also, we're doing everything in a transaction, so
	//an overflow because of concurrent quota changes is also out of the
	//question.
	newProjectsQuota := *domRes.ProjectsQuota - *projRes.Quota + newQuota
	if newProjectsQuota > *domRes.DomainQuota {
		maxQuota := *domRes.DomainQuota - (*domRes.ProjectsQuota - *projRes.Quota)
		if *domRes.DomainQuota < *domRes.ProjectsQuota-*projRes.Quota {
			maxQuota = 0
		}
		return &core.QuotaValidationError{
			Status:       http.StatusConflict,
			Message:      "domain quota exceeded",
			MaximumValue: &maxQuota,
			Unit:         domRes.Unit,
		}
	}

	return nil
}

//IsValid returns true if all u.Requests are valid (i.e. ValidationError == nil).
func (u QuotaUpdater) IsValid() bool {
	for _, reqs := range u.ResourceRequests {
		for _, req := range reqs {
			if req.ValidationError != nil {
				return false
			}
		}
	}
	for _, reqs := range u.RateLimitRequests {
		for _, req := range reqs {
			if req.ValidationError != nil {
				return false
			}
		}
	}
	return true
}

//WriteSimulationReport produces the HTTP response for the POST /simulate-put
//endpoints.
func (u QuotaUpdater) WriteSimulationReport(w http.ResponseWriter) {
	type (
		unacceptableResource struct {
			ServiceType  string `json:"service_type"`
			ResourceName string `json:"resource_name"`
			core.QuotaValidationError
		}

		unacceptableRateLimit struct {
			ServiceType string `json:"service_type"`
			Name        string `json:"name"`
			core.QuotaValidationError
		}
	)
	var result struct {
		IsValid                bool                    `json:"success"`
		UnacceptableResources  []unacceptableResource  `json:"unacceptable_resources,omitempty"`
		UnacceptableRateLimits []unacceptableRateLimit `json:"unacceptable_rates,omitempty"`
	}
	result.IsValid = true //until proven otherwise

	for srvType, reqs := range u.ResourceRequests {
		for resName, req := range reqs {
			if req.ValidationError != nil {
				result.IsValid = false
				result.UnacceptableResources = append(result.UnacceptableResources,
					unacceptableResource{
						ServiceType:          srvType,
						ResourceName:         resName,
						QuotaValidationError: *req.ValidationError,
					},
				)
			}
		}
	}

	for srvType, reqs := range u.RateLimitRequests {
		for rateName, req := range reqs {
			if req.ValidationError != nil {
				result.IsValid = false
				result.UnacceptableRateLimits = append(result.UnacceptableRateLimits,
					unacceptableRateLimit{
						ServiceType:          srvType,
						Name:                 rateName,
						QuotaValidationError: *req.ValidationError,
					},
				)
			}
		}
	}

	//deterministic ordering for unit tests
	sort.Slice(result.UnacceptableResources, func(i, j int) bool {
		srvType1 := result.UnacceptableResources[i].ServiceType
		srvType2 := result.UnacceptableResources[j].ServiceType
		if srvType1 != srvType2 {
			return srvType1 < srvType2
		}
		resName1 := result.UnacceptableResources[i].ResourceName
		resName2 := result.UnacceptableResources[j].ResourceName
		return resName1 < resName2
	})

	sort.Slice(result.UnacceptableRateLimits, func(i, j int) bool {
		srvType1 := result.UnacceptableRateLimits[i].ServiceType
		srvType2 := result.UnacceptableRateLimits[j].ServiceType
		if srvType1 != srvType2 {
			return srvType1 < srvType2
		}
		rateName1 := result.UnacceptableRateLimits[i].Name
		rateName2 := result.UnacceptableRateLimits[j].Name
		return rateName1 < rateName2
	})

	respondwith.JSON(w, http.StatusOK, result)
}

//WritePutErrorResponse produces a negative HTTP response for this PUT request.
//It may only be used when `u.IsValid()` is false.
func (u QuotaUpdater) WritePutErrorResponse(w http.ResponseWriter) {
	var lines []string
	hasSubstatus := make(map[int]bool)

	//collect error messages
	for srvType, reqs := range u.ResourceRequests {
		for resName, req := range reqs {
			err := req.ValidationError
			if err != nil {
				hasSubstatus[err.Status] = true
				line := fmt.Sprintf("cannot change %s/%s quota: %s",
					srvType, resName, err.Message)
				var notes []string
				if err.MinimumValue != nil {
					notes = append(notes, fmt.Sprintf("minimum acceptable %s quota is %v",
						u.ScopeType(), limes.ValueWithUnit{Value: *err.MinimumValue, Unit: err.Unit}))
				}
				if err.MaximumValue != nil {
					notes = append(notes, fmt.Sprintf("maximum acceptable %s quota is %v",
						u.ScopeType(), limes.ValueWithUnit{Value: *err.MaximumValue, Unit: err.Unit}))
				}
				if len(notes) > 0 {
					line += fmt.Sprintf(" (%s)", strings.Join(notes, ", "))
				}
				lines = append(lines, line)
			}
		}
	}
	for srvType, reqs := range u.RateLimitRequests {
		for rateName, req := range reqs {
			if err := req.ValidationError; err != nil {
				hasSubstatus[err.Status] = true
				lines = append(
					lines,
					fmt.Sprintf("cannot change %s/%s rate limits: %s", srvType, rateName, err.Message),
				)
			}
		}
	}
	sort.Strings(lines) //for determinism in unit test
	msg := strings.Join(lines, "\n")

	//when all errors have the same status, report that; otherwise use 422
	//(Unprocessable Entity) as a reasonable overall default
	status := http.StatusUnprocessableEntity
	if len(hasSubstatus) == 1 {
		for s := range hasSubstatus {
			status = s
		}
	}
	http.Error(w, msg, status)
}

////////////////////////////////////////////////////////////////////////////////
// integration with package audit

//CommitAuditTrail prepares an audit.Trail instance for this updater and
//commits it.
func (u QuotaUpdater) CommitAuditTrail(token *gopherpolicy.Token, r *http.Request, requestTime time.Time) {
	projectUUID := ""
	if u.Project != nil {
		projectUUID = u.Project.UUID
	}

	invalid := !u.IsValid()
	statusCode := http.StatusOK
	if invalid {
		statusCode = http.StatusUnprocessableEntity
	}

	for srvType, reqs := range u.ResourceRequests {
		for resName, req := range reqs {
			// low-privilege-raise metrics
			if u.CanRaiseLP(srvType) && !u.CanRaise(srvType) {
				labels := prometheus.Labels{
					"os_cluster": u.Cluster.ID,
					"service":    srvType,
					"resource":   resName,
				}
				if u.ScopeType() == "domain" {
					if invalid {
						lowPrivilegeRaiseDomainFailureCounter.With(labels).Inc()
					} else {
						lowPrivilegeRaiseDomainSuccessCounter.With(labels).Inc()
					}
				} else {
					if invalid {
						lowPrivilegeRaiseProjectFailureCounter.With(labels).Inc()
					} else {
						lowPrivilegeRaiseProjectSuccessCounter.With(labels).Inc()
					}
				}
			}

			//if !u.IsValid(), then all requested quotas in this PUT are considered
			//invalid (and none are committed), so set the rejectReason to explain this
			rejectReason := ""
			if invalid {
				if req.ValidationError == nil {
					rejectReason = "cannot commit this because other values in this request are unacceptable"
				} else {
					rejectReason = req.ValidationError.Message
				}
			}

			logAndPublishEvent(requestTime, r, token, statusCode,
				quotaEventTarget{
					DomainID:     u.Domain.UUID,
					ProjectID:    projectUUID, //is empty for domain quota updates, see above
					ServiceType:  srvType,
					ResourceName: resName,
					OldQuota:     req.OldValue,
					NewQuota:     req.NewValue,
					QuotaUnit:    req.Unit,
					RejectReason: rejectReason,
				})
		}
	}

	for srvType, reqs := range u.RateLimitRequests {
		for rateName, req := range reqs {
			//if !u.IsValid(), then all requested quotas in this PUT are considered
			//invalid (and none are committed), so set the rejectReason to explain this
			rejectReason := ""
			if invalid {
				if req.ValidationError == nil {
					rejectReason = "cannot commit this because other values in this request are unacceptable"
				} else {
					rejectReason = req.ValidationError.Message
				}
			}

			logAndPublishEvent(requestTime, r, token, statusCode,
				rateLimitEventTarget{
					DomainID:     u.Domain.UUID,
					ProjectID:    projectUUID,
					ServiceType:  srvType,
					Name:         rateName,
					OldLimit:     req.OldLimit,
					NewLimit:     req.NewLimit,
					OldWindow:    req.OldWindow,
					NewWindow:    req.NewWindow,
					Unit:         req.Unit,
					RejectReason: rejectReason,
				})
		}
	}
}
