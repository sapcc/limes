// SPDX-FileCopyrightText: 2018-2020 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package limes

import "github.com/sapcc/go-api-declarations/liquid"

// ClusterInfo contains the metadata for a cluster that appears in both
// resource data and rate data reports.
type ClusterInfo struct {
	ID string `json:"id"`
}

// DomainInfo contains the metadata for a domain that appears in both resource
// data and rate data reports.
type DomainInfo struct {
	UUID string `json:"id"`
	Name string `json:"name"`
}

// ProjectInfo contains the metadata for a project that appears in both
// resource data and rate data reports.
type ProjectInfo struct {
	UUID       string `json:"id"`
	Name       string `json:"name"`
	ParentUUID string `json:"parent_id"`
}

// ServiceType identifies a backend service that can have resources or rates.
// This type is used to distinguish service types from other types of string
// values in function signatures.
type ServiceType string

// ServiceInfo contains the metadata for a backend service that appears in both
// resource data and rate data reports.
type ServiceInfo struct {
	// Type returns the service type that the backend service for this
	// plugin implements. This string must be identical to the type string from
	// the Keystone service catalog.
	Type ServiceType `json:"type"`
	// ProductName returns the name of the product that is the reference
	// implementation for this service. For example, ProductName = "nova" for
	// Type = "compute".
	ProductName string `json:"-"`
	// Area is a hint that UIs can use to group similar services.
	Area string `json:"area"`
}

// AvailabilityZone is the name of an availability zone.
// Some special values are enumerated at the original declaration site.
type AvailabilityZone = liquid.AvailabilityZone

const (
	// AvailabilityZoneAny marks values that are not bound to a specific AZ.
	AvailabilityZoneAny AvailabilityZone = liquid.AvailabilityZoneAny
	// AvailabilityZoneUnknown marks values that are bound to an unknown AZ.
	AvailabilityZoneUnknown AvailabilityZone = liquid.AvailabilityZoneUnknown
)
