// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"fmt"
	"reflect"
	"slices"
	"time"

	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/db"
)

// ProjectResourceUpdate describes an operation that updates resource data
// within a single project service.
type ProjectResourceUpdate struct {
	// A custom callback that will be called once for each resource in the given service.
	UpdateResource func(*db.ProjectResource, liquid.ResourceName) error
	// If nil, logg.Error is used. Unit tests should give t.Errorf here.
	LogError func(msg string, args ...any)
}

// Run executes the given ProjectResourceUpdate operation:
//
//   - Missing ProjectResource entries are created.
//   - The `UpdateResource` callback is called for each resource to allow the
//     caller to update resource data as necessary.
func (u ProjectResourceUpdate) Run(dbi db.Interface, serviceInfo liquid.ServiceInfo, now time.Time, domain db.Domain, project db.Project, srv db.Service) ([]db.ProjectResource, error) {
	if u.LogError == nil {
		u.LogError = logg.Error
	}

	// We will first collect all existing data into one of these structs for each
	// resource. Then we will compute the target state of the DB record. We only
	// need to write into the DB if `.Target` ends up different from `.Original`.
	type resourceState struct {
		Info                  *liquid.ResourceInfo
		Original              *db.ProjectResource
		CorrespondingResource *db.Resource
	}

	// collect resources for reference of the resource_id
	var resources []db.Resource
	_, err := dbi.Select(&resources, `SELECT * FROM resources WHERE service_id = $1`, srv.ID)
	if err != nil {
		return nil, fmt.Errorf("while loading %s resources: %w", srv.Type, err)
	}
	var (
		resourcesByID   = make(map[db.ResourceID]db.Resource, len(resources))
		resourcesByName = make(map[liquid.ResourceName]db.Resource, len(resources))
	)
	for _, resource := range resources {
		resourcesByID[resource.ID] = resource
		resourcesByName[resource.Name] = resource
	}

	// collect ResourceInfo instances for this service
	allResources := make(map[liquid.ResourceName]resourceState)
	for resName, resInfo := range serviceInfo.Resources {
		correspondingResource := resourcesByName[resName]
		allResources[resName] = resourceState{
			Original:              nil, // might be filled in the next loop below
			Info:                  &resInfo,
			CorrespondingResource: &correspondingResource,
		}
	}

	// collect existing project_resources for this service
	var dbResources []db.ProjectResource
	_, err = dbi.Select(&dbResources, `SELECT pr.* FROM project_resources pr JOIN resources r ON pr.resource_id = r.id WHERE r.service_id = $1 AND pr.project_id = $2`, srv.ID, project.ID)
	if err != nil {
		return nil, fmt.Errorf("while loading %s project resources: %w", srv.Type, err)
	}
	for _, res := range dbResources {
		correspondingResource := resourcesByID[res.ResourceID]
		allResources[correspondingResource.Name] = resourceState{
			Original:              &res,
			CorrespondingResource: &correspondingResource,
			Info:                  allResources[correspondingResource.Name].Info, // might be nil if not filled in the previous loop
		}
	}

	// go through resources in a defined order (to ensure deterministic test behavior)
	allResourceNames := make([]liquid.ResourceName, 0, len(allResources))
	for resName := range allResources {
		allResourceNames = append(allResourceNames, resName)
	}
	slices.Sort(allResourceNames)

	// for each resource...
	var result []db.ProjectResource
	hasBackendQuotaDrift := false
	for _, resName := range allResourceNames {
		state := allResources[resName]
		// skip project_resources that we do not know about (we do not delete them
		// here because the ResourceInfo might only be missing temporarily because
		// of an error in resource discovery; in that case, deleting the project
		// resource would get rid of the only authoritative source of truth for that
		// resource's quota values)
		if state.Info == nil {
			u.LogError(
				"project service %d (%s of %s/%s) has unknown resource %q (was this resource type removed from the liquid?)",
				srv.ID, srv.Type, domain.Name, project.Name, resName,
			)
			continue
		}
		resInfo := *state.Info

		// setup a copy of `state.Original` (or a new resource) that we can write into
		res := db.ProjectResource{
			ProjectID:  project.ID,
			ResourceID: state.CorrespondingResource.ID,
		}
		if state.Original != nil {
			res = *state.Original
		}

		// update in place while enforcing validation rules
		validateResourceConstraints(&res, resInfo)
		if u.UpdateResource != nil {
			err := u.UpdateResource(&res, resName)
			if err != nil {
				return nil, err
			}
			validateResourceConstraints(&res, resInfo)
		}

		// insert or update resource if changes have been made
		if state.Original == nil {
			err := dbi.Insert(&res)
			if err != nil {
				return nil, fmt.Errorf("while inserting %s/%s resource in the DB: %w", srv.Type, resName, err)
			}
		} else if !reflect.DeepEqual(*state.Original, res) {
			_, err := dbi.Update(&res)
			if err != nil {
				return nil, fmt.Errorf("while updating %s/%s resource in the DB: %w", srv.Type, resName, err)
			}
		}
		result = append(result, res)

		// check if we need to arrange for SetQuotaJob to look at this project service
		if resInfo.HasQuota && resInfo.Topology != liquid.AZSeparatedTopology {
			backendQuota := res.BackendQuota.UnwrapOr(-1)
			quota := res.Quota.UnwrapOr(0) // definitely not None, it was set above in validateResourceConstraints()
			if backendQuota < 0 || uint64(backendQuota) != quota {
				hasBackendQuotaDrift = true
			}
		}
	}

	// if this update caused `quota != backend_quota` anywhere,
	// request SetQuotaJob to take over (unless we already have an open request)
	if hasBackendQuotaDrift {
		query := `UPDATE project_services ps SET quota_desynced_at = $1 FROM services s WHERE s.id = ps.service_id AND s.id = $2 AND ps.project_id = $3 AND quota_desynced_at IS NULL`
		_, err := dbi.Exec(query, now, srv.ID, project.ID)
		if err != nil {
			return nil, fmt.Errorf("while scheduling backend sync for %s quotas: %w", srv.Type, err)
		}
	}

	return result, nil
}

// Ensures that `res` conforms to various constraints and validation rules.
func validateResourceConstraints(res *db.ProjectResource, resInfo liquid.ResourceInfo) {
	if !resInfo.HasQuota || resInfo.Topology == liquid.AZSeparatedTopology {
		// ensure that NoQuota resources do not contain any quota values
		res.Quota = None[uint64]()
		res.BackendQuota = None[int64]()
	} else if res.Quota.IsNone() {
		// apply missing default quota
		res.Quota = Some[uint64](0)
	}
}
