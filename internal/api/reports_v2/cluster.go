// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"database/sql"
	"maps"
	"slices"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/sqlext"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/go-bits/gopherpolicy"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

var clusterReportQuery = sqlext.SimplifyWhitespace(`
	SELECT pra.rate_id, SUM(pra.usage_as_bigint::BIGINT)::TEXT AS usage_as_bigint
	FROM project_rates pra
	WHERE {{pra.rate_id = ANY($rate_id)}}
	GROUP BY pra.rate_id
`)

// GetClusterRates returns a ratesv2.ClusterGetResponse.
func GetClusterRates(cluster *core.Cluster, token *gopherpolicy.Token, filter Filter, options common.ClusterRateReportOpts) (ratesv2.ClusterGetResponse, error) {
	result := &ratesv2.ClusterGetResponse{}

	// fill info report
	if options.WithInfo {
		infoReport, err := GetRatesInfo(cluster, token, filter)
		if err != nil {
			return *result, err
		}
		result.InfoReport = Some(infoReport)
	}

	// the result will have all rates without usage --> we will filter later
	query, args := filter.ExpandServiceFilters(clusterReportQuery)
	err := sqlext.ForeachRow(cluster.DB, query, args, func(rows *sql.Rows) error {
		var (
			rateID        db.RateID
			usageAsBigint string
		)
		err := rows.Scan(&rateID, &usageAsBigint)
		if err != nil {
			return err
		}
		setInRateReport(filter, cluster, result, rateID, ratesv2.ClusterRateReport{UsageAsBigint: usageAsBigint})
		return nil
	})

	if err != nil {
		return *result, err
	}
	return *result, nil
}

// setInRateReport creates or iterates higher level structs on the way to the nested
// location of the db.RateID in the report and assigns the value for ratesv2.ClusterRateReport.
// If this rate should not get set because it does not have usage, this is a no-op.
func setInRateReport(filter Filter, cluster *core.Cluster, report *ratesv2.ClusterGetResponse, rateID db.RateID, value ratesv2.ClusterRateReport) {
	services := filter.GetServices()
	categories := filter.GetCategories()

	for _, serviceType := range slices.Sorted(maps.Keys(services)) {
		rates, _ := filter.GetRatesForType(serviceType) // can have no resources
		for _, rateName := range slices.Sorted(maps.Keys(rates)) {
			rate := rates[rateName]
			if rate.ID != rateID {
				continue
			}
			if !rate.HasUsage {
				return
			}

			config := cluster.Config.Liquids[serviceType]
			area := config.Area
			// defense in depth: config should be in sync with serviceInfo
			if area == "" {
				return
			}

			// check area level (might be uninitialized)
			if report.ClusterReport.Areas == nil {
				report.ClusterReport.Areas = make(map[string]ratesv2.ClusterAreaReport)
			}
			if _, exists := report.ClusterReport.Areas[area]; !exists {
				report.ClusterReport.Areas[area] = ratesv2.ClusterAreaReport{Services: make(map[db.ServiceType]ratesv2.ClusterServiceReport)}
			}
			areaReport := report.ClusterReport.Areas[area]

			// check service level
			if _, exists := areaReport.Services[serviceType]; !exists {
				areaReport.Services[serviceType] = ratesv2.ClusterServiceReport{Categories: make(map[liquid.CategoryName]ratesv2.ClusterCategoryReport)}
			}
			serviceReport := areaReport.Services[serviceType]

			// check category level
			category := liquid.CategoryName(serviceType)
			if categoryID, exists := rate.CategoryID.Unpack(); exists {
				category = categories[categoryID].Name
			}
			if _, exists := serviceReport.Categories[category]; !exists {
				serviceReport.Categories[category] = ratesv2.ClusterCategoryReport{Rates: make(map[liquid.RateName]ratesv2.ClusterRateReport)}
			}
			categoryReport := serviceReport.Categories[category]

			// check rate level
			categoryReport.Rates[rate.Name] = value
			return
		}
	}
}
