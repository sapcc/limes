// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/reports"
	"github.com/sapcc/limes/internal/util"
)

// RateLimitUpdater contains most of the business logic for PUT on rate limits.
type RateLimitUpdater struct {
	// scope (all fields are always set since rate limits can only be updated on
	// the project level)
	Cluster *core.Cluster
	Domain  *db.Domain
	Project *db.Project

	// AuthZ info
	CanSetRateLimit func(db.ServiceType) bool
	Auditor         audittools.Auditor

	// Filled by ValidateInput() with the keys being the service type and the rate name.
	Requests map[db.ServiceType]map[liquid.RateName]RateLimitRequest
}

// RateLimitRequest describes a single rate limit that a PUT requests wants to change.
// It appears in type RateLimitUpdater.
type RateLimitRequest struct {
	Unit            limes.Unit
	OldLimit        uint64
	NewLimit        uint64
	OldWindow       limesrates.Window
	NewWindow       limesrates.Window
	ValidationError *RateValidationError
}

// RateValidationError appears in the Limes API in the POST .../simulate-put responses.
type RateValidationError struct {
	Status  int    `json:"status"` // an HTTP status code, e.g. http.StatusForbidden
	Message string `json:"message"`
}

// ValidateInput reads the given input and validates the quotas contained therein.
// Results are collected into u.Requests. The return value is only set for unexpected
// errors, not for validation errors.
func (u *RateLimitUpdater) ValidateInput(input limesrates.RateRequest, dbi db.Interface) error {
	serviceInfos, err := u.Cluster.AllServiceInfos()
	if err != nil {
		return err
	}

	projectReport, err := GetProjectRateReport(u.Cluster, *u.Domain, *u.Project, dbi, reports.Filter{}, serviceInfos)
	if err != nil {
		return err
	}

	nm := core.BuildRateNameMapping(u.Cluster, serviceInfos)
	u.Requests = make(map[db.ServiceType]map[liquid.RateName]RateLimitRequest)

	// Go through all services and validate the requested rate limits.
	for apiServiceType, in := range input {
		for apiRateName, newRateLimit := range in {
			dbServiceType, dbRateName, exists := nm.MapFromV1API(apiServiceType, apiRateName)
			if !exists {
				// it is ugly that this breaks the existing error reporting format, but
				// since we don't have a DB-level identifier, we cannot record this in
				// our usual structure
				return fmt.Errorf("no such rate: %s/%s", apiServiceType, apiRateName)
			}
			serviceConfig, ok := u.Cluster.Config.GetLiquidConfigurationForType(dbServiceType)
			if !ok {
				// defense in depth: should not occur because then we should have had `!exists` above
				continue
			}
			if u.Requests[dbServiceType] == nil {
				u.Requests[dbServiceType] = make(map[liquid.RateName]RateLimitRequest)
			}

			req := RateLimitRequest{
				NewLimit:  newRateLimit.Limit,
				NewWindow: newRateLimit.Window,
			}

			// only allow setting rate limits for which a default exists
			defaultRateLimit, exists := serviceConfig.RateLimits.GetProjectDefaultRateLimit(dbRateName)
			if exists {
				req.Unit = defaultRateLimit.Unit
			} else {
				req.ValidationError = &RateValidationError{
					Status:  http.StatusForbidden,
					Message: "user is not allowed to create new rate limits",
				}
				u.Requests[dbServiceType][dbRateName] = req
				continue
			}

			if projectService, exists := projectReport.Services[apiServiceType]; exists {
				projectRate, exists := projectService.Rates[apiRateName]
				if exists && projectRate.Limit != 0 && projectRate.Window != nil {
					req.OldLimit = projectRate.Limit
					req.OldWindow = *projectRate.Window
				} else {
					req.OldLimit = defaultRateLimit.Limit
					req.OldWindow = defaultRateLimit.Window
				}
			}

			// skip if rate limit was not changed
			if req.OldLimit == req.NewLimit && req.OldWindow == req.NewWindow {
				continue
			}

			// value is valid and novel -> perform further validation
			req.ValidationError = u.validateRateLimit(dbServiceType)
			u.Requests[dbServiceType][dbRateName] = req
		}
	}

	return nil
}

func (u RateLimitUpdater) validateRateLimit(serviceType db.ServiceType) *RateValidationError {
	if u.CanSetRateLimit(serviceType) {
		return nil
	}
	return &RateValidationError{
		Status:  http.StatusForbidden,
		Message: fmt.Sprintf("user is not allowed to set %q rate limits", serviceType),
	}
}

// IsValid returns true if all u.LimitRequests are valid (i.e. ValidationError == nil).
func (u RateLimitUpdater) IsValid() bool {
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
func (u RateLimitUpdater) WriteSimulationReport(w http.ResponseWriter) {
	type unacceptableRateLimit struct {
		ServiceType limes.ServiceType   `json:"service_type"`
		Name        limesrates.RateName `json:"name"`
		RateValidationError
	}
	var result struct {
		IsValid                bool                    `json:"success"`
		UnacceptableRateLimits []unacceptableRateLimit `json:"unacceptable_rates,omitempty"`
	}
	result.IsValid = true // until proven otherwise

	for dbServiceType, reqs := range u.Requests {
		for dbRateName, req := range reqs {
			if req.ValidationError != nil {
				result.IsValid = false
				apiIdentity := u.Cluster.BehaviorForRate(dbServiceType, dbRateName).IdentityInV1API
				result.UnacceptableRateLimits = append(result.UnacceptableRateLimits,
					unacceptableRateLimit{
						ServiceType:         apiIdentity.ServiceType,
						Name:                apiIdentity.Name,
						RateValidationError: *req.ValidationError,
					},
				)
			}
		}
	}

	// deterministic ordering for unit tests
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

// WritePutErrorResponse produces a negative HTTP response for this PUT request.
// It may only be used when `u.IsValid()` is false.
func (u RateLimitUpdater) WritePutErrorResponse(w http.ResponseWriter) {
	var lines []string
	hasSubstatus := make(map[int]bool)

	// collect error messages
	for dbServiceType, reqs := range u.Requests {
		for dbRateName, req := range reqs {
			if err := req.ValidationError; err != nil {
				apiIdentity := u.Cluster.BehaviorForRate(dbServiceType, dbRateName).IdentityInV1API
				hasSubstatus[err.Status] = true
				lines = append(
					lines,
					fmt.Sprintf("cannot change %s/%s rate limits: %s", apiIdentity.ServiceType, apiIdentity.Name, err.Message),
				)
			}
		}
	}
	sort.Strings(lines) // for determinism in unit test
	msg := strings.Join(lines, "\n")

	// when all errors have the same status, report that; otherwise use 422
	// (Unprocessable Entity) as a reasonable overall default
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
func (u RateLimitUpdater) CommitAuditTrail(token *gopherpolicy.Token, r *http.Request, requestTime time.Time) {
	invalid := !u.IsValid()
	statusCode := http.StatusOK
	if invalid {
		statusCode = http.StatusUnprocessableEntity
	}

	for dbServiceType, reqs := range u.Requests {
		for dbRateName, req := range reqs {
			// if !u.IsValid(), then all requested quotas in this PUT are considered
			// invalid (and none are committed), so set the rejectReason to explain this
			rejectReason := ""
			if invalid {
				if req.ValidationError == nil {
					rejectReason = "cannot commit this because other values in this request are unacceptable"
				} else {
					rejectReason = req.ValidationError.Message
				}
			}

			apiIdentity := u.Cluster.BehaviorForRate(dbServiceType, dbRateName).IdentityInV1API
			u.Auditor.Record(audittools.Event{
				Time:       requestTime,
				Request:    r,
				User:       token,
				ReasonCode: statusCode,
				Action:     cadf.UpdateAction,
				Target: util.RateLimitEventTarget{
					DomainID:    u.Domain.UUID,
					DomainName:  u.Domain.Name,
					ProjectID:   u.Project.UUID,
					ProjectName: u.Project.Name,
					ServiceType: apiIdentity.ServiceType,
					Name:        apiIdentity.Name,
					Payload: util.RateLimitChange{
						OldLimit:     req.OldLimit,
						NewLimit:     req.NewLimit,
						OldWindow:    req.OldWindow,
						NewWindow:    req.NewWindow,
						Unit:         req.Unit,
						RejectReason: rejectReason,
					},
				},
			})
		}
	}
}
