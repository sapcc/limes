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
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/limes/pkg/audit"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/reports"
)

//QuotaUpdater contains the shared code for domain and project PUT requests.
//See func PutDomain and func PutProject for how it's used.
type QuotaUpdater struct {
	//scope
	Cluster *limes.Cluster
	Domain  *db.Domain  //always set (for project quota updates, contains the project'u domain)
	Project *db.Project //nil for domain quota updates
	//AuthZ info
	CanRaise bool
	CanLower bool

	//filled by ValidateInput(), key = service type + resource name
	Requests map[string]map[string]QuotaRequest
}

//QuotaRequest describes a single quota value that a PUT request wants to change. It appears in type QuotaUpdater.
type QuotaRequest struct {
	OldValue        uint64
	NewValue        uint64
	Unit            limes.Unit
	ValidationError error
}

//ScopeType is used for constructing error messages.
func (u QuotaUpdater) ScopeType() string {
	if u.Project == nil {
		return "domain"
	}
	return "project"
}

//QuotaConstraints returns the quota constraints that apply to this updater's scope.
func (u QuotaUpdater) QuotaConstraints() limes.QuotaConstraints {
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

//ValidateInput reads the given input and validates the quotas contained therein.
//Results are collected into u.Requests. The return value is only set for unexpected
//errors, not for validation errors.
func (u *QuotaUpdater) ValidateInput(input ServiceQuotas, dbi db.Interface) error {
	//gather a report on the domain's quotas to decide whether a quota update is legal
	domainReport, err := GetDomainReport(u.Cluster, *u.Domain, dbi, reports.Filter{})
	if err != nil {
		return err
	}
	//for project scope, we also need a project report for validation
	var projectReport *reports.Project
	if u.Project != nil {
		projectReport, err = GetProjectReport(u.Cluster, *u.Domain, *u.Project, dbi, reports.Filter{}, false)
		if err != nil {
			return err
		}
	}

	//go through all services and resources and validate the requested quotas
	u.Requests = make(map[string]map[string]QuotaRequest)
	for _, quotaPlugin := range u.Cluster.QuotaPlugins {
		srv := quotaPlugin.ServiceInfo()
		u.Requests[srv.Type] = make(map[string]QuotaRequest)

		for _, res := range quotaPlugin.Resources() {
			//find the report data for this resource
			var (
				domRes  *reports.DomainResource
				projRes *reports.ProjectResource
			)
			if domainService, exists := domainReport.Services[srv.Type]; exists {
				domRes = domainService.Resources[res.Name]
			}
			if domRes == nil {
				return fmt.Errorf("no domain report for resource %s/%s", srv.Type, res.Name)
			}
			if u.Project != nil {
				if projectService, exists := projectReport.Services[srv.Type]; exists {
					projRes = projectService.Resources[res.Name]
				}
				if projRes == nil {
					return fmt.Errorf("no project report for resource %s/%s", srv.Type, res.Name)
				}
			}

			req := QuotaRequest{
				OldValue: domRes.DomainQuota,
				Unit:     domRes.Unit,
			}
			if u.Project != nil {
				req.OldValue = projRes.Quota
			}

			//validation phase 1: convert given value to correct unit
			newQuota, exists := input[srv.Type][res.Name]
			if !exists {
				continue
			}
			req.NewValue, req.ValidationError = newQuota.ConvertFor(u.Cluster, srv.Type, res.Name)

			//skip this resource entirely if no change is requested
			if req.ValidationError == nil && req.OldValue == newQuota.Value {
				continue //with next resource
			}

			//validation phase 2: can we change this quota at all?
			if req.ValidationError == nil {
				if res.ExternallyManaged {
					req.ValidationError = errors.New("resource is managed externally")
				}
			}

			//validation phase 3: check quota constraints
			if req.ValidationError == nil {
				constraint := u.QuotaConstraints()[srv.Type][res.Name]
				if !constraint.Allows(req.NewValue) {
					req.ValidationError = fmt.Errorf("requested value %q contradicts constraint %q for this %s and resource",
						limes.ValueWithUnit{Value: req.NewValue, Unit: res.Unit},
						constraint.ToString(res.Unit), u.ScopeType())
				}
			}

			//validation phase 4: specific rules for domain quotas vs. project quotas
			if req.ValidationError == nil {
				if u.Project == nil {
					req.ValidationError = u.validateDomainQuota(*domRes, req.NewValue)
				} else {
					req.ValidationError = u.validateProjectQuota(*domRes, *projRes, req.NewValue)
				}
			}

			u.Requests[srv.Type][res.Name] = req
		}
	}

	return nil
}

func (u QuotaUpdater) validateDomainQuota(report reports.DomainResource, newQuota uint64) error {
	//if quota is being raised, only permission is required (overprovisioning of
	//domain quota over the cluster capacity is explicitly allowed because
	//capacity measurements are usually to be taken with a grain of salt)
	if report.DomainQuota < newQuota {
		if u.CanRaise {
			return nil
		}
		return fmt.Errorf("user is not allowed to raise quotas in this domain")
	}

	//if quota is being lowered, permission is required and the domain quota may
	//not be less than the sum of quotas that the domain gives out to projects
	if !u.CanLower {
		return fmt.Errorf("user is not allowed to lower quotas in this domain")
	}
	if newQuota < report.ProjectsQuota {
		return fmt.Errorf(
			"domain quota may not be smaller than sum of project quotas in that domain (%s)",
			report.Unit.Format(report.ProjectsQuota),
		)
	}

	return nil
}

func (u QuotaUpdater) validateProjectQuota(domRes reports.DomainResource, projRes reports.ProjectResource, newQuota uint64) error {
	//if quota is being reduced, permission is required and usage must fit into quota
	//(note that both res.Quota and newQuota are uint64, so we do not need to
	//cover the case of infinite quotas)
	if projRes.Quota > newQuota {
		if !u.CanLower {
			return fmt.Errorf("user is not allowed to lower quotas in this project")
		}
		if projRes.Usage > newQuota {
			return fmt.Errorf("quota may not be lower than current usage")
		}
		return nil
	}

	//if quota is being raised, permission is required and also the domain quota may not be exceeded
	if !u.CanRaise {
		return fmt.Errorf("user is not allowed to raise quotas in this project")
	}
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
		return fmt.Errorf("domain quota exceeded (maximum acceptable project quota is %s)",
			limes.ValueWithUnit{Value: maxQuota, Unit: domRes.Unit},
		)
	}

	return nil
}

//IsValid returns true if all u.Requests are valid (i.e. ValidationError == nil).
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

//ErrorMessage compiles all validation errors into a single user-readable string.
func (u QuotaUpdater) ErrorMessage() string {
	var lines []string
	for srvType, reqs := range u.Requests {
		for resName, req := range reqs {
			if req.ValidationError != nil {
				lines = append(lines, fmt.Sprintf("cannot change %s/%s quota: %s",
					srvType, resName, req.ValidationError.Error()))
			}
		}
	}
	sort.Strings(lines) //for determinism in unit test
	return strings.Join(lines, "\n")
}

////////////////////////////////////////////////////////////////////////////////
// integration with package audit

//CommitAuditTrail prepares an audit.Trail instance for this updater and
//commits it.
func (u QuotaUpdater) CommitAuditTrail(token *gopherpolicy.Token, r *http.Request, requestTime time.Time) {
	requestTimeStr := requestTime.Format("2006-01-02T15:04:05.999999+00:00")
	var trail audit.Trail

	projectUUID := ""
	if u.Project != nil {
		projectUUID = u.Project.UUID
	}

	invalid := !u.IsValid()
	statusCode := http.StatusOK
	if invalid {
		statusCode = http.StatusUnprocessableEntity
	}

	for srvType, reqs := range u.Requests {
		for resName, req := range reqs {
			//if !u.IsValid(), then all requested quotas in this PUT are considered
			//invalid (and none are committed), so set the rejectReason to explain this
			rejectReason := ""
			if invalid {
				if req.ValidationError == nil {
					rejectReason = "cannot commit this because other values in this request are unacceptable"
				} else {
					rejectReason = req.ValidationError.Error()
				}
			}

			trail.Add(audit.EventParams{
				Token:        token,
				Request:      r,
				ReasonCode:   statusCode,
				Time:         requestTimeStr,
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

	trail.Commit(u.Cluster.ID, u.Cluster.Config.CADF)
}
