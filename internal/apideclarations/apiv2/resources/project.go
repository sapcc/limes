// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package resourcesv2

import (
	"encoding/json"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	"github.com/sapcc/limes/internal/db"

	. "go.xyrillian.de/gg/option"
)

// ProjectGetResponse is the response type for GET /resources/v2/projects(/:project_id)?.
// It contains the latest state of all dynamic resource data for one or many projects.
// The data comes directly from the project level.
type ProjectGetResponse struct {
	// InfoReport is what GET /resources/v2/info would report.
	// It is only returned when the respective query option with=info is set.
	InfoReport    Option[InfoReport]                `json:"service_info,omitzero"`
	DomainReports map[string]ProjectsByDomainReport `json:"domain_reports"`
}

// ProjectsByDomainReport groups all projects for one domain.
// It appears in [ProjectGetResponse].
type ProjectsByDomainReport struct {
	ProjectReports map[string]ProjectReport `json:"project_reports"`
}

// ProjectReport groups all services for one project into areas,
// which are defined in the config.
// It appears in [ProjectGetResponse].
type ProjectReport struct {
	common.ProjectInfo
	Areas map[string]ProjectAreaReport `json:"service_areas"`
}

// ProjectAreaReport contains data for one area.
// It appears in [ProjectReport].
type ProjectAreaReport struct {
	Services map[db.ServiceType]ProjectServiceReport `json:"services"`
}

// ProjectServiceReport contains data for one service.
// It appears in [ProjectAreaReport].
type ProjectServiceReport struct {
	// ScrapedAt expresses when this service/project-combination was last successfully queried from the backend.
	// It is only returned when the respective query option with=timing is set.
	ScrapedAt  Option[limes.UnixEncodedTime]                 `json:"scraped_at,omitzero"`
	Categories map[liquid.CategoryName]ProjectCategoryReport `json:"categories"`
}

// ProjectCategoryReport groups resources into categories, which are defined in the config.
// It appears in [ProjectServiceReport].
type ProjectCategoryReport struct {
	Resources map[liquid.ResourceName]ProjectResourceReport `json:"resources"`
}

// ProjectResourceReport contains data for one resource.
// It appears in [ProjectCategoryReport].
type ProjectResourceReport struct {
	// TODO: note which AZs can surface here, depending on topology and various scenarios.
	AvailabilityZones map[limes.AvailabilityZone]ProjectAvailabilityZoneReport `json:"availability_zones"`
	// MaxQuota and ForbidAutogrowth can be set by a project admin.
	// They are only returned when the respective query option with=constraints is set.
	MaxQuota         Option[uint64] `json:"max_quota,omitzero"` // refers to max_quota constraint maintained via API
	ForbidAutogrowth bool           `json:"forbid_autogrowth,omitempty"`
}

// ProjectAvailabilityZoneReport contains the data for an availability zone.
// It appears in [ProjectResourceReport].
type ProjectAvailabilityZoneReport struct {
	// Usage is the usage in this project as reported by the service.
	Usage uint64 `json:"usage"`
	// Quota is the quota value which was calculated in Limes for this availability zone.
	// It might differ to the value set in the service, when the backend of the service was not yet updated.
	// It is only returned when the resource has quota.
	Quota Option[uint64] `json:"quota,omitzero"`
	// CommittedConfirmed is the sum of committed and confirmed amounts across projects grouped by their CommitmentDuration.
	// It is only returned when the respective query option with=commitment_stats is set.
	CommittedConfirmed map[limesresources.CommitmentDuration]uint64 `json:"committed_confirmed,omitempty"`
	// PhysicalUsage is usage of physical hardware resources which results from the Usage.
	// It is only returned when the service reports it.
	PhysicalUsage Option[uint64] `json:"physical_usage,omitzero"`
	// HistoricalUsage are the statistics on which the automatic quota distribution is based on.
	// It is only returned when automatic quota distribution is configured for this resource.
	HistoricalUsage Option[ProjectHistoricalReport] `json:"historical_usage,omitzero"`
	// Subresources is formatted as json.RawMessage for convenience for reading from the database.
	// The content will be a marshalled []liquid.Subresource.
	// It is only returned when the respective query option with=subresources is set.
	Subresources json.RawMessage `json:"subresources,omitempty"`
}

// ProjectHistoricalReport contains historical summed data for an availability zone.
// It appears in [ProjectAvailabilityZoneReport].
type ProjectHistoricalReport struct {
	MinUsage uint64        `json:"min_usage,omitempty"`
	MaxUsage uint64        `json:"max_usage,omitempty"`
	Duration time.Duration `json:"duration"`
}
