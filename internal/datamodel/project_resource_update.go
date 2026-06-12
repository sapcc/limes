// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// ProjectResourceUpdate describes an operation that updates resource data
// within a single project service.
type ProjectResourceUpdate struct {
	// A custom callback that will be called once for each resource in the given service.
	UpdateResource func(*db.ProjectResource, liquid.ResourceName) error
}

// Run executes the given ProjectResourceUpdate operation:
//
//   - Missing ProjectResource entries are created.
//   - The `UpdateResource` callback is called for each resource to allow the
//     caller to update resource data as necessary.
func (u ProjectResourceUpdate) Run(dbi db.Interface, project db.Project, filteredSIS core.FilteredServiceInfoSnapshot) error {
	service := must.BeOK(filteredSIS.GetFilteredService())
	resources, _ := filteredSIS.GetResourcesForType(service.Type)
	// We will first collect all existing data into one of these structs for each
	// resource. Then we will compute the target state of the DB record. We only
	// need to write into the DB if `.Target` ends up different from `.Original`.
	type resourceState struct {
		Original              *db.ProjectResource
		CorrespondingResource *db.Resource
	}

	resourcesByID := db.BuildIndexOfArray(slices.Collect(maps.Values(resources)), func(r db.Resource) db.ResourceID { return r.ID })
	allResourceNames := slices.Sorted(maps.Keys(resources))
	// collect allResources instances for this service
	allResources := make(map[liquid.ResourceName]resourceState)
	for resName, res := range resources {
		allResources[resName] = resourceState{
			Original:              nil, // might be filled in the next loop below
			CorrespondingResource: &res,
		}
	}

	// collect existing project_resources for this service
	var projectResources []db.ProjectResource
	_, err := dbi.Select(&projectResources, `SELECT pr.* FROM project_resources pr JOIN resources r ON pr.resource_id = r.id WHERE r.service_id = $1 AND pr.project_id = $2`, service.ID, project.ID)
	if err != nil {
		return fmt.Errorf("while loading %s project resources: %w", service.Type, err)
	}
	for _, res := range projectResources {
		correspondingResource := resourcesByID[res.ResourceID]
		allResources[correspondingResource.Name] = resourceState{
			Original:              &res,
			CorrespondingResource: &correspondingResource,
		}
	}

	// go through resources in a defined order (to ensure deterministic test behavior)
	for _, resName := range allResourceNames {
		state := allResources[resName]

		// setup a copy of `state.Original` (or a new resource) that we can write into
		res := db.ProjectResource{
			ProjectID:  project.ID,
			ResourceID: state.CorrespondingResource.ID,
		}
		if state.Original != nil {
			res = *state.Original
		}

		// update in place
		if u.UpdateResource != nil {
			err := u.UpdateResource(&res, resName)
			if err != nil {
				return err
			}
		}

		// insert or update resource if changes have been made
		if state.Original == nil {
			err := dbi.Insert(&res)
			if err != nil {
				return fmt.Errorf("while inserting %s/%s resource in the DB: %w", service.Type, resName, err)
			}
		} else if !reflect.DeepEqual(*state.Original, res) {
			_, err := dbi.Update(&res)
			if err != nil {
				return fmt.Errorf("while updating %s/%s resource in the DB: %w", service.Type, resName, err)
			}
		}
	}

	return nil
}
