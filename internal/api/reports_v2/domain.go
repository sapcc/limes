// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"database/sql"

	"github.com/sapcc/go-bits/must"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

var domainRateReportQuery = sqlext.SimplifyWhitespace(`
	SELECT d.uuid, d.name, pra.rate_id, SUM(pra.usage_as_bigint::BIGINT)::TEXT AS usage_as_bigint
	FROM project_rates pra
	JOIN projects p
	ON p.id = pra.project_id
	JOIN domains d
	ON d.id = p.domain_id
	WHERE {{pra.rate_id = ANY($rate_id)}}
	AND {{d.id = ANY($domain_id)}}
	GROUP BY d.uuid, d.name, pra.rate_id
`)

// GetDomainRates returns a ratesv2.DomainGetResponse.
func GetDomainRates(cluster *core.Cluster, token *gopherpolicy.Token, filter Filter, opts common.DomainRateReportOpts, scope Scope) (ratesv2.DomainGetResponse, error) {
	result := &ratesv2.DomainGetResponse{}

	// fill info report
	if opts.WithInfo {
		infoReport, err := GetRatesInfo(cluster, token, filter)
		if err != nil {
			return *result, err
		}
		result.InfoReport = Some(infoReport)
	}

	// the result will have all rates without usage --> we will filter later
	query, args := filter.ExpandServiceFilters(domainRateReportQuery)
	query, args = scope.ExpandScopeFilters(query, args...)
	err := sqlext.ForeachRow(cluster.DB, query, args, func(rows *sql.Rows) error {
		var (
			domainUUID    string
			domainName    string
			rateID        db.RateID
			usageAsBigint string
		)
		err := rows.Scan(&domainUUID, &domainName, &rateID, &usageAsBigint)
		if err != nil {
			return err
		}
		setInDomainRateReport(filter, cluster, result, rateID, common.DomainMetadata{
			UUID: domainUUID,
			Name: domainName,
		}, ratesv2.DomainRateReport{
			UsageAsBigint: usageAsBigint,
		})
		return nil
	})

	if err != nil {
		return *result, err
	}
	return *result, nil
}

// setInDomainReport creates or iterates higher level structs on the way to the nested
// location of the db.RateID in the report and assigns the value for ratesv2.DomainRateReport.
// If this rate should not get set because it does not have usage, this is a no-op.
func setInDomainRateReport(filter Filter, cluster *core.Cluster, report *ratesv2.DomainGetResponse, rateID db.RateID, domain common.DomainMetadata, value ratesv2.DomainRateReport) {
	rate, rExists := filter.GetRateForID(rateID)
	if !rExists {
		// defense in depth: a rate was deleted in between, so we ignore the data
		return
	}
	// cannot be missing due to referential integrity
	service := must.BeOK(filter.GetServiceForID(rate.ServiceID))
	if !rate.HasUsage {
		return
	}

	config := cluster.Config.Liquids[service.Type]
	area := config.Area
	// defense in depth: config should be in sync with serviceInfo
	if area == "" {
		return
	}

	// check domain level (might be uninitialized)
	if report.DomainReports == nil {
		report.DomainReports = make(map[string]ratesv2.DomainReport)
	}
	if _, exists := report.DomainReports[domain.UUID]; !exists {
		report.DomainReports[domain.UUID] = ratesv2.DomainReport{
			DomainMetadata: domain,
			Areas:          make(map[string]ratesv2.DomainAreaReport),
		}
	}
	domainReport := report.DomainReports[domain.UUID]

	// check area level
	if _, exists := domainReport.Areas[area]; !exists {
		domainReport.Areas[area] = ratesv2.DomainAreaReport{Services: make(map[db.ServiceType]ratesv2.DomainServiceReport)}
	}
	areaReport := domainReport.Areas[area]

	// check service level
	if _, exists := areaReport.Services[service.Type]; !exists {
		areaReport.Services[service.Type] = ratesv2.DomainServiceReport{Categories: make(map[liquid.CategoryName]ratesv2.DomainCategoryReport)}
	}
	serviceReport := areaReport.Services[service.Type]

	// check category level
	category := liquid.CategoryName(service.Type)
	if categoryID, exists := rate.CategoryID.Unpack(); exists {
		category = must.BeOK(filter.GetCategoryForID(categoryID)).Name
	}
	if _, exists := serviceReport.Categories[category]; !exists {
		serviceReport.Categories[category] = ratesv2.DomainCategoryReport{Rates: make(map[liquid.RateName]ratesv2.DomainRateReport)}
	}
	categoryReport := serviceReport.Categories[category]

	// check rate level
	categoryReport.Rates[rate.Name] = value
}
