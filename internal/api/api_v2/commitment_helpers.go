// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"
	"go.xyrillian.de/gg/is"
	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/gg/options"

	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

var (
	errAZMustBeAny               = errors.New(`resource does not accept AZ-aware commitments, so the AZ must be set to "any"`)
	errCommitmentsDisabled       = errors.New("commitments are not enabled for this resource")
	errConfirmByInPast           = errors.New("confirm_by may not be set in the past")
	errConfirmByMissing          = errors.New("confirm_by must be set for the requested initial commitment status")
	errConfirmByNotAllowed       = errors.New("confirm_by may not be set for the requested initial commitment status")
	errEmptyAmount               = errors.New("amount of committed resource must be greater than zero")
	errInvalidInitialStatus      = errors.New("initial commitment status value is invalid")
	errNoSuchAZ                  = errors.New("no such availability zone")
	errNoSuchResource            = errors.New("no such resource")
	errNoSuchService             = errors.New("no such service")
	errNotifyOnConfirmNotAllowed = errors.New("notify_on_confirm may not be set for commitments with immediate confirmation")
	errResourceForbidden         = errors.New("resource is not enabled in this project")
)

func convertCommitmentToDisplayForm(c db.ProjectCommitment, path db.AZResourcePath, project db.Project, canBeDeleted bool) resourcesv2.Commitment {
	return resourcesv2.Commitment{
		UUID:             c.UUID,
		Amount:           c.Amount,
		Duration:         c.Duration,
		ProjectUUID:      project.UUID,
		ServiceType:      path.ServiceType,
		ResourceName:     path.ResourceName,
		AvailabilityZone: path.AvailabilityZone,
		Status:           c.Status,
		TransferStatus:   c.TransferStatus,
		TransferToken:    c.TransferToken,
		CreatedAt:        limes.UnixEncodedTime{Time: c.CreatedAt},
		CreatorUUID:      c.CreatorUUID,
		CreatorName:      c.CreatorName,
		CanBeDeleted:     canBeDeleted,
		ConfirmBy:        options.Map(c.ConfirmBy, util.IntoUnixEncodedTime),
		ConfirmedAt:      options.Map(c.ConfirmedAt, util.IntoUnixEncodedTime),
		ExpiresAt:        limes.UnixEncodedTime{Time: c.ExpiresAt},
		NotifyOnConfirm:  c.NotifyOnConfirm,
		WasRenewed:       c.RenewContextJSON.IsSome(),
	}
}

// validateCommittability checks that the AZ resource identified by `path`:
//   - exists in the given project scope, and
//   - allows commitments of the specified duration.
func (p *v2Provider) validateCommittability(path db.AZResourcePath, dbDomain db.Domain, dbProject db.Project, duration limesresources.CommitmentDuration) (_ db.Service, _ db.AZResource, _ core.ScopedCommitmentBehavior, err error) {
	service, ok := p.Cluster.SIC.GetServiceForType(path.ServiceType)
	if !ok {
		err = respondwith.CustomStatus(http.StatusUnprocessableEntity, errNoSuchService)
		return
	}
	resource, ok := p.Cluster.SIC.GetResourceForPath(path.Resource())
	if !ok {
		err = respondwith.CustomStatus(http.StatusUnprocessableEntity, errNoSuchResource)
		return
	}

	var forbidden bool
	err = p.DB.QueryRow(`SELECT forbidden FROM project_resources WHERE project_id = $1 AND resource_id = $2`,
		dbProject.ID, resource.ID).Scan(&forbidden)
	if err != nil {
		return
	}
	if forbidden {
		err = respondwith.CustomStatus(http.StatusUnprocessableEntity, errResourceForbidden)
		return
	}

	behavior := p.Cluster.CommitmentBehaviorForResourcePath(path.Resource()).ForDomain(dbDomain.Name)
	if len(behavior.Durations) == 0 {
		err = respondwith.CustomStatus(http.StatusUnprocessableEntity, errCommitmentsDisabled)
		return
	}
	if !slices.Contains(behavior.Durations, duration) {
		buf := must.Return(json.Marshal(behavior.Durations)) // panic on error is acceptable here, marshals should never fail
		msg := "unacceptable commitment duration for this resource; acceptable values: " + string(buf)
		err = respondwith.CustomStatus(http.StatusUnprocessableEntity, errors.New(msg))
		return
	}

	if resource.Topology == liquid.FlatTopology {
		if path.AvailabilityZone != limes.AvailabilityZoneAny {
			err = respondwith.CustomStatus(http.StatusUnprocessableEntity, errAZMustBeAny)
			return
		}
	} else {
		if !slices.Contains(p.Cluster.Config.AvailabilityZones, path.AvailabilityZone) {
			err = respondwith.CustomStatus(http.StatusUnprocessableEntity, errNoSuchAZ)
			return
		}
	}

	// If we did this load earlier, we could only report a generic error like
	// "something about this service/resource/AZ combo is not right", instead of
	// the more specific errors we produced above.
	//
	// With all the previous validations having succeeded, it should not be
	// possible to fail this load, so errors are considered server errors here.
	azResource, ok := p.Cluster.SIC.GetAZResourceForPath(path)
	if !ok {
		err = fmt.Errorf("could not find az_resources entry for path = %q in ServiceInfoCache", path.String())
		return
	}

	return service, azResource, behavior, nil
}

type commitmentStatusAttributes struct {
	Status          liquid.CommitmentStatus
	ConfirmBy       Option[time.Time]
	NotifyOnConfirm bool
}

// validateStatusAttributesOnNewCommitment validates the Status, ConfirmBy and
// NotifyOnConfirm fields of a CommitmentRequest.
func (p *v2Provider) validateStatusAttributesOnNewCommitment(attrs commitmentStatusAttributes, behavior core.ScopedCommitmentBehavior, now time.Time) error {
	switch attrs.Status {
	case liquid.CommitmentStatusPlanned, liquid.CommitmentStatusGuaranteed:
		if attrs.ConfirmBy.IsNone() {
			return respondwith.CustomStatus(http.StatusUnprocessableEntity, errConfirmByMissing)
		}
	case liquid.CommitmentStatusPending, liquid.CommitmentStatusConfirmed:
		if attrs.ConfirmBy.IsSome() {
			return respondwith.CustomStatus(http.StatusUnprocessableEntity, errConfirmByNotAllowed)
		}
	default:
		return respondwith.CustomStatus(http.StatusUnprocessableEntity, errInvalidInitialStatus)
	}

	if attrs.ConfirmBy.IsSomeAnd(is.Before(now)) {
		return respondwith.CustomStatus(http.StatusUnprocessableEntity, errConfirmByInPast)
	}
	if msg := behavior.CanConfirmCommitmentsAt(attrs.ConfirmBy.UnwrapOr(now)); msg != "" {
		return respondwith.CustomStatus(http.StatusUnprocessableEntity, errors.New(msg))
	}

	if attrs.NotifyOnConfirm && attrs.Status == liquid.CommitmentStatusConfirmed {
		return respondwith.CustomStatus(http.StatusUnprocessableEntity, errNotifyOnConfirmNotAllowed)
	}

	return nil
}

// pazrCommitmentStats contains statistics about existing commitments in a specific ProjectAZResource (pazr) scope.
type pazrCommitmentStats struct {
	TotalConfirmed  uint64
	TotalGuaranteed uint64
}

var pazrCommitmentStatsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	SELECT SUM(amount) FILTER (WHERE status = {{liquid.CommitmentStatusConfirmed}}) AS total_confirmed,
	       SUM(amount) FILTER (WHERE status = {{liquid.CommitmentStatusGuaranteed}}) AS total_guaranteed
	  FROM project_commitments
	 WHERE project_id = $1 AND az_resource_id = $2
`))

// getCommitmentStats collects statistics about existing commitments in a specific ProjectAZResource (pazr) scope.
func getCommitmentStats(dbi db.Interface, projectID db.ProjectID, azResourceID db.AZResourceID) (stats pazrCommitmentStats, err error) {
	err = dbi.QueryRow(pazrCommitmentStatsQuery, projectID, azResourceID).
		Scan(&stats.TotalConfirmed, &stats.TotalGuaranteed)
	return
}

// analyzeCommitmentChangeResponse converts a CommitmentChangeResponse into an API error unless the response is positive.
func analyzeCommitmentChangeResponse(resp liquid.CommitmentChangeResponse) error {
	if resp.RejectionReason == "" {
		return nil
	}
	err := respondwith.CustomStatus(http.StatusConflict, errors.New(resp.RejectionReason))
	if retryAt, exists := resp.RetryAt.Unpack(); exists {
		return errorWithResponseHeaders{
			Header: http.Header{"Retry-After": {retryAt.Format(time.RFC1123)}},
			Err:    err,
		}
	} else {
		return err
	}
}
