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

import (
	"encoding/json"

	"github.com/sapcc/go-api-declarations/limes"
)

// ProjectReport contains all data about resource usage in a project.
type ProjectReport struct {
	limes.ProjectInfo
	Bursting *ProjectBurstingInfo  `json:"bursting,omitempty"`
	Services ProjectServiceReports `json:"services"`
}

// ProjectBurstingInfo is a substructure of ProjectReport containing information about
// quota bursting. (It is omitted if bursting is not supported for the project's
// cluster.)
type ProjectBurstingInfo struct {
	Enabled    bool               `json:"enabled"`
	Multiplier BurstingMultiplier `json:"multiplier"`
}

// ProjectServiceReport is a substructure of ProjectReport containing data for
// a single backend service.
type ProjectServiceReport struct {
	limes.ServiceInfo
	Resources ProjectResourceReports `json:"resources"`
	ScrapedAt *limes.UnixEncodedTime `json:"scraped_at,omitempty"`
}

// ProjectResourceReport is a substructure of ProjectReport containing data for
// a single resource.
type ProjectResourceReport struct {
	//Several fields are pointers to values to enable precise control over which fields are rendered in output.
	ResourceInfo
	QuotaDistributionModel QuotaDistributionModel `json:"quota_distribution_model,omitempty"`
	Quota                  *uint64                `json:"quota,omitempty"`
	DefaultQuota           *uint64                `json:"default_quota,omitempty"`
	UsableQuota            *uint64                `json:"usable_quota,omitempty"`
	Usage                  uint64                 `json:"usage"`
	BurstUsage             uint64                 `json:"burst_usage,omitempty"`
	PhysicalUsage          *uint64                `json:"physical_usage,omitempty"`
	BackendQuota           *int64                 `json:"backend_quota,omitempty"`
	Subresources           json.RawMessage        `json:"subresources,omitempty"`
	Scaling                *ScalingBehavior       `json:"scales_with,omitempty"`
	//Annotations may contain arbitrary metadata that was configured for this
	//resource in this scope by Limes' operator.
	Annotations map[string]any `json:"annotations,omitempty"`
}

// ProjectServiceReports provides fast lookup of services using a map, but serializes
// to JSON as a list.
type ProjectServiceReports map[string]*ProjectServiceReport

// ProjectResourceReports provides fast lookup of resources using a map, but serializes
// to JSON as a list.
type ProjectResourceReports map[string]*ProjectResourceReport
