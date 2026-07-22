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
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/sqlext"
	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/gg/options"

	"github.com/sapcc/go-bits/gopherpolicy"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

var clusterResourceReportQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	WITH 
	$with_commitment_stats{{
		project_commitment_sums AS (
			SELECT az_resource_id,
		  	json_object_agg(status, by_status) AS committed
			FROM (
		  		SELECT az_resource_id, status,
				json_object_agg(duration, total_amount) AS by_status
				FROM (
					SELECT az_resource_id, status, duration, SUM(amount) AS total_amount
					FROM project_commitments
					WHERE {{az_resource_id = ANY($az_resource_id)}}
					AND status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}, {{util.CommitmentStatusDeleted}})
					GROUP BY az_resource_id, status, duration
		 		) inner_agg
		  		GROUP BY az_resource_id, status
			) outer_agg
			GROUP BY az_resource_id
		),
		project_commitment_project_sums_confirmed AS (
			SELECT az_resource_id, project_id, SUM(amount) as amount
			FROM project_commitments
			WHERE {{az_resource_id = ANY($az_resource_id)}}
			AND status = {{liquid.CommitmentStatusConfirmed}}
			GROUP BY az_resource_id, project_id
		),
	}}
	project_sums AS (
		SELECT pazr.az_resource_id, 
		$with_commitment_stats{{
			SUM(COALESCE(pcpsc.amount, 0)) as committed_confirmed,
			SUM(GREATEST(COALESCE(pcpsc.AMOUNT-pazr.usage, 0), 0)) AS committed_confirmed_unutilized,
		}}
		SUM(pazr.usage) as usage,
		SUM(pazr.physical_usage) as physical_usage
		FROM project_az_resources pazr
		$with_commitment_stats{{
			LEFT JOIN project_commitment_project_sums_confirmed pcpsc
			ON pcpsc.project_id = pazr.project_id AND pcpsc.az_resource_id = pazr.az_resource_id
		}}
		WHERE {{az_resource_id = ANY($az_resource_id)}}
		GROUP BY pazr.az_resource_id
	)
	SELECT 
		azr.id, azr.raw_capacity, azr.usage as overall_usage, ps.usage,
		$with_timing{{s.scraped_at,}} 
		$with_commitment_stats{{
			COALESCE(pcs.committed, '{}') as committed,
			ps.committed_confirmed_unutilized,
			ps.usage - (ps.committed_confirmed - ps.committed_confirmed_unutilized) as usage_uncommitted, 
		}}
		$with_subcapacities{{azr.subcapacities,}}
		ps.physical_usage
	FROM services s
	JOIN resources r ON r.service_id = s.id
	JOIN az_resources azr ON azr.resource_id = r.id
	JOIN project_sums ps ON ps.az_resource_id = azr.id
	$with_commitment_stats{{
		LEFT JOIN project_commitment_sums pcs ON pcs.az_resource_id = azr.id
	}}
	WHERE azr.az != {{liquid.AvailabilityZoneTotal}}
`))

// GetClusterResources returns a resourcesv2.ClusterGetResponse.
func GetClusterResources(cluster *core.Cluster, token *gopherpolicy.Token, filter Filter, opts common.ClusterResourceReportOpts, timeNow time.Time) (resourcesv2.ClusterGetResponse, error) {
	var result resourcesv2.ClusterGetResponse

	// fill info report
	if opts.WithInfo {
		infoReport, err := GetResourcesInfo(cluster, token, timeNow, filter)
		if err != nil {
			return result, err
		}
		result.InfoReport = Some(infoReport)
	}

	query := EvalClusterResourceExtraProps(clusterResourceReportQuery, opts)
	query, args := filter.ExpandServiceFilters(query)
	err := sqlext.ForeachRow(cluster.DB, query, args, func(rows *sql.Rows) error {
		var (
			azResourceID                 db.AZResourceID
			rawCapacity                  uint64
			overallUsage                 Option[uint64]
			usage                        uint64
			scrapedAt                    Option[time.Time]
			committedJSON                string
			committedConfirmedUnutilized Option[uint64]
			usageUncommitted             Option[uint64]
			subcapacities                string
			physicalUsage                Option[uint64]
		)
		columns := []any{&azResourceID, &rawCapacity, &overallUsage, &usage}
		if opts.WithTiming {
			columns = append(columns, &scrapedAt)
		}
		if opts.WithCommitmentStats {
			columns = append(columns, &committedJSON, &committedConfirmedUnutilized, &usageUncommitted)
		}
		if opts.WithSubcapacities {
			columns = append(columns, &subcapacities)
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
		overcommitFactor := cluster.BehaviorForResource(azResource.Path.ServiceType, azResource.Path.ResourceName).OvercommitFactor
		capacity := overcommitFactor.ApplyTo(rawCapacity)

		var committed map[liquid.CommitmentStatus]map[limesresources.CommitmentDuration]uint64
		if opts.WithCommitmentStats {
			// cancel out committed values when no commitments are configured for this resource in all projects
			// the database cannot do this, because it does not know the allowed configurations
			commitmentBehavior := cluster.CommitmentBehaviorForResource(azResource.Path.ServiceType, azResource.Path.ResourceName)
			if len(commitmentBehavior.ForCluster().Durations) != 0 {
				err = json.Unmarshal([]byte(committedJSON), &committed)
				if err != nil {
					return fmt.Errorf("while parsing DB commitment stats for %s: %w", azResource.Path, err)
				}
			} else {
				usageUncommitted = None[uint64]()
				committedConfirmedUnutilized = None[uint64]()
			}
		}

		// cancel out committed values when no commitments are configured for this resource in all projects
		// the database cannot do this, because it does not know the allowed configurations
		commitmentBehavior := cluster.CommitmentBehaviorForResource(azResource.Path.ServiceType, azResource.Path.ResourceName)
		if len(commitmentBehavior.ForCluster().Durations) == 0 {
			committed = make(map[liquid.CommitmentStatus]map[limesresources.CommitmentDuration]uint64)
			usageUncommitted = None[uint64]()
			committedConfirmedUnutilized = None[uint64]()
		}

		scrapedAtUnix := options.Map(scrapedAt, util.IntoUnixEncodedTime)

		setInClusterResourceReport(filter, cluster, &result, azResourceID, resourcesv2.ClusterAvailabilityZoneReport{
			Capacity:                     capacity,
			RawCapacity:                  rawCapacity,
			OverallUsage:                 overallUsage,
			Usage:                        usage,
			Committed:                    committed,
			CommittedConfirmedUnutilized: committedConfirmedUnutilized,
			UsageUncommitted:             usageUncommitted,
			PhysicalUsage:                physicalUsage,
			Subcapacities:                json.RawMessage(subcapacities),
		}, scrapedAtUnix)
		return nil
	})

	return result, err
}

// setInClusterResourceReport creates or iterates higher level structs on the way to the nested
// location of the db.AZResourceID in the report and assigns the value for resourcesv2.ClusterAvailabilityZoneReport.
func setInClusterResourceReport(filter Filter, cluster *core.Cluster, report *resourcesv2.ClusterGetResponse, azResourceID db.AZResourceID, value resourcesv2.ClusterAvailabilityZoneReport, scrapedAt Option[limes.UnixEncodedTime]) {
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

	// check area level (might be uninitialized)
	if report.ClusterReport.Areas == nil {
		report.ClusterReport.Areas = make(map[string]resourcesv2.ClusterAreaReport)
	}
	if _, exists := report.ClusterReport.Areas[area]; !exists {
		report.ClusterReport.Areas[area] = resourcesv2.ClusterAreaReport{Services: make(map[db.ServiceType]resourcesv2.ClusterServiceReport)}
	}
	areaReport := report.ClusterReport.Areas[area]

	// check service level
	if _, exists := areaReport.Services[service.Type]; !exists {
		areaReport.Services[service.Type] = resourcesv2.ClusterServiceReport{
			ScrapedAt:  scrapedAt,
			Categories: make(map[liquid.CategoryName]resourcesv2.ClusterCategoryReport),
		}
	}
	serviceReport := areaReport.Services[service.Type]

	// check category level
	category := liquid.CategoryName(service.Type)
	if categoryID, exists := resource.CategoryID.Unpack(); exists {
		category = must.BeOK(filter.GetCategoryForID(categoryID)).Name
	}
	if _, exists := serviceReport.Categories[category]; !exists {
		serviceReport.Categories[category] = resourcesv2.ClusterCategoryReport{Resources: make(map[liquid.ResourceName]resourcesv2.ClusterResourceReport)}
	}
	categoryReport := serviceReport.Categories[category]

	// check resource level
	if _, exists := categoryReport.Resources[resource.Name]; !exists {
		categoryReport.Resources[resource.Name] = resourcesv2.ClusterResourceReport{AvailabilityZones: make(map[limes.AvailabilityZone]resourcesv2.ClusterAvailabilityZoneReport)}
	}
	azReport := categoryReport.Resources[resource.Name]

	// check AZ level
	azReport.AvailabilityZones[azResource.AvailabilityZone] = value
}

var clusterRateReportQuery = sqlext.SimplifyWhitespace(`
	SELECT pra.rate_id, SUM(pra.usage_as_bigint::BIGINT)::TEXT AS usage_as_bigint
	FROM project_rates pra
	WHERE {{pra.rate_id = ANY($rate_id)}}
	GROUP BY pra.rate_id
`)

// GetClusterRates returns a ratesv2.ClusterGetResponse.
func GetClusterRates(cluster *core.Cluster, token *gopherpolicy.Token, filter Filter, opts common.ClusterRateReportOpts) (ratesv2.ClusterGetResponse, error) {
	var result ratesv2.ClusterGetResponse

	// fill info report
	if opts.WithInfo {
		infoReport, err := GetRatesInfo(cluster, token, filter)
		if err != nil {
			return result, err
		}
		result.InfoReport = Some(infoReport)
	}

	// the result will have all rates without usage --> we will filter later
	query, args := filter.ExpandServiceFilters(clusterRateReportQuery)
	err := sqlext.ForeachRow(cluster.DB, query, args, func(rows *sql.Rows) error {
		var (
			rateID        db.RateID
			usageAsBigint string
		)
		err := rows.Scan(&rateID, &usageAsBigint)
		if err != nil {
			return err
		}
		setInClusterRateReport(filter, cluster, &result, rateID, ratesv2.ClusterRateReport{UsageAsBigint: usageAsBigint})
		return nil
	})
	return result, err
}

// setInClusterRateReport creates or iterates higher level structs on the way to the nested
// location of the db.RateID in the report and assigns the value for ratesv2.ClusterRateReport.
// If this rate should not get set because it does not have usage, this is a no-op.
func setInClusterRateReport(filter Filter, cluster *core.Cluster, report *ratesv2.ClusterGetResponse, rateID db.RateID, value ratesv2.ClusterRateReport) {
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

	// check area level (might be uninitialized)
	if report.ClusterReport.Areas == nil {
		report.ClusterReport.Areas = make(map[string]ratesv2.ClusterAreaReport)
	}
	if _, exists := report.ClusterReport.Areas[area]; !exists {
		report.ClusterReport.Areas[area] = ratesv2.ClusterAreaReport{Services: make(map[db.ServiceType]ratesv2.ClusterServiceReport)}
	}
	areaReport := report.ClusterReport.Areas[area]

	// check service level
	if _, exists := areaReport.Services[service.Type]; !exists {
		areaReport.Services[service.Type] = ratesv2.ClusterServiceReport{Categories: make(map[liquid.CategoryName]ratesv2.ClusterCategoryReport)}
	}
	serviceReport := areaReport.Services[service.Type]

	// check category level
	category := liquid.CategoryName(service.Type)
	if categoryID, exists := rate.CategoryID.Unpack(); exists {
		category = must.BeOK(filter.GetCategoryForID(categoryID)).Name
	}
	if _, exists := serviceReport.Categories[category]; !exists {
		serviceReport.Categories[category] = ratesv2.ClusterCategoryReport{Rates: make(map[liquid.RateName]ratesv2.ClusterRateReport)}
	}
	categoryReport := serviceReport.Categories[category]

	// check rate level
	categoryReport.Rates[rate.Name] = value
}
