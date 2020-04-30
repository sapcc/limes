/*******************************************************************************
*
* Copyright 2017-2018 SAP SE
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
	Config  core.Configuration
	Cluster *core.Cluster
	Domain  *db.Domain  //always set (for project quota updates, contains the project's domain)
	Project *db.Project //nil for domain quota updates

	//AuthZ info
	CanRaise        bool
	CanRaiseLP      bool //low-privilege raise
	CanLower        bool
	CanSetRateLimit bool

	//Filled by ValidateInput() with the key being the service type.
	ResourceRequests  map[string]ResourceRequests
	RateLimitRequests map[string]RateLimitRequests
}

//ResourceRequests with the key being the resource name.
type ResourceRequests map[string]QuotaRequest

//RateLimitRequests with the keys being the target type URI and action name.
type RateLimitRequests map[string]map[string]QuotaRequest

//QuotaRequest describes a single quota value that a PUT request wants to
//change. It appears in type QuotaUpdater.
type QuotaRequest struct {
	OldValue        uint64
	NewValue        uint64
	Unit            limes.Unit
	NewUnit         limes.Unit
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

var getCapacityQuery = `
	SELECT capacity FROM cluster_resources WHERE service_id = (
		SELECT id FROM cluster_services WHERE cluster_id = $1 AND type = $2
	) AND name = $3
`

//ValidateInput reads the given input and validates the quotas contained therein.
//Results are collected into u.Requests. The return value is only set for unexpected
//errors, not for validation errors.
func (u *QuotaUpdater) ValidateInput(input limes.QuotaRequest, dbi db.Interface) error {
	//gather reports on the cluster's capacity and domain's quotas to decide whether a quota update is legal
	clusterReport, err := GetClusterReport(u.Config, u.Cluster, dbi, reports.Filter{})
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
	u.ResourceRequests = make(map[string]ResourceRequests)
	for _, quotaPlugin := range u.Cluster.QuotaPlugins {
		srv := quotaPlugin.ServiceInfo()
		u.ResourceRequests[srv.Type] = ResourceRequests{}

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

			req := QuotaRequest{
				OldValue: domRes.DomainQuota,
				Unit:     domRes.Unit,
			}
			if u.Project != nil {
				req.OldValue = projRes.Quota
			}

			//convert given value to correct unit
			newQuota, exists := input[srv.Type].Resources[res.Name]
			if !exists {
				continue
			}
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
				req.ValidationError = u.validateQuota(srv, res, behavior, *clusterRes, *domRes, projRes, req.NewValue)
			}

			u.ResourceRequests[srv.Type][res.Name] = req
		}
	}

	//Rate limits are only available on project level.
	if u.Project != nil {
		u.RateLimitRequests = make(map[string]RateLimitRequests)

		//Go through all services and validate the requested rate limits.
		for svcType, in := range input {
			svcConfig, err := u.Cluster.Config.GetServiceConfigurationForType(svcType)
			if err != nil {
				//Skip service if not configured.
				continue
			}
			if _, ok := u.RateLimitRequests[svcType]; !ok {
				u.RateLimitRequests[svcType] = make(RateLimitRequests)
			}

			for targetTypeURI, requests := range in.Rates {
				for action, newRateLimit := range requests {
					if _, ok := u.RateLimitRequests[svcType][targetTypeURI]; !ok {
						u.RateLimitRequests[svcType][targetTypeURI] = make(map[string]QuotaRequest)
					}

					req := QuotaRequest{
						NewValue: newRateLimit.Value,
						NewUnit:  newRateLimit.Unit,
					}

					//Allow only setting rate limits for which a default exists.
					defaultLimit, defaultUnit, err := svcConfig.Rates.GetProjectDefaultRateLimit(targetTypeURI, action)
					if err != nil {
						req.ValidationError = &core.QuotaValidationError{
							Status:  http.StatusForbidden,
							Message: "user is not allowed to create new rate limits",
						}
						u.RateLimitRequests[svcType][targetTypeURI][action] = req
						continue
					}

					var rlActRep *limes.ProjectRateLimitActionReport
					if projectService, exists := projectReport.Services[svcType]; exists {
						projectRateLimit, exists := projectService.Rates[targetTypeURI]
						if !exists {
							projectRateLimit = &limes.ProjectRateLimitReport{Actions: make(limes.ProjectRateLimitActionReports)}
						}

						actionRateLimit, exists := projectRateLimit.Actions[action]
						if !exists {
							actionRateLimit = &limes.ProjectRateLimitActionReport{
								Limit: defaultLimit,
								Unit:  limes.Unit(defaultUnit),
							}
						}
						rlActRep = actionRateLimit
					}

					req.OldValue = rlActRep.Limit
					req.Unit = rlActRep.Unit

					//Skip if rate limit value and unit were not changed.
					if req.OldValue == newRateLimit.Value && req.Unit == newRateLimit.Unit {
						continue
					}

					//Add to the list of rate limit requests as the value and/or unit changed.
					req.ValidationError = u.validateRateLimit(u.Cluster.InfoForService(svcType), targetTypeURI, rlActRep, req.NewValue, req.NewUnit)
					u.RateLimitRequests[svcType][targetTypeURI][action] = req
				}
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

	return nil
}

func (u QuotaUpdater) validateQuota(srv limes.ServiceInfo, res limes.ResourceInfo, behavior core.ResourceBehavior, clusterRes limes.ClusterResourceReport, domRes limes.DomainResourceReport, projRes *limes.ProjectResourceReport, newQuota uint64) *core.QuotaValidationError {
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
	var (
		oldQuota uint64
		lprLimit uint64
	)
	if u.Project == nil {
		oldQuota = domRes.DomainQuota
		limitSpec := u.Cluster.LowPrivilegeRaise.LimitsForDomains[srv.Type][res.Name]
		lprLimit = limitSpec.Evaluate(clusterRes, oldQuota)
	} else {
		oldQuota = projRes.Quota
		if u.Cluster.Config.LowPrivilegeRaise.IsAllowedForProjectsIn(u.Domain.Name) {
			limitSpec := u.Cluster.LowPrivilegeRaise.LimitsForProjects[srv.Type][res.Name]
			lprLimit = limitSpec.Evaluate(clusterRes, oldQuota)
		} else {
			lprLimit = 0
		}
	}
	verr = u.validateAuthorization(oldQuota, newQuota, lprLimit, res.Unit)
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

func (u QuotaUpdater) validateRateLimit(srv limes.ServiceInfo, targetTypeURI string, rateLimit *limes.ProjectRateLimitActionReport, newRateLimitValue uint64, newRateLimitUnit limes.Unit) *core.QuotaValidationError {
	return u.validateAuthorizationRateLimit(rateLimit.Limit, newRateLimitValue, rateLimit.Unit, newRateLimitUnit)
}

func (u QuotaUpdater) validateAuthorization(oldQuota, newQuota, lprLimit uint64, unit limes.Unit) *core.QuotaValidationError {
	if oldQuota >= newQuota {
		if u.CanLower {
			return nil
		}
		return &core.QuotaValidationError{
			Status:  http.StatusForbidden,
			Message: "user is not allowed to lower quotas",
		}
	}

	if u.CanRaise {
		return nil
	}
	if u.CanRaiseLP && lprLimit > 0 {
		if newQuota <= lprLimit {
			return nil
		}
		return &core.QuotaValidationError{
			Status:       http.StatusForbidden,
			Message:      "user is not allowed to raise quotas that high",
			MaximumValue: &lprLimit,
			Unit:         unit,
		}
	}
	return &core.QuotaValidationError{
		Status:  http.StatusForbidden,
		Message: "user is not allowed to raise quotas",
	}
}

func (u QuotaUpdater) validateAuthorizationRateLimit(oldLimit, newLimit uint64, oldUnit, newUnit limes.Unit) *core.QuotaValidationError {
	if u.CanSetRateLimit {
		return nil
	}
	return &core.QuotaValidationError{
		Status:  http.StatusForbidden,
		Message: "user is not allowed to set rate limits",
	}
}

func (u QuotaUpdater) validateDomainQuota(report limes.DomainResourceReport, newQuota uint64) *core.QuotaValidationError {
	//when reducing domain quota, existing project quotas must fit into new domain quota
	oldQuota := report.DomainQuota
	if newQuota < oldQuota && newQuota < report.ProjectsQuota {
		min := report.ProjectsQuota
		return &core.QuotaValidationError{
			Status:       http.StatusConflict,
			Message:      "domain quota may not be smaller than sum of project quotas in that domain",
			MinimumValue: &min,
			Unit:         report.Unit,
		}
	}

	return nil
}

func (u QuotaUpdater) validateProjectQuota(domRes limes.DomainResourceReport, projRes limes.ProjectResourceReport, newQuota uint64) *core.QuotaValidationError {
	//when reducing project quota, existing usage must fit into new quotaj
	oldQuota := projRes.Quota
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
	newProjectsQuota := domRes.ProjectsQuota - projRes.Quota + newQuota
	if newProjectsQuota > domRes.DomainQuota {
		maxQuota := domRes.DomainQuota - (domRes.ProjectsQuota - projRes.Quota)
		if domRes.DomainQuota < domRes.ProjectsQuota-projRes.Quota {
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
	for _, rlRequests := range u.RateLimitRequests {
		for _, actions := range rlRequests {
			for _, req := range actions {
				if req.ValidationError != nil {
					return false
				}
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
			ServiceType   string `json:"service_type"`
			TargetTypeURI string `json:"target_type_uri"`
			Action        string `json:"action"`
			core.QuotaValidationError
		}
	)
	var result struct {
		IsValid                bool                    `json:"success,keepempty"`
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

	for srvType, rateLimits := range u.RateLimitRequests {
		for targetTypeURI, requests := range rateLimits {
			for action, req := range requests {
				if req.ValidationError != nil {
					result.IsValid = false
					result.UnacceptableRateLimits = append(result.UnacceptableRateLimits,
						unacceptableRateLimit{
							ServiceType:          srvType,
							TargetTypeURI:        targetTypeURI,
							Action:               action,
							QuotaValidationError: *req.ValidationError,
						},
					)
				}
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

		ttu1 := result.UnacceptableRateLimits[i].TargetTypeURI
		ttu2 := result.UnacceptableRateLimits[j].TargetTypeURI
		if ttu1 != ttu2 {
			return ttu1 < ttu2
		}

		action1 := result.UnacceptableRateLimits[i].Action
		action2 := result.UnacceptableRateLimits[j].Action
		return action1 < action2
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
	for srvType, rateLimits := range u.RateLimitRequests {
		for targetTypeURI, requests := range rateLimits {
			for action, req := range requests {
				if err := req.ValidationError; err != nil {
					hasSubstatus[err.Status] = true
					lines = append(
						lines,
						fmt.Sprintf("cannot change %s/%s/%s rate limits: %s", srvType, targetTypeURI, action, err.Message),
					)
				}
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
			if u.CanRaiseLP && !u.CanRaise {
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

			logAndPublishEvent(u.Cluster.ID, requestTime, r, token, statusCode,
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

	for srvType, rateLimits := range u.RateLimitRequests {
		for targetTypeURI, requests := range rateLimits {
			for action, req := range requests {
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

				logAndPublishEvent(u.Cluster.ID, requestTime, r, token, statusCode,
					rateLimitEventTarget{
						DomainID:      u.Domain.UUID,
						ProjectID:     projectUUID,
						ServiceType:   srvType,
						TargetTypeURI: targetTypeURI,
						Action:        action,
						OldLimit:      req.OldValue,
						NewLimit:      req.NewValue,
						OldUnit:       req.Unit,
						NewUnit:       req.NewUnit,
						RejectReason:  rejectReason,
					})
			}
		}
	}
}
