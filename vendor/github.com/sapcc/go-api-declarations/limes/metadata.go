/*******************************************************************************
*
* Copyright 2018-2020 SAP SE
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

package limes

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
// Some special values are enumerated below.
type AvailabilityZone string

const (
	// AvailabilityZoneAny marks values that are not bound to a specific AZ.
	AvailabilityZoneAny AvailabilityZone = "any"
	// AvailabilityZoneUnknown marks values that are bound to an unknown AZ.
	AvailabilityZoneUnknown AvailabilityZone = "unknown"
)
