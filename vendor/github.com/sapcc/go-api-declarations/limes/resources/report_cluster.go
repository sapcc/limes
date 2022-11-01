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
	//Several fields are pointers to values to enable precise control over which fields are rendered in output.
	ResourceInfo
	QuotaDistributionModel QuotaDistributionModel         `json:"quota_distribution_model,omitempty"`
	Capacity               *uint64                        `json:"capacity,omitempty"`
	RawCapacity            *uint64                        `json:"raw_capacity,omitempty"`
	CapacityPerAZ          ClusterAvailabilityZoneReports `json:"per_availability_zone,omitempty"`
	DomainsQuota           *uint64                        `json:"domains_quota,omitempty"`
	Usage                  uint64                         `json:"usage"`
	BurstUsage             uint64                         `json:"burst_usage,omitempty"`
	PhysicalUsage          *uint64                        `json:"physical_usage,omitempty"`
	Subcapacities          json.RawMessage                `json:"subcapacities,omitempty"`
}

// ClusterAvailabilityZoneReport is a substructure of ClusterResourceReport containing
// capacity and usage data for a single resource in an availability zone.
type ClusterAvailabilityZoneReport struct {
	Name        string `json:"name"`
	Capacity    uint64 `json:"capacity"`
	RawCapacity uint64 `json:"raw_capacity,omitempty"`
	Usage       uint64 `json:"usage,omitempty"`
}

// ClusterServiceReports provides fast lookup of services by service type, but
// serializes to JSON as a list.
type ClusterServiceReports map[string]*ClusterServiceReport

// ClusterResourceReports provides fast lookup of resources by resource name,
// but serializes to JSON as a list.
type ClusterResourceReports map[string]*ClusterResourceReport

// ClusterAvailabilityZoneReports provides fast lookup of availability zones
// using a map, but serializes to JSON as a list.
type ClusterAvailabilityZoneReports map[string]*ClusterAvailabilityZoneReport
