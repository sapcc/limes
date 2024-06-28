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
	Services ProjectServiceReports `json:"services"`
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
	// Several fields are pointers to values to enable precise control over which fields are rendered in output.
	ResourceInfo
	QuotaDistributionModel QuotaDistributionModel   `json:"quota_distribution_model,omitempty"`
	CommitmentConfig       *CommitmentConfiguration `json:"commitment_config,omitempty"`
	// PerAZ is only rendered by Limes when the v2 API feature preview is enabled.
	PerAZ         ProjectAZResourceReports `json:"per_az,omitempty"`
	Quota         *uint64                  `json:"quota,omitempty"`
	UsableQuota   *uint64                  `json:"usable_quota,omitempty"`
	MaxQuota      *uint64                  `json:"max_quota,omitempty"` // refers to max_quota constraint maintained via API
	Usage         uint64                   `json:"usage"`
	PhysicalUsage *uint64                  `json:"physical_usage,omitempty"`
	BackendQuota  *int64                   `json:"backend_quota,omitempty"`
	Subresources  json.RawMessage          `json:"subresources,omitempty"`
}

// ProjectAZResourceReport is a substructure of ProjectResourceReport containing
// quota and usage data for a single resource in an availability zone.
//
// This type is part of the v2 API feature preview.
type ProjectAZResourceReport struct {
	Quota *uint64 `json:"quota,omitempty"`
	// The keys for these maps must be commitment durations as accepted
	// by func ParseCommitmentDuration. We cannot use type CommitmentDuration
	// directly here because Go does not allow struct types as map keys.
	Committed          map[string]uint64 `json:"committed,omitempty"`
	PendingCommitments map[string]uint64 `json:"pending_commitments,omitempty"`
	PlannedCommitments map[string]uint64 `json:"planned_commitments,omitempty"`
	Usage              uint64            `json:"usage"`
	PhysicalUsage      *uint64           `json:"physical_usage,omitempty"`
	HistoricalUsage    *HistoricalReport `json:"historical_usage,omitempty"`
	Subresources       json.RawMessage   `json:"subresources,omitempty"`
}

type HistoricalReport struct {
	MinUsage uint64             `json:"min_usage,omitempty"`
	MaxUsage uint64             `json:"max_usage,omitempty"`
	Duration CommitmentDuration `json:"duration,omitempty"`
}

// ProjectServiceReports provides fast lookup of services using a map, but serializes
// to JSON as a list.
type ProjectServiceReports map[limes.ServiceType]*ProjectServiceReport

// ProjectResourceReports provides fast lookup of resources using a map, but serializes
// to JSON as a list.
type ProjectResourceReports map[ResourceName]*ProjectResourceReport

// ProjectAZResourceReport is a substructure of ProjectResourceReport that breaks
// down quota and usage data for a single resource by availability zone.
type ProjectAZResourceReports map[limes.AvailabilityZone]*ProjectAZResourceReport
