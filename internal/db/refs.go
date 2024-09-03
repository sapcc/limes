/******************************************************************************
*
*  Copyright 2024 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package db

import (
	"cmp"

	"github.com/sapcc/go-api-declarations/liquid"
)

// ServiceType identifies a backend service that can have resources or rates.
//
// This type is used for the service type columns that appear in the DB.
// It is legally distinct from `limes.ServiceType` to ensure that the
// ResourceBehavior.IdentityInV1API mapping is applied when converting between
// API-level and DB-level identifiers.
type ServiceType string

// RateName identifies a rate within a service.
//
// This type is used for the resource name columns that appear in the DB.
// It is legally distinct from `limesrates.RateName`, not because we currently
// support API-level renaming, but to make it easier to add it in the future.
type RateName string

// ClusterServiceID is an ID into the cluster_services table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ClusterServiceID int64

// ClusterResourceID is an ID into the cluster_resources table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ClusterResourceID int64

// ClusterAZResourceID is an ID into the cluster_az_resources table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ClusterAZResourceID int64

// DomainID is an ID into the domains table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type DomainID int64

// ProjectID is an ID into the projects table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ProjectID int64

// ProjectServiceID is an ID into the project_services table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ProjectServiceID int64

// ProjectResourceID is an ID into the project_resources table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ProjectResourceID int64

// ProjectAZResourceID is an ID into the project_az_resources table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ProjectAZResourceID int64

// ProjectCommitmentID is an ID into the project_commitments table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ProjectCommitmentID int64

// ResourceRef identifies an individual ProjectResource, DomainResource or ClusterResource.
type ResourceRef[I ~int64] struct {
	ServiceID I                   `db:"service_id"`
	Name      liquid.ResourceName `db:"name"`
}

// CompareResourceRefs is a compare function for ResourceRef (for use with slices.SortFunc etc.)
func CompareResourceRefs[I ~int64](lhs, rhs ResourceRef[I]) int {
	if lhs.ServiceID != rhs.ServiceID {
		return int(rhs.ServiceID - lhs.ServiceID)
	}
	return cmp.Compare(lhs.Name, rhs.Name)
}

// ServiceRef identifies an individual ProjectService, DomainService or ClusterService.
// It appears in APIs when not the entire Service record is needed.
type ServiceRef[I ~int64] struct {
	ID   I
	Type ServiceType
}
