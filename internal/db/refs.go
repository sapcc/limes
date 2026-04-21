// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

import (
	_ "database/sql" // only used in docstring links
	"database/sql/driver"
	"fmt"
	"strings"

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
// We form a struct type of it, so that we can throw proper errors when receiving
// malformed strings from the database.
type ResourcePath struct {
	ServiceType  ServiceType
	ResourceName liquid.ResourceName
}

// String implements the [fmt.Stringer] interface.
func (r ResourcePath) String() string {
	return strings.Join([]string{string(r.ServiceType), string(r.ResourceName)}, "/")
}

// Value implements the [driver.Valuer] interface.
func (r ResourcePath) Value() (driver.Value, error) {
	return r.String(), nil
}

// Scan implements the [sql.Scanner] interface.
func (r *ResourcePath) Scan(src any) error {
	srcStr, ok := src.(string)
	if !ok {
		return fmt.Errorf("invalid input type for %T.Scan(): %T", r, src)
	}
	first, second, ok := strings.Cut(srcStr, "/")
	if !ok || strings.Contains(second, "/") {
		return fmt.Errorf("invalid value for %T: %q", r, srcStr)
	}
	r.ServiceType = ServiceType(first)
	r.ResourceName = liquid.ResourceName(second)
	return nil
}

// AZResourceID is an ID into the az_resources table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type AZResourceID int64

// AZResourcePath is a concatenation of db.ServiceType + "/" + liquid.ResourceName +
// "/" + liquid.AvailabilityZone. We form a struct type of it, so that we can
// throw proper errors when receiving malformed strings from the database.
type AZResourcePath struct {
	ServiceType      ServiceType
	ResourceName     liquid.ResourceName
	AvailabilityZone liquid.AvailabilityZone
}

// String implements the [fmt.Stringer] interface.
func (r AZResourcePath) String() string {
	return strings.Join([]string{string(r.ServiceType), string(r.ResourceName), string(r.AvailabilityZone)}, "/")
}

// Value implements the [driver.Valuer] interface.
func (r AZResourcePath) Value() (driver.Value, error) {
	return r.String(), nil
}

// Scan implements the [sql.Scanner] interface.
func (r *AZResourcePath) Scan(src any) error {
	srcStr, ok := src.(string)
	if !ok {
		return fmt.Errorf("invalid input type for %T.Scan(): %T", r, src)
	}
	parts := strings.SplitN(srcStr, "/", 3)
	if len(parts) != 3 || strings.Contains(parts[2], "/") {
		return fmt.Errorf("invalid value for %T: %q", r, srcStr)
	}
	r.ServiceType = ServiceType(parts[0])
	r.ResourceName = liquid.ResourceName(parts[1])
	r.AvailabilityZone = liquid.AvailabilityZone(parts[2])
	return nil
}

// RateID is an ID into the rates table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type RateID int64

// RatePath is a concatenation of db.ServiceType + "/" + liquid.RateName.
// We form a struct type of it, so that we can throw proper errors when receiving
// malformed strings from the database.
type RatePath struct {
	ServiceType ServiceType
	RateName    liquid.RateName
}

// String implements the [fmt.Stringer] interface.
func (r RatePath) String() string {
	return strings.Join([]string{string(r.ServiceType), string(r.RateName)}, "/")
}

// Value implements the [driver.Valuer] interface.
func (r RatePath) Value() (driver.Value, error) {
	return r.String(), nil
}

// Scan implements the [sql.Scanner] interface.
func (r *RatePath) Scan(src any) error {
	srcStr, ok := src.(string)
	if !ok {
		return fmt.Errorf("invalid input type for %T.Scan(): %T", r, src)
	}
	first, second, ok := strings.Cut(srcStr, "/")
	if !ok || strings.Contains(second, "/") {
		return fmt.Errorf("invalid value for %T: %q", r, srcStr)
	}
	r.ServiceType = ServiceType(first)
	r.RateName = liquid.RateName(second)
	return nil
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
