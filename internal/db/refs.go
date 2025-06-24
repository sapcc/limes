// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

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

// ClusterServiceID is an ID into the cluster_services table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ClusterServiceID int64

// ClusterResourceID is an ID into the cluster_resources table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ClusterResourceID int64

// ClusterAZResourceID is an ID into the cluster_az_resources table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ClusterAZResourceID int64

// ClusterRateID is an ID into the cluster_rates table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ClusterRateID int64

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

// ProjectRateID is an ID into the project_rates table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ProjectRateID int64

// ProjectCommitmentID is an ID into the project_commitments table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ProjectCommitmentID int64

// ProjectCommitmentUUID is a UUID into the project_commitments table. This typedef is
// used to distinguish these UUIDs from UUIDs of other tables or raw string values.
type ProjectCommitmentUUID string

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
