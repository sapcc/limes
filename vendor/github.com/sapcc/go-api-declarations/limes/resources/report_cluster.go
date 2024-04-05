/*******************************************************************************
*
* Copyright 2017-2020 SAP SE
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

package limesresources

import (
	"encoding/json"

	"github.com/sapcc/go-api-declarations/limes"
)

// ClusterReport contains aggregated data about resource usage in a cluster.
// It is returned by GET endpoints for clusters.
type ClusterReport struct {
	limes.ClusterInfo
	Services     ClusterServiceReports  `json:"services"`
	MaxScrapedAt *limes.UnixEncodedTime `json:"max_scraped_at,omitempty"`
	MinScrapedAt *limes.UnixEncodedTime `json:"min_scraped_at,omitempty"`
}

// ClusterServiceReport is a substructure of ClusterReport containing data for
// a single backend service.
type ClusterServiceReport struct {
	limes.ServiceInfo
	Resources    ClusterResourceReports `json:"resources"`
	MaxScrapedAt *limes.UnixEncodedTime `json:"max_scraped_at,omitempty"`
	MinScrapedAt *limes.UnixEncodedTime `json:"min_scraped_at,omitempty"`
}

// ClusterResourceReport is a substructure of ClusterReport containing data for
// a single resource.
type ClusterResourceReport struct {
	// Several fields are pointers to values to enable precise control over which fields are rendered in output.
	ResourceInfo
	QuotaDistributionModel QuotaDistributionModel   `json:"quota_distribution_model,omitempty"`
	CommitmentConfig       *CommitmentConfiguration `json:"commitment_config,omitempty"`
	Capacity               *uint64                  `json:"capacity,omitempty"`
	RawCapacity            *uint64                  `json:"raw_capacity,omitempty"`
	// PerAZ is only rendered by Limes when the v2 API feature preview is enabled.
	// In this case, CapacityPerAZ will be omitted.
	PerAZ         ClusterAZResourceReports       `json:"per_az,omitempty"`
	CapacityPerAZ ClusterAvailabilityZoneReports `json:"per_availability_zone,omitempty"`
	DomainsQuota  *uint64                        `json:"domains_quota,omitempty"`
	Usage         uint64                         `json:"usage"`
	BurstUsage    uint64                         `json:"burst_usage,omitempty"`
	PhysicalUsage *uint64                        `json:"physical_usage,omitempty"`
	Subcapacities json.RawMessage                `json:"subcapacities,omitempty"`
}

// ClusterAvailabilityZoneReport is a substructure of ClusterResourceReport containing
// capacity and usage data for a single resource in an availability zone.
type ClusterAvailabilityZoneReport struct {
	Name        limes.AvailabilityZone `json:"name"`
	Capacity    uint64                 `json:"capacity"`
	RawCapacity uint64                 `json:"raw_capacity,omitempty"`
	Usage       uint64                 `json:"usage,omitempty"`
}

// ClusterAZResourceReport is a substructure of ClusterResourceReport containing
// capacity and usage data for a single resource in an availability zone.
//
// This type is part of the v2 API feature preview.
type ClusterAZResourceReport struct {
	Capacity    uint64 `json:"capacity"`
	RawCapacity uint64 `json:"raw_capacity,omitempty"`
	// Usage is what the backend reports. This is only shown if the backend does indeed report a summarized cluster-wide usage level.
	//TODO: rename this to "backend_usage" in v2
	Usage *uint64 `json:"usage,omitempty"`
	// ProjectsUsage is the aggregate of the usage across all projects, as reported by the backend on the project level.
	//TODO: rename this to "usage" in v2 (to be consistent with domain and project level)
	ProjectsUsage uint64 `json:"projects_usage,omitempty"`
	// The keys for these maps must be commitment durations as accepted
	// by func ParseCommitmentDuration. We cannot use type CommitmentDuration
	// directly here because Go does not allow struct types as map keys.
	Committed          map[string]uint64 `json:"committed,omitempty"`
	UnusedCommitments  uint64            `json:"unused_commitments,omitempty"`
	PendingCommitments map[string]uint64 `json:"pending_commitments,omitempty"`
	PlannedCommitments map[string]uint64 `json:"planned_commitments,omitempty"`
	// PhysicalUsage is collected per project and then aggregated, same as ProjectsUsage.
	PhysicalUsage *uint64         `json:"physical_usage,omitempty"`
	Subcapacities json.RawMessage `json:"subcapacities,omitempty"`
}

// ClusterServiceReports provides fast lookup of services by service type, but
// serializes to JSON as a list.
type ClusterServiceReports map[limes.ServiceType]*ClusterServiceReport

// ClusterResourceReports provides fast lookup of resources by resource name,
// but serializes to JSON as a list.
type ClusterResourceReports map[ResourceName]*ClusterResourceReport

// ClusterAvailabilityZoneReports provides fast lookup of availability zones
// using a map, but serializes to JSON as a list.
type ClusterAvailabilityZoneReports map[limes.AvailabilityZone]*ClusterAvailabilityZoneReport

// ClusterAZResourceReport is a substructure of ClusterResourceReport that breaks
// down capacity and usage data for a single resource by availability zone.
type ClusterAZResourceReports map[limes.AvailabilityZone]*ClusterAZResourceReport
