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
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/reports"
)

// QuotaUpdater contains the shared code for domain and project PUT requests.
// See func PutDomain and func PutProject for how it's used.
type QuotaUpdater struct {
	//scope
	Cluster *core.Cluster
	Domain  *db.Domain  //always set (for project quota updates, contains the project's domain)
	Project *db.Project //nil for domain quota updates

	//context
	Now time.Time

	//AuthZ info
	CanRaise   func(serviceType, resourceName string) bool
	CanRaiseLP func(serviceType, resourceName string) bool //low-privilege raise
	CanLower   func(serviceType, resourceName string) bool
	CanLowerLP func(serviceType, resourceName string) bool

	//Filled by ValidateInput() with the keys being the service type and the resource name.
	Requests map[string]map[string]QuotaRequest
}

// QuotaRequest describes a single quota value that a PUT request wants to
// change. It appears in type QuotaUpdater.
type QuotaRequest struct {
	OldValue        uint64
	NewValue        uint64
	Unit            limes.Unit
	NewUnit         limes.Unit
	ValidationError *core.QuotaValidationError
}

// ScopeType is used for constructing error messages.
func (u QuotaUpdater) ScopeType() string {
	if u.Project == nil {
		return "domain"
	}
	return "project"
}

// ScopeName is "$DOMAIN_NAME" for domains and "$DOMAIN_NAME/$PROJECT_NAME" for projects.
func (u QuotaUpdater) ScopeName() string {
	if u.Project == nil {
		return u.Domain.Name
	}
	return u.Domain.Name + "/" + u.Project.Name
}

// QuotaConstraints returns the quota constraints that apply to this updater's scope.
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

// MissingProjectReportError is returned by QuotaUpdater.ValidateInput() when a
// project report is incomplete. This usually happens when a user tries to PUT a
// quota on a new project that has not been scraped yet.
type MissingProjectReportError struct {
	ServiceType  string
	ResourceName string
}

// Error implements the builtin/error interface.
func (e MissingProjectReportError) Error() string {
	return fmt.Sprintf("no project report for resource %s/%s", e.ServiceType, e.ResourceName)
}

// ValidateInput reads the given input and validates the quotas contained therein.
// Results are collected into u.Requests. The return value is only set for unexpected
// errors, not for validation errors.
func (u *QuotaUpdater) ValidateInput(input limesresources.QuotaRequest, dbi db.Interface) error {
	//gather reports on the cluster's capacity and domain's quotas to decide whether a quota update is legal
	clusterReport, err := reports.GetClusterResources(u.Cluster, dbi, reports.Filter{})
	if err != nil {
		return err
	}
	domainReport, err := GetDomainReport(u.Cluster, *u.Domain, dbi, reports.Filter{})
	if err != nil {
		return err
	}
	//for project scope, we also need a project report for validation
	var projectReport *limesresources.ProjectReport
	if u.Project != nil {
		projectReport, err = GetProjectResourceReport(u.Cluster, *u.Domain, *u.Project, u.Now, dbi, reports.Filter{})
		if err != nil {
			return err
		}
	}

	//go through all services and resources and validate the requested quotas
	u.Requests = make(map[string]map[string]QuotaRequest)
	for serviceType, quotaPlugin := range u.Cluster.QuotaPlugins {
		srv := quotaPlugin.ServiceInfo(serviceType)
		u.Requests[srv.Type] = map[string]QuotaRequest{}

		for _, res := range quotaPlugin.Resources() {
			//find the report data for this resource
			var (
				clusterRes *limesresources.ClusterResourceReport
				domRes     *limesresources.DomainResourceReport
				projRes    *limesresources.ProjectResourceReport
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
			newQuota, exists := input[srv.Type][res.Name]
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
				req.NewValue, err = core.ConvertUnitFor(u.Cluster, srv.Type, res.Name, limes.ValueWithUnit(newQuota))
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

			u.Requests[srv.Type][res.Name] = req
		}
	}

	//check if the request contains any services/resources that are not known to us
	for srvType, srvInput := range input {
		isUnknownService := !u.Cluster.HasService(srvType)
		if isUnknownService {
			u.Requests[srvType] = make(map[string]QuotaRequest)
		}
		for resName := range srvInput {
			if !u.Cluster.HasResource(srvType, resName) {
				msg := "no such resource"
				if isUnknownService {
					msg = "no such service"
				}

				u.Requests[srvType][resName] = QuotaRequest{
					ValidationError: &core.QuotaValidationError{
						Status:  http.StatusUnprocessableEntity,
						Message: msg,
					},
				}
			}
		}
	}

	if u.Project != nil {
		//collect the full set of quotas as requested by the user
		quotaValues := make(map[string]map[string]uint64)
		for srvType := range u.Cluster.QuotaPlugins {
			quotaValues[srvType] = make(map[string]uint64)

			if projectService, exists := projectReport.Services[srvType]; exists {
				for resName, res := range projectService.Resources {
					if !res.NoQuota && res.Quota != nil {
						quotaValues[srvType][resName] = *res.Quota
					}
				}
			}
			for resName, resReq := range u.Requests[srvType] {
				quotaValues[srvType][resName] = resReq.NewValue
			}
		}

		//perform project-specific checks via QuotaPlugin.IsQuotaAcceptableForProject()
		for srvType, srvInput := range input {
			//only check if there were no other validation errors
			hasAnyPreviousErrors := false
			for resName := range srvInput {
				if u.Requests[srvType][resName].ValidationError != nil {
					hasAnyPreviousErrors = true
					break
				}
			}
			if hasAnyPreviousErrors {
				continue
			}

			//perform validation
			if plugin, exists := u.Cluster.QuotaPlugins[srvType]; exists {
				domain := core.KeystoneDomainFromDB(*u.Domain)
				project := core.KeystoneProjectFromDB(*u.Project, domain)
				err := plugin.IsQuotaAcceptableForProject(project, quotaValues, u.Cluster.AllServiceInfos())
				if err != nil {
					for resName := range srvInput {
						u.Requests[srvType][resName] = QuotaRequest{
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

func (u QuotaUpdater) validateQuota(srv limes.ServiceInfo, res limesresources.ResourceInfo, behavior core.ResourceBehavior, clusterRes limesresources.ClusterResourceReport, domRes limesresources.DomainResourceReport, projRes *limesresources.ProjectResourceReport, oldQuota, newQuota uint64) *core.QuotaValidationError {
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
		lprLimit        uint64
		lprIsReversible bool
	)
	if u.Project == nil {
		limitSpec := u.Cluster.LowPrivilegeRaise.LimitsForDomains[srv.Type][res.Name]
		lprLimit = limitSpec.Evaluate(clusterRes, oldQuota)
		lprIsReversible = limitSpec.IsReversible()
	} else {
		if u.Cluster.Config.LowPrivilegeRaise.IsAllowedForProjectsIn(u.Domain.Name) {
			limitSpec := u.Cluster.LowPrivilegeRaise.LimitsForProjects[srv.Type][res.Name]
			lprLimit = limitSpec.Evaluate(clusterRes, oldQuota)
			lprIsReversible = limitSpec.IsReversible()
		} else {
			lprLimit = 0
			lprIsReversible = false
		}
	}
	verr = u.validateAuthorization(srv, res, oldQuota, newQuota, lprLimit, lprIsReversible, res.Unit)
	if verr != nil {
		if verr.Status == http.StatusForbidden {
			verr.Message += fmt.Sprintf(" in this %s", u.ScopeType())
		}
		return verr
	}

	//specific rules for domain quotas vs. project quotas
	if u.Project == nil {
		return u.validateDomainQuota(srv, res, clusterRes, domRes, newQuota)
	}
	return u.validateProjectQuota(domRes, *projRes, newQuota)
}

func (u QuotaUpdater) validateAuthorization(srv limes.ServiceInfo, res limesresources.ResourceInfo, oldQuota, newQuota, lprLimit uint64, lprIsReversible bool, unit limes.Unit) *core.QuotaValidationError {
	qdConfig := u.Cluster.QuotaDistributionConfigForResource(srv.Type, res.Name)

	if oldQuota >= newQuota {
		switch qdConfig.Model {
		case limesresources.HierarchicalQuotaDistribution:
			if u.CanLower(srv.Type, res.Name) {
				return nil
			}
			//If the low-privilege raise (LPR) limit is specified in a reversible
			//way, it also applies in reverse. If a raise is allowed by the LPR
			//limit, the reverse lower is also allowed by it.
			if u.CanLowerLP(srv.Type, res.Name) && lprLimit > 0 && lprIsReversible {
				if oldQuota <= lprLimit {
					return nil
				}
				return &core.QuotaValidationError{
					Status:  http.StatusForbidden,
					Message: fmt.Sprintf("user is not allowed to lower %q quotas that high", srv.Type),
				}
			}
		}
		return &core.QuotaValidationError{
			Status:  http.StatusForbidden,
			Message: fmt.Sprintf("user is not allowed to lower %q quotas", srv.Type),
		}
	}

	switch qdConfig.Model {
	case limesresources.HierarchicalQuotaDistribution:
		if u.CanRaise(srv.Type, res.Name) {
			return nil
		}
		if u.CanRaiseLP(srv.Type, res.Name) && lprLimit > 0 {
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
	}
	return &core.QuotaValidationError{
		Status:  http.StatusForbidden,
		Message: fmt.Sprintf("user is not allowed to raise %q quotas", srv.Type),
	}
}

func (u QuotaUpdater) validateDomainQuota(srv limes.ServiceInfo, res limesresources.ResourceInfo, clusterRes limesresources.ClusterResourceReport, domRes limesresources.DomainResourceReport, newQuota uint64) *core.QuotaValidationError {
	if domRes.DomainQuota == nil || domRes.ProjectsQuota == nil {
		//defense in depth: we should have detected NoQuota resources a long time ago
		return &core.QuotaValidationError{
			Status:  http.StatusInternalServerError,
			Message: "missing input data for quota validation (please report this problem!)",
		}
	}

	//when reducing domain quota, existing project quotas must fit into new domain quota
	oldQuota := *domRes.DomainQuota
	if newQuota < oldQuota && newQuota < *domRes.ProjectsQuota {
		return &core.QuotaValidationError{
			Status:       http.StatusConflict,
			Message:      "domain quota may not be smaller than sum of project quotas in that domain",
			MinimumValue: domRes.ProjectsQuota,
			Unit:         domRes.Unit,
		}
	}

	//when increasing domain quota, check that cluster capacity is not exceeded (if this constraint has been enabled in the config)
	qdConfig := u.Cluster.QuotaDistributionConfigForResource(srv.Type, res.Name)
	if newQuota > oldQuota && qdConfig.StrictDomainQuotaLimit {
		//NOTE: No arithmetic overflow/underflow is possible here, for the same
		//reasons as explained below in validateProjectQuota().
		newDomainsQuota := *clusterRes.DomainsQuota - oldQuota + newQuota
		if newDomainsQuota > *clusterRes.Capacity {
			maxQuota := *clusterRes.Capacity - (*clusterRes.DomainsQuota - oldQuota)
			if *clusterRes.Capacity < *clusterRes.DomainsQuota-oldQuota {
				maxQuota = 0
			}
			return &core.QuotaValidationError{
				Status:       http.StatusConflict,
				Message:      "cluster capacity may not be exceeded for this resource",
				MaximumValue: &maxQuota,
				Unit:         domRes.Unit,
			}
		}
	}
	return nil
}

func (u QuotaUpdater) validateProjectQuota(domRes limesresources.DomainResourceReport, projRes limesresources.ProjectResourceReport, newQuota uint64) *core.QuotaValidationError {
	if projRes.Quota == nil || domRes.ProjectsQuota == nil || domRes.DomainQuota == nil {
		//defense in depth: we should have detected NoQuota resources a long time ago
		return &core.QuotaValidationError{
			Status:  http.StatusInternalServerError,
			Message: "missing input data for quota validation (please report this problem!)",
		}
	}

	//when reducing project quota, existing usage must fit into new quota
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
	//NOTE: This check is skipped on non-hierarchical quota distribution since domain
	//quota is always set equal to the sum of project quotas afterwards there.
	if projRes.QuotaDistributionModel == limesresources.HierarchicalQuotaDistribution {
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
	}

	return nil
}

// IsValid returns true if all u.Requests are valid (i.e. ValidationError == nil).
func (u QuotaUpdater) IsValid() bool {
	for _, reqs := range u.Requests {
		for _, req := range reqs {
			if req.ValidationError != nil {
				return false
			}
		}
	}
	return true
}

// WriteSimulationReport produces the HTTP response for the POST /simulate-put
// endpoints.
//
//nolint:dupl // This function is very similar to RateLimitUpdater.WriteSimulationReport, but cannot be deduped because of different content types.
func (u QuotaUpdater) WriteSimulationReport(w http.ResponseWriter) {
	type unacceptableResource struct {
		ServiceType  string `json:"service_type"`
		ResourceName string `json:"resource_name"`
		core.QuotaValidationError
	}
	var result struct {
		IsValid               bool                   `json:"success"`
		UnacceptableResources []unacceptableResource `json:"unacceptable_resources,omitempty"`
	}
	result.IsValid = true //until proven otherwise

	for srvType, reqs := range u.Requests {
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

	respondwith.JSON(w, http.StatusOK, result)
}

// WritePutErrorResponse produces a negative HTTP response for this PUT request.
// It may only be used when `u.IsValid()` is false.
func (u QuotaUpdater) WritePutErrorResponse(w http.ResponseWriter) {
	var lines []string
	hasSubstatus := make(map[int]bool)

	//collect error messages
	for srvType, reqs := range u.Requests {
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

// CommitAuditTrail prepares an audit.Trail instance for this updater and
// commits it.
func (u QuotaUpdater) CommitAuditTrail(token *gopherpolicy.Token, r *http.Request, requestTime time.Time) {
	projectName := ""
	projectUUID := ""
	if u.Project != nil {
		projectName = u.Project.Name
		projectUUID = u.Project.UUID
	}

	invalid := !u.IsValid()
	statusCode := http.StatusOK
	if invalid {
		statusCode = http.StatusUnprocessableEntity
	}

	for srvType, reqs := range u.Requests {
		for resName, req := range reqs {
			qdConfig := u.Cluster.QuotaDistributionConfigForResource(srvType, resName)
			// low-privilege-raise metrics
			if qdConfig.Model == limesresources.HierarchicalQuotaDistribution && u.CanRaiseLP(srvType, resName) && !u.CanRaise(srvType, resName) {
				labels := prometheus.Labels{
					"service":  srvType,
					"resource": resName,
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
					DomainName:   u.Domain.Name,
					ProjectID:    projectUUID, //is empty for domain quota updates, see above
					ProjectName:  projectName,
					ServiceType:  srvType,
					ResourceName: resName,
					OldQuota:     req.OldValue,
					NewQuota:     req.NewValue,
					QuotaUnit:    req.Unit,
					RejectReason: rejectReason,
				})
		}
	}
}
