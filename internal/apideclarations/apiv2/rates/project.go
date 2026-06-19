// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package ratesv2

import (
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	"github.com/sapcc/limes/internal/db"

	. "go.xyrillian.de/gg/option"
)

// ProjectGetResponse is the response type for GET /rates/v2/projects(/:project_uuid)?.
// It contains the latest state of all dynamic rate data for one or many projects.
// The data comes directly from the project level.
type ProjectGetResponse struct {
	// InfoReport is what GET /rates/v2/info would report.
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

// ProjectCategoryReport groups rates into categories, which are defined in the config.
// It appears in [ProjectServiceReport].
type ProjectCategoryReport struct {
	Rates map[liquid.RateName]ProjectRateReport `json:"rates"`
}

// ProjectRateReport contains data for one rate.
// It appears in [ProjectCategoryReport].
type ProjectRateReport struct {
	// UsageAsBigint is a big.Int that has to be serialized as string.
	// If the rate does not report usage and has no ProjectLimit, the rate is omitted from the ProjectCategoryReport.Rates map.
	// If the rate does not report usage and has a ProjectLimit, this will be None.
	UsageAsBigint Option[string] `json:"usage_as_bigint,omitzero"`
	// ProjectLimit and ProjectWindow can be set by a project admin.
	// If the rate does not have a local project rate limit, this will be None.
	ProjectLimit  Option[uint64]            `json:"project_limit,omitzero"`
	ProjectWindow Option[limesrates.Window] `json:"project_window,omitzero"`
}
