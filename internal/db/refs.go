// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

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

// AZResourceID is an ID into the az_resources table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type AZResourceID int64

// RateID is an ID into the rates table. This typedef is
// used to distinguish these IDs from IDs of other tables or raw int64 values.
type RateID int64

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
