// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-api-declarations/opts"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"
	"go.xyrillian.de/oblast"

	"github.com/sapcc/limes/internal/api/reports_v2"
	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

func (p *v2Provider) handleGetCommitmentSingle(r *http.Request, token *gopherpolicy.Token) (resourcesv2.Commitment, error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/commitments/:commitment_uuid")
	var (
		none resourcesv2.Commitment
		ctx  = r.Context()
		sis  = p.Cluster.SIC.GetSnapshot()
	)

	cUUID := liquid.CommitmentUUID(token.Context.Request["commitment_uuid"])
	c, err := db.ProjectCommitmentStore.SelectOneWhere(ctx, p.DB, `uuid = $1`, cUUID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return none, respondwith.CustomStatus(http.StatusNotFound, errNoSuchCommitment)
	case err != nil:
		return none, err
	}

	// check auth for this commitment, if found
	scope, err := p.checkProjectAccessByID(ctx, token, c.ProjectID, "v2:project:commitment_get")
	if err != nil {
		return none, err
	}
	if slices.Contains([]liquid.CommitmentStatus{util.CommitmentStatusDeleted, liquid.CommitmentStatusSuperseded, liquid.CommitmentStatusExpired}, c.Status) {
		err = token.Enforce("v2:project:with_inactive")
		if err != nil {
			return none, err
		}
	}

	// display form
	// obtain service ref
	azRes, ok := sis.GetAZResourceForID(c.AZResourceID)
	if !ok {
		// defense in depth, the referenced AZResource should exist
		return none, errInvalidResourceReference
	}
	canBeDeleted := datamodel.CanDeleteCommitment(token, c, p.timeNow)
	result := convertCommitmentToDisplayForm(c, azRes.Path, scope.Project.UUID, canBeDeleted)
	return result, nil
}

// idea of this sql is that it contains all possible combinations and the code
// will replace the dynamic parts with a combination that is semantically valid.
var findCommitmentsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	SELECT p.uuid as project_uuid, pc.* FROM project_commitments pc
	JOIN projects p ON pc.project_id = p.id
	JOIN domains d ON p.domain_id = d.id
	WHERE {{pc.az_resource_id = ANY($az_resource_id)}} 
	AND {{d.id = ANY($domain_id)}}
	AND {{p.id = ANY($project_id)}}
	$with_public{{AND pc.transfer_status = {{limesresources.CommitmentTransferStatusPublic}}}}
	AND {{pc.updated_at >= $updated_after}}
	$without_inactive{{AND pc.status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}, {{util.CommitmentStatusDeleted}})}}
	ORDER BY pc.uuid
`))

func (p *v2Provider) handleGetCommitmentMultiple(r *http.Request, token *gopherpolicy.Token) (resourcesv2.CommitmentList, error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/commitments")
	var (
		none  resourcesv2.CommitmentList
		scope reports_v2.Scope
		ctx   = r.Context()
		sis   = p.Cluster.SIC.GetSnapshot()
	)

	// parse string and basic option checks
	options, err := opts.ParseQueryString[common.CommitmentListOpts](r.URL.Query())
	if err != nil {
		return none, respondwith.CustomStatus(http.StatusBadRequest, err)
	}
	err = checkCommitmentListOpts(token, options)
	if err != nil {
		return none, err
	}

	// handle rest of auth and scope - checkOptions made sure that this is consistent
	if domainUUID, ok := options.DomainUUID.Unpack(); ok {
		scope, err = p.checkDomainAccess(ctx, token, domainUUID, "v2:project:commitment_get")
		if err != nil {
			return none, err
		}
	} else if projectUUID, ok := options.ProjectUUID.Unpack(); ok {
		scope, err = p.checkProjectAccess(ctx, token, projectUUID, "v2:project:commitment_get")
		if err != nil {
			return none, err
		}
	} else {
		scope = reports_v2.ClusterScope{}
	}

	// get filter
	filter, err := reports_v2.FilterFromCommitmentListOpts(p.Cluster, options)
	if err != nil {
		return none, respondwith.CustomStatus(http.StatusBadRequest, err)
	}

	// get query
	query := reports_v2.EvalCommitmentListExtraProps(findCommitmentsQuery, options)
	query, args := reports_v2.EvalCommitmentListOptsGenericFilters(options, query)
	query, args = filter.ExpandServiceFilters(query, args...)
	query, args = scope.ExpandScopeFilters(query, args...)

	// exec
	result := resourcesv2.CommitmentList{}
	type record struct {
		ProjectUUID liquid.ProjectUUID `db:"project_uuid"`
		db.ProjectCommitment
	}
	err = oblast.MustNewStore[record](oblast.PostgresDialect()).Select(ctx, p.DB, query, args...).Foreach(func(c record) error {
		// display form
		// obtain service ref
		azRes, ok := sis.GetAZResourceForID(c.AZResourceID)
		if !ok {
			// defense in depth, the referenced AZResource should exist
			return errInvalidResourceReference
		}
		canBeDeleted := datamodel.CanDeleteCommitment(token, c.ProjectCommitment, p.timeNow)
		result.Commitments = append(result.Commitments, convertCommitmentToDisplayForm(c.ProjectCommitment, azRes.Path, c.ProjectUUID, canBeDeleted))
		return nil
	})

	if err != nil {
		return none, err
	}
	return result, nil
}

func checkCommitmentListOpts(token *gopherpolicy.Token, options common.CommitmentListOpts) error {
	// check path filter
	if (options.ResourceName.IsSome() || options.Category.IsSome()) && options.ServiceType.IsNone() {
		return respondwith.CustomStatus(http.StatusBadRequest, errors.New(`"category" or "resource" require "service" to be set`))
	}

	// check too many main filters
	cnt := 0
	if options.OnlyPublic {
		cnt++
	}
	if options.ProjectUUID.IsSome() {
		cnt++
	}
	if options.DomainUUID.IsSome() {
		cnt++
	}
	if cnt > 1 {
		return respondwith.CustomStatus(http.StatusBadRequest, errors.New(`only one of "public", "project_uuid", "domain_uuid" may be set`))
	}

	isAdmin := token.Check(`v2:project:commitment_get_unscoped`)
	// check too few main filters, admins still require a path-filter
	if cnt == 0 {
		if isAdmin && options.ServiceType.IsNone() {
			return respondwith.CustomStatus(http.StatusBadRequest, errors.New(`one of "category" or "resource" must be set`))
		}
		if !isAdmin {
			return respondwith.CustomStatus(http.StatusBadRequest, errors.New(`one of "public", "project_uuid", "domain_uuid" must be set`))
		}
	}

	// rest of the auth checks
	// note: the domain/ project scope is checked where we assemble the scope later, not here
	if options.OnlyPublic {
		err := token.Enforce("v2:project:commitment_get_public")
		if err != nil {
			return err
		}
	}
	if options.WithInactive && !token.Check("v2:project:with_inactive") {
		return respondwith.CustomStatus(http.StatusForbidden, errors.New(`"with=inactive" requires special permissions`))
	}
	return nil
}
