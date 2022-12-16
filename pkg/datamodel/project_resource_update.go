/*******************************************************************************
*
* Copyright 2018-2022 SAP SE
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

package datamodel

import (
	"fmt"
	"reflect"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
)

// ProjectResourceUpdate describes an operation that updates resource data
// within a single project service.
type ProjectResourceUpdate struct {
	//A custom callback that will be called once for each resource in the given service.
	UpdateResource func(*db.ProjectResource) error
	//If nil, logg.Error is used. Unit tests should give t.Errorf here.
	LogError func(msg string, args ...interface{})
}

// ProjectResourceUpdateResult is the return value for ProjectUpdate.Run().
type ProjectResourceUpdateResult struct {
	//If true, some resources have a BackendQuota that differs from the
	//DesiredBackendQuota. The caller should call ApplyBackendQuota() for these
	//services once the DB transaction has been committed.
	HasBackendQuotaDrift bool
}

// Run executes the given ProjectResourceUpdate operation:
//
//   - Missing ProjectResource entries are created. If constraints are
//     configured for this project, they will be taken into account when
//     initializing missing database entries.
//   - The `UpdateResource` callback is called for each resource to allow the
//     caller to update resource data as necessary.
//   - Constraints are enforced and other derived fields are recomputed on all
//     ProjectResource entries.
func (u ProjectResourceUpdate) Run(dbi db.Interface, cluster *core.Cluster, domain db.Domain, project db.Project, serviceID int64, serviceType string) (*ProjectResourceUpdateResult, error) {
	if u.LogError == nil {
		u.LogError = logg.Error
	}
	var constraints map[string]core.QuotaConstraint
	if cluster.QuotaConstraints != nil {
		constraints = cluster.QuotaConstraints.Projects[domain.Name][project.Name][serviceType]
	}

	//We will first collect all existing data into one of these structs for each
	//resource. Then we will compute the target state of the DB record. We only
	//need to write into the DB if `.Target` ends up different from `.Original`.
	type resourceState struct {
		Info     *limesresources.ResourceInfo
		Original *db.ProjectResource
	}

	//collect ResourceInfo instances for this service
	allResources := make(map[string]resourceState)
	for _, resInfo := range cluster.QuotaPlugins[serviceType].Resources() {
		resInfo := resInfo
		allResources[resInfo.Name] = resourceState{
			Original: nil, //might be filled in the next loop below
			Info:     &resInfo,
		}
	}

	//collect existing resources for this service
	var dbResources []db.ProjectResource
	_, err := dbi.Select(&dbResources, `SELECT * FROM project_resources WHERE service_id = $1`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("while loading %s project resources: %w", serviceType, err)
	}
	for _, res := range dbResources {
		res := res
		allResources[res.Name] = resourceState{
			Original: &res,
			Info:     allResources[res.Name].Info, //might be nil if not filled in the previous loop
		}
	}

	//for each resource...
	hasChanges := false
	var result ProjectResourceUpdateResult
	for resName, state := range allResources {
		//skip project_resources that we do not know about (we do not delete them
		//here because the ResourceInfo might only be missing temporarily because
		//of an error in resource discovery; in that case, deleting the project
		//resource would get rid of the only authoritative source of truth for that
		//resource's quota values)
		if state.Info == nil {
			u.LogError(
				"project service %d (%s of %s/%s) has unknown resource %q (was this resource type removed from the quota plugin?)",
				serviceID, serviceType, domain.Name, project.Name, resName,
			)
			continue
		}
		resInfo := *state.Info

		//setup a copy of `state.Original` (or a new resource) that we can write into
		res := db.ProjectResource{
			ServiceID: serviceID,
			Name:      resName,
		}
		if state.Original != nil {
			res = *state.Original
		}

		//update in place while enforcing validation rules and constraints
		qdConfig := cluster.QuotaDistributionConfigForResource(serviceType, res.Name)
		validateResourceConstraints(domain, project, serviceType, &res, resInfo, constraints[res.Name], qdConfig)
		if u.UpdateResource != nil {
			err := u.UpdateResource(&res)
			if err != nil {
				return nil, err
			}
			validateResourceConstraints(domain, project, serviceType, &res, resInfo, constraints[res.Name], qdConfig)
		}

		//(re-)compute derived values
		if !resInfo.NoQuota {
			if project.HasBursting {
				behavior := cluster.BehaviorForResource(serviceType, res.Name, domain.Name+"/"+project.Name)
				desiredBackendQuota := behavior.MaxBurstMultiplier.ApplyTo(*res.Quota, qdConfig.Model)
				res.DesiredBackendQuota = &desiredBackendQuota
			} else {
				res.DesiredBackendQuota = res.Quota
			}
		}

		//insert or update resource if changes have been made
		if state.Original == nil {
			err := dbi.Insert(&res)
			if err != nil {
				return nil, fmt.Errorf("while inserting %s/%s resource in the DB: %w", serviceType, res.Name, err)
			}
			hasChanges = true
		} else if !reflect.DeepEqual(*state.Original, res) {
			_, err := dbi.Update(&res)
			if err != nil {
				return nil, fmt.Errorf("while updating %s/%s resource in the DB: %w", serviceType, res.Name, err)
			}
			hasChanges = true
		}

		//check if we need to tell the caller to call ApplyBackendQuota after the tx
		if !resInfo.NoQuota {
			backendQuota := unwrapOrDefault(res.BackendQuota, -1)
			desiredBackendQuota := *res.DesiredBackendQuota //definitely not nil, it was set above
			if backendQuota < 0 || uint64(backendQuota) != desiredBackendQuota {
				result.HasBackendQuotaDrift = true
			}
		}
	}

	if hasChanges {
		err = ApplyComputedDomainQuota(dbi, cluster, domain.ID, serviceType)
		if err != nil {
			return nil, fmt.Errorf("while recomputing %s domain quotas: %w", serviceType, err)
		}
	}

	return &result, nil
}

func unwrapOrDefault[T any](value *T, defaultValue T) T {
	if value == nil {
		return defaultValue
	}
	return *value
}

// Ensures that `res` conforms to various constraints and validation rules.
func validateResourceConstraints(domain db.Domain, project db.Project, serviceType string, res *db.ProjectResource, resInfo limesresources.ResourceInfo, constraint core.QuotaConstraint, qdConfig core.QuotaDistributionConfiguration) {
	if resInfo.NoQuota {
		//ensure that NoQuota resources do not contain any quota values
		res.Quota = nil
		res.BackendQuota = nil
		res.DesiredBackendQuota = nil
	} else {
		//check if we need to apply a missing default quota
		if res.Quota == nil || *res.Quota == 0 {
			initialQuota := qdConfig.InitialProjectQuota()
			res.Quota = &initialQuota
		}

		//check if we need to enforce a constraint
		constrainedQuota := constraint.ApplyTo(*res.Quota)
		if constrainedQuota != *res.Quota {
			logg.Other("AUDIT", "changing %s/%s quota for project %s/%s from %s to %s to satisfy constraint %q",
				serviceType, res.Name, domain.Name, project.Name,
				limes.ValueWithUnit{Value: *res.Quota, Unit: resInfo.Unit},
				limes.ValueWithUnit{Value: constrainedQuota, Unit: resInfo.Unit},
				constraint.String(),
			)
			res.Quota = &constrainedQuota
		}
	}
}
