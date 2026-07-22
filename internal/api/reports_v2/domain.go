// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/sqlext"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

var domainResourceReportQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	WITH 
	$with_commitment_stats{{
		project_commitment_domain_sums AS (
			SELECT az_resource_id, domain_id,
		  	json_object_agg(status, by_status) AS committed
			FROM (
		  		SELECT az_resource_id, domain_id, status,
				json_object_agg(duration, total_amount) AS by_status
				FROM (
					SELECT az_resource_id, domain_id, status, duration, SUM(amount) AS total_amount
					FROM project_commitments pc
					JOIN projects p 
					ON pc.project_id = p.id 
					WHERE {{az_resource_id = ANY($az_resource_id)}}
					AND {{p.domain_id = ANY($domain_id)}}
					AND status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}, {{util.CommitmentStatusDeleted}})
					GROUP BY az_resource_id, domain_id, status, duration
		 		) inner_agg
		  		GROUP BY az_resource_id, domain_id, status
			) outer_agg
			GROUP BY az_resource_id, domain_id
		),
		project_commitment_project_sums_confirmed AS (
			SELECT az_resource_id, project_id, SUM(amount) as amount
			FROM project_commitments pc
			JOIN projects p 
			ON pc.project_id = p.id
			WHERE {{az_resource_id = ANY($az_resource_id)}}
			AND {{p.domain_id = ANY($domain_id)}}
			AND status = {{liquid.CommitmentStatusConfirmed}}
			GROUP BY az_resource_id, project_id
		),
	}}
	domain_sums AS (
		SELECT pazr.az_resource_id, domain_id,
		$with_commitment_stats{{
			SUM(COALESCE(pcpsc.amount, 0)) as committed_confirmed,
			SUM(GREATEST(COALESCE(pcpsc.AMOUNT-pazr.usage, 0), 0)) AS committed_confirmed_unutilized,
		}}
		SUM(pazr.usage) as usage,
		SUM(pazr.physical_usage) as physical_usage
		FROM project_az_resources pazr
		JOIN projects p 
		ON pazr.project_id = p.id
		$with_commitment_stats{{
			LEFT JOIN project_commitment_project_sums_confirmed pcpsc
			ON pcpsc.project_id = pazr.project_id AND pcpsc.az_resource_id = pazr.az_resource_id
		}}
		WHERE {{pazr.az_resource_id = ANY($az_resource_id)}}
		AND {{p.domain_id = ANY($domain_id)}}
		GROUP BY pazr.az_resource_id, domain_id
	)
	SELECT 
		d.uuid, d.name, azr.id, ds.usage,
		$with_commitment_stats{{
			COALESCE(pcds.committed, '{}') as committed,
			ds.committed_confirmed_unutilized,
			ds.usage - (ds.committed_confirmed - ds.committed_confirmed_unutilized) as usage_uncommitted, 
		}}
		ds.physical_usage
	FROM services s
	JOIN resources r ON r.service_id = s.id
	JOIN az_resources azr ON azr.resource_id = r.id
	JOIN domain_sums ds ON ds.az_resource_id = azr.id
	JOIN domains d ON d.id = ds.domain_id
	$with_commitment_stats{{
		LEFT JOIN project_commitment_domain_sums pcds ON pcds.az_resource_id = azr.id AND pcds.domain_id = ds.domain_id
	}}
	WHERE azr.az != {{liquid.AvailabilityZoneTotal}}
`))

// GetDomainResources returns a resourcesv2.DomainGetResponse.
func GetDomainResources(cluster *core.Cluster, token *gopherpolicy.Token, filter Filter, opts common.DomainResourceReportOpts, scope Scope, timeNow time.Time) (resourcesv2.DomainGetResponse, error) {
	var result resourcesv2.DomainGetResponse

	// fill info report
	if opts.WithInfo {
		infoReport, err := GetResourcesInfo(cluster, token, timeNow, filter)
		if err != nil {
			return result, err
		}
		result.InfoReport = Some(infoReport)
	}

	query := EvalDomainResourceExtraProps(domainResourceReportQuery, opts)
	query, args := filter.ExpandServiceFilters(query)
	query, args = scope.ExpandScopeFilters(query, args...)
	err := sqlext.ForeachRow(cluster.DB, query, args, func(rows *sql.Rows) error {
		var (
			domainUUID                   string
			domainName                   string
			azResourceID                 db.AZResourceID
			usage                        uint64
			committedJSON                string
			committedConfirmedUnutilized Option[uint64]
			usageUncommitted             Option[uint64]
			physicalUsage                Option[uint64]
		)
		columns := []any{&domainUUID, &domainName, &azResourceID, &usage}
		if opts.WithCommitmentStats {
			columns = append(columns, &committedJSON, &committedConfirmedUnutilized, &usageUncommitted)
		}
		columns = append(columns, &physicalUsage)
		err := rows.Scan(columns...)
		if err != nil {
			return err
		}

		// do some computations on the resulting values
		azResource, aExists := filter.GetAZResourceForID(azResourceID)
		if !aExists {
			// defense in depth: an az_resource was deleted in between, so we ignore the data
			return nil
		}
		var committed map[liquid.CommitmentStatus]map[limesresources.CommitmentDuration]uint64
		if opts.WithCommitmentStats {
			// cancel out committed values when no commitments are configured for this resource in all projects
			// the database cannot do this, because it does not know the allowed configurations
			commitmentBehavior := cluster.CommitmentBehaviorForResource(azResource.Path.ServiceType, azResource.Path.ResourceName)
			if len(commitmentBehavior.ForDomain(domainName).Durations) != 0 {
				err = json.Unmarshal([]byte(committedJSON), &committed)
				if err != nil {
					return fmt.Errorf("while parsing DB commitment stats for %s: %w", azResource.Path, err)
				}
			} else {
				usageUncommitted = None[uint64]()
				committedConfirmedUnutilized = None[uint64]()
			}
		}

		setInDomainResourceReport(filter, cluster, &result, azResourceID, common.DomainMetadata{
			UUID: domainUUID,
			Name: domainName,
		}, resourcesv2.DomainAvailabilityZoneReport{
			Usage:                        usage,
			Committed:                    committed,
			CommittedConfirmedUnutilized: committedConfirmedUnutilized,
			UsageUncommitted:             usageUncommitted,
			PhysicalUsage:                physicalUsage,
		})
		return nil
	})
	return result, err
}

// setInDomainResourceReport creates or iterates higher level structs on the way to the nested
// location of the db.AZResourceID in the report and assigns the value for resourcesv2.DomainAvailabilityZoneReport.
func setInDomainResourceReport(filter Filter, cluster *core.Cluster, report *resourcesv2.DomainGetResponse, azResourceID db.AZResourceID, domain common.DomainMetadata, value resourcesv2.DomainAvailabilityZoneReport) {
	azResource, aExists := filter.GetAZResourceForID(azResourceID)
	if !aExists {
		// defense in depth: an az_resource was deleted in between, so we ignore the data
		return
	}
	// cannot be missing due to referential integrity
	resource := must.BeOK(filter.GetResourceForID(azResource.ResourceID))
	service := must.BeOK(filter.GetServiceForID(resource.ServiceID))

	config := cluster.Config.Liquids[service.Type]
	area := config.Area
	// defense in depth: config should be in sync with serviceInfo
	if area == "" {
		return
	}

	// check domain level (might be uninitialized)
	if report.DomainReports == nil {
		report.DomainReports = make(map[string]resourcesv2.DomainReport)
	}
	if _, exists := report.DomainReports[domain.UUID]; !exists {
		report.DomainReports[domain.UUID] = resourcesv2.DomainReport{
			DomainMetadata: domain,
			Areas:          make(map[string]resourcesv2.DomainAreaReport),
		}
	}
	domainReport := report.DomainReports[domain.UUID]

	// check area level
	if _, exists := domainReport.Areas[area]; !exists {
		domainReport.Areas[area] = resourcesv2.DomainAreaReport{Services: make(map[db.ServiceType]resourcesv2.DomainServiceReport)}
	}
	areaReport := domainReport.Areas[area]

	// check service level
	if _, exists := areaReport.Services[service.Type]; !exists {
		areaReport.Services[service.Type] = resourcesv2.DomainServiceReport{
			Categories: make(map[liquid.CategoryName]resourcesv2.DomainCategoryReport),
		}
	}
	serviceReport := areaReport.Services[service.Type]

	// check category level
	category := liquid.CategoryName(service.Type)
	if categoryID, exists := resource.CategoryID.Unpack(); exists {
		category = must.BeOK(filter.GetCategoryForID(categoryID)).Name
	}
	if _, exists := serviceReport.Categories[category]; !exists {
		serviceReport.Categories[category] = resourcesv2.DomainCategoryReport{Resources: make(map[liquid.ResourceName]resourcesv2.DomainResourceReport)}
	}
	categoryReport := serviceReport.Categories[category]

	// check resource level
	if _, exists := categoryReport.Resources[resource.Name]; !exists {
		categoryReport.Resources[resource.Name] = resourcesv2.DomainResourceReport{AvailabilityZones: make(map[limes.AvailabilityZone]resourcesv2.DomainAvailabilityZoneReport)}
	}
	azReport := categoryReport.Resources[resource.Name]

	// check AZ level
	azReport.AvailabilityZones[azResource.AvailabilityZone] = value
}

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
	var result ratesv2.DomainGetResponse

	// fill info report
	if opts.WithInfo {
		infoReport, err := GetRatesInfo(cluster, token, filter)
		if err != nil {
			return result, err
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
		setInDomainRateReport(filter, cluster, &result, rateID, common.DomainMetadata{
			UUID: domainUUID,
			Name: domainName,
		}, ratesv2.DomainRateReport{
			UsageAsBigint: usageAsBigint,
		})
		return nil
	})
	return result, err
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
