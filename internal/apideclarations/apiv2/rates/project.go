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

// ProjectGetResponse is the response type for GET /rates/v2/projects(/:project_id)?.
// It contains the latest state of all dynamic rate data for one or many projects.
// The data comes directly from the project level.
type ProjectGetResponse struct {
	// InfoReport is what GET /rates/v2/info would report.
	// It is only returned when the respective query option with=info is set.
	InfoReport    Option[InfoReport]                `json:"service_info,omitzero"`
	DomainReports map[string]ProjectsByDomainReport `json:"project_reports"`
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

// ProjectCategoryReport groups rates into categories, which are defined in the config.
// It appears in [ProjectServiceReport].
type ProjectCategoryReport struct {
	Rates map[liquid.RateName]ProjectRateReport `json:"rates"`
}

// ProjectRateReport contains data for one rate.
// It appears in [ProjectCategoryReport].
type ProjectRateReport struct {
	// UsageAsBigint is a big.Int that has to be serialized as string.
	UsageAsBigint string `json:"usage_as_bigint"`
	// ProjectLimit and ProjectWindow can be set by a project admin.
	// They are only returned when the respective query option with=constraints is set.
	ProjectLimit  Option[uint64]            `json:"project_limit,omitzero"`
	ProjectWindow Option[limesrates.Window] `json:"project_window,omitzero"`
}
