// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"database/sql"
	"maps"
	"slices"

	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

var projectRateReportQuery = sqlext.SimplifyWhitespace(`
	SELECT d.uuid, d.name, p.uuid, p.name, p.parent_uuid, pra.rate_id, pra.usage_as_bigint, pra.rate_limit, pra.window_ns
	FROM project_rates pra
	JOIN projects p
	ON p.id = pra.project_id
	JOIN domains d
	ON d.id = p.domain_id
	WHERE {{pra.rate_id = ANY($rate_id)}}
	AND {{d.id = $domain_id}}
	AND {{p.id = $project_id}}
`)

// GetProjectRates returns a ratesv2.ProjectGetResponse.
func GetProjectRates(cluster *core.Cluster, token *gopherpolicy.Token, filter Filter, options common.ProjectRateReportOpts, scope Scope) (ratesv2.ProjectGetResponse, error) {
	result := &ratesv2.ProjectGetResponse{}

	// fill info report
	if options.WithInfo {
		infoReport, err := GetRatesInfo(cluster, token, filter)
		if err != nil {
			return *result, err
		}
		result.InfoReport = Some(infoReport)
	}

	// the result will have all rates without usage --> we will filter later
	query, args := filter.ExpandServiceFilters(projectRateReportQuery)
	query, args = scope.ExpandScopeFilters(query, args...)
	err := sqlext.ForeachRow(cluster.DB, query, args, func(rows *sql.Rows) error {
		var (
			domainUUID        string
			domainName        string
			projectUUID       string
			projectName       string
			projectParentUUID string
			rateID            db.RateID
			usageAsBigint     string
			projectLimit      Option[uint64]
			projectWindow     Option[limesrates.Window]
		)
		err := rows.Scan(&domainUUID, &domainName, &projectUUID, &projectName, &projectParentUUID,
			&rateID, &usageAsBigint, &projectLimit, &projectWindow)
		if err != nil {
			return err
		}
		setInProjectRateReport(filter, cluster, result, rateID, common.ProjectMetadata{
			UUID:       projectUUID,
			Name:       projectName,
			ParentUUID: projectParentUUID,
			DomainInfo: common.DomainMetadata{
				UUID: domainUUID,
				Name: domainName,
			},
		}, ratesv2.ProjectRateReport{
			UsageAsBigint: Some(usageAsBigint), // note: the database has a non-null constraint here, make None when setting
			ProjectLimit:  projectLimit,
			ProjectWindow: projectWindow,
		})
		return nil
	})

	if err != nil {
		return *result, err
	}
	return *result, nil
}

// setInProjectRateReport creates or iterates higher level structs on the way to the nested
// location of the db.RateID in the report and assigns the value for ratesv2.ProjectRateReport.
// If this rate should not get set because it does not have usage, this is a no-op.
func setInProjectRateReport(filter Filter, cluster *core.Cluster, report *ratesv2.ProjectGetResponse, rateID db.RateID, project common.ProjectMetadata, value ratesv2.ProjectRateReport) {
	services := filter.GetServices()
	categories := filter.GetCategories()

	for _, serviceType := range slices.Sorted(maps.Keys(services)) {
		rates, _ := filter.GetRatesForType(serviceType) // can have no resources
		for _, rateName := range slices.Sorted(maps.Keys(rates)) {
			rate := rates[rateName]
			if rate.ID != rateID {
				continue
			}
			if !rate.HasUsage && value.ProjectLimit.IsNone() && value.ProjectWindow.IsNone() {
				return
			}
			// note: the database has a non-null constraint here, so we correct this after the fact
			if !rate.HasUsage {
				value.UsageAsBigint = None[string]()
			}

			config := cluster.Config.Liquids[serviceType]
			area := config.Area
			// defense in depth: config should be in sync with serviceInfo
			if area == "" {
				return
			}

			// check domain level (might be uninitialized)
			if report.DomainReports == nil {
				report.DomainReports = make(map[string]ratesv2.ProjectsByDomainReport)
			}
			if _, exists := report.DomainReports[project.DomainInfo.UUID]; !exists {
				report.DomainReports[project.DomainInfo.UUID] = ratesv2.ProjectsByDomainReport{
					ProjectReports: make(map[string]ratesv2.ProjectReport),
				}
			}
			domainReport := report.DomainReports[project.DomainInfo.UUID]

			// check project level
			if _, exists := domainReport.ProjectReports[project.UUID]; !exists {
				domainReport.ProjectReports[project.UUID] = ratesv2.ProjectReport{
					ProjectMetadata: project,
					Areas:           make(map[string]ratesv2.ProjectAreaReport),
				}
			}
			projectReport := domainReport.ProjectReports[project.UUID]

			// check area level
			if _, exists := projectReport.Areas[area]; !exists {
				projectReport.Areas[area] = ratesv2.ProjectAreaReport{Services: make(map[db.ServiceType]ratesv2.ProjectServiceReport)}
			}
			areaReport := projectReport.Areas[area]

			// check service level
			if _, exists := areaReport.Services[serviceType]; !exists {
				areaReport.Services[serviceType] = ratesv2.ProjectServiceReport{Categories: make(map[liquid.CategoryName]ratesv2.ProjectCategoryReport)}
			}
			serviceReport := areaReport.Services[serviceType]

			// check category level
			category := liquid.CategoryName(serviceType)
			if categoryID, exists := rate.CategoryID.Unpack(); exists {
				category = categories[categoryID].Name
			}
			if _, exists := serviceReport.Categories[category]; !exists {
				serviceReport.Categories[category] = ratesv2.ProjectCategoryReport{Rates: make(map[liquid.RateName]ratesv2.ProjectRateReport)}
			}
			categoryReport := serviceReport.Categories[category]

			// check rate level
			categoryReport.Rates[rate.Name] = value
			return
		}
	}
}
