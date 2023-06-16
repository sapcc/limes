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

package limesresources

import "github.com/sapcc/go-api-declarations/limes"

// DomainReport contains aggregated data about resource usage in a domain.
// It is returned by GET requests on domains.
type DomainReport struct {
	limes.DomainInfo
	Services DomainServiceReports `json:"services"`
}

// DomainServiceReport is a substructure of DomainReport containing data for
// a single backend service.
type DomainServiceReport struct {
	limes.ServiceInfo
	Resources    DomainResourceReports  `json:"resources"`
	MaxScrapedAt *limes.UnixEncodedTime `json:"max_scraped_at,omitempty"`
	MinScrapedAt *limes.UnixEncodedTime `json:"min_scraped_at,omitempty"`
}

// DomainResourceReport is a substructure of DomainReport containing data for
// a single resource.
type DomainResourceReport struct {
	//Several fields are pointers to values to enable precise control over which fields are rendered in output.
	ResourceInfo
	QuotaDistributionModel QuotaDistributionModel `json:"quota_distribution_model,omitempty"`
	DomainQuota            *uint64                `json:"quota,omitempty"`
	ProjectsQuota          *uint64                `json:"projects_quota,omitempty"`
	Usage                  uint64                 `json:"usage"`
	BurstUsage             uint64                 `json:"burst_usage,omitempty"`
	PhysicalUsage          *uint64                `json:"physical_usage,omitempty"`
	BackendQuota           *uint64                `json:"backend_quota,omitempty"`
	InfiniteBackendQuota   *bool                  `json:"infinite_backend_quota,omitempty"`
	Scaling                *ScalingBehavior       `json:"scales_with,omitempty"`
	//Annotations may contain arbitrary metadata that was configured for this
	//resource in this scope by Limes' operator.
	Annotations map[string]any `json:"annotations,omitempty"`
}

// DomainServiceReports provides fast lookup of services using a map, but serializes
// to JSON as a list.
type DomainServiceReports map[string]*DomainServiceReport

// DomainResourceReports provides fast lookup of resources using a map, but serializes
// to JSON as a list.
type DomainResourceReports map[string]*DomainResourceReport
