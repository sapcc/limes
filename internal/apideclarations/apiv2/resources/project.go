// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package resourcesv2

import (
	"encoding/json"

	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	"github.com/sapcc/limes/internal/db"

	. "go.xyrillian.de/gg/option"
)

// ProjectGetResponse is the response type for GET /resources/v2/projects(/:project_uuid)?.
// It contains the latest state of all dynamic resource data for one or many projects.
// The data comes directly from the project level.
type ProjectGetResponse struct {
	// InfoReport is what GET /resources/v2/info would report.
	// It is only returned when the respective query option with=info is set.
	InfoReport    Option[InfoReport]                `json:"info,omitzero"`
	DomainReports map[string]ProjectsByDomainReport `json:"domains"`
}

// ProjectsByDomainReport groups all projects for one domain.
// It appears in [ProjectGetResponse].
type ProjectsByDomainReport struct {
	ProjectReports map[string]ProjectReport `json:"projects"`
}

// ProjectReport groups all services for one project into areas,
// which are defined in the config.
// It appears in [ProjectGetResponse].
type ProjectReport struct {
	common.ProjectMetadata
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
	// ScrapedAt is the most recent time at which usage data for this service within this
	// project was successfully collected from the backend, or None if no usage data has been collected yet.
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
	MaxQuota         Option[uint64] `json:"max_quota,omitzero"`
	ForbidAutogrowth Option[bool]   `json:"forbid_autogrowth,omitzero"`
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
	// Committed is the sum of committed amounts grouped by their Status and then CommitmentDuration.
	// It is populated sparsely, so that only status and durations which exist will appear.
	// It is only returned when the respective query option with=commitment_stats is set.
	Committed map[liquid.CommitmentStatus]map[limesresources.CommitmentDuration]uint64 `json:"committed,omitempty"`
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
	MinUsage uint64            `json:"min_usage"`
	MaxUsage uint64            `json:"max_usage"`
	Duration limesrates.Window `json:"duration"`
}
