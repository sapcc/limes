// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"strings"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
)

// ServiceType identifies a backend service that can have resources or rates.
//
// This type is used for the service type columns that appear in the DB.
// It is legally distinct from `limes.ServiceType` to ensure that the
// ResourceBehavior.IdentityInV1API mapping is applied when converting between
// API-level and DB-level identifiers.
type ServiceType string

// ServiceID is an ID into the services table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ServiceID int64

// ResourceID is an ID into the resources table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type ResourceID int64

// ResourcePath is a concatenation of db.ServiceType + "/" + liquid.ResourceName.
// This typedef is used to quickly retrieve the single components from the path.
type ResourcePath string

// ServiceType extracts the service type component from the ResourcePath.
func (r ResourcePath) ServiceType() ServiceType {
	return ServiceType(strings.SplitN(string(r), "/", 2)[0])
}

// ResourceName extracts the resource name component from the ResourcePath.
func (r ResourcePath) ResourceName() liquid.ResourceName {
	return liquid.ResourceName(strings.SplitN(string(r), "/", 2)[1])
}

// NewResourcePath constructs a ResourcePath from a service type and resource name.
func NewResourcePath(serviceType ServiceType, resourceName liquid.ResourceName) ResourcePath {
	return ResourcePath(string(serviceType) + "/" + string(resourceName))
}

// AZResourceID is an ID into the az_resources table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type AZResourceID int64

// AZResourcePath is a concatenation of db.ServiceType + "/" + liquid.ResourceName
// + "/" + limes.AvailabilityZone. This typedef is used to quickly retrieve the
// single components from the path.
type AZResourcePath string

// ServiceType extracts the service type component from the AZResourcePath.
func (a AZResourcePath) ServiceType() ServiceType {
	return ServiceType(strings.SplitN(string(a), "/", 3)[0])
}

// ResourceName extracts the resource name component from the AZResourcePath.
func (a AZResourcePath) ResourceName() liquid.ResourceName {
	return liquid.ResourceName(strings.SplitN(string(a), "/", 3)[1])
}

// AvailabilityZone extracts the availability zone component from the AZResourcePath.
func (a AZResourcePath) AvailabilityZone() limes.AvailabilityZone {
	return limes.AvailabilityZone(strings.SplitN(string(a), "/", 3)[2])
}

// NewAZResourcePath constructs an AZResourcePath from a service type, resource name, and availability zone.
func NewAZResourcePath(serviceType ServiceType, resourceName liquid.ResourceName, availabilityZone limes.AvailabilityZone) AZResourcePath {
	return AZResourcePath(string(serviceType) + "/" + string(resourceName) + "/" + string(availabilityZone))
}

// RateID is an ID into the rates table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type RateID int64

// RatePath is a concatenation of db.ServiceType + "/" + liquid.RateName.
// This typedef is used to quickly retrieve the single components from the path.
type RatePath string

// ServiceType extracts the service type component from the RatePath.
func (r RatePath) ServiceType() ServiceType {
	return ServiceType(strings.SplitN(string(r), "/", 2)[0])
}

// RateName extracts the rate name component from the RatePath.
func (r RatePath) RateName() liquid.RateName {
	return liquid.RateName(strings.SplitN(string(r), "/", 2)[1])
}

// NewRatePath constructs a RatePath from a service type and rate name.
func NewRatePath(serviceType ServiceType, rateName liquid.RateName) RatePath {
	return RatePath(string(serviceType) + "/" + string(rateName))
}

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

// CategoryID is an ID into the categories table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type CategoryID int64
