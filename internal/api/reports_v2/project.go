// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/must"
	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/gg/options"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

var projectResourceReportQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	$with_commitment_stats{{
		WITH
		project_commitment_project_sums AS (
			SELECT az_resource_id, project_id,
		  	json_object_agg(status, by_status) AS committed
			FROM (
		  		SELECT az_resource_id, project_id, status,
				json_object_agg(duration, total_amount) AS by_status
				FROM (
					SELECT az_resource_id, project_id, status, duration, SUM(amount) AS total_amount
					FROM project_commitments pc
					JOIN projects p
					ON pc.project_id = p.id
					WHERE {{az_resource_id = ANY($az_resource_id)}}
					AND {{p.domain_id = ANY($domain_id)}}
					AND {{p.id = ANY($project_id)}}
					AND status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}, {{util.CommitmentStatusDeleted}})
					GROUP BY az_resource_id, project_id, status, duration
		 		) inner_agg
		  		GROUP BY az_resource_id, project_id, status
			) outer_agg
			GROUP BY az_resource_id, project_id
		)
	}}
	SELECT 
		d.uuid, d.name, p.uuid, p.name, p.parent_uuid, azr.id, pazr.usage, pazr.quota,
		$with_timing{{ps.scraped_at,}}
		$with_commitment_stats{{COALESCE(pcps.committed, '{}') as committed,}}
		$with_historical_usage{{pazr.historical_usage,}}
		$with_subresources{{pazr.subresources,}}
		$with_constraints{{pr.forbid_autogrowth, pr.max_quota_from_outside_admin,}}
		pazr.physical_usage
	FROM services s
	JOIN resources r ON r.service_id = s.id
	JOIN az_resources azr ON azr.resource_id = r.id
	JOIN project_az_resources pazr ON pazr.az_resource_id = azr.id
	$with_constraints{{
		JOIN project_resources pr ON pr.resource_id = r.id AND pazr.project_id = pr.project_id
	}}
	$with_timing{{
		JOIN project_services ps ON ps.service_id = s.id AND pazr.project_id = ps.project_id
	}}
	JOIN projects p ON p.id = pazr.project_id
	JOIN domains d ON d.id = p.domain_id
	$with_commitment_stats{{
		LEFT JOIN project_commitment_project_sums pcps ON pcps.az_resource_id = azr.id AND pcps.project_id = p.id
	}}
	WHERE {{d.id = ANY($domain_id)}}
	AND {{p.id = ANY($project_id)}}
	AND azr.az != {{liquid.AvailabilityZoneTotal}}
`))

// GetProjectResources returns a resourcesv2.ProjectGetResponse.
func GetProjectResources(cluster *core.Cluster, token *gopherpolicy.Token, filter Filter, opts common.ProjectResourceReportOpts, scope Scope, timeNow time.Time) (resourcesv2.ProjectGetResponse, error) {
	result := &resourcesv2.ProjectGetResponse{}

	// fill info report
	if opts.WithInfo {
		infoReport, err := GetResourcesInfo(cluster, token, timeNow, filter)
		if err != nil {
			return *result, err
		}
		result.InfoReport = Some(infoReport)
	}

	query := EvalProjectResourceExtraProps(projectResourceReportQuery, opts)
	query, args := filter.ExpandServiceFilters(query)
	query, args = scope.ExpandScopeFilters(query, args...)
	err := sqlext.ForeachRow(cluster.DB, query, args, func(rows *sql.Rows) error {
		var (
			domainUUID          string
			domainName          string
			projectUUID         string
			projectName         string
			projectParentUUID   string
			azResourceID        db.AZResourceID
			usage               uint64
			quota               Option[uint64]
			scrapedAt           Option[time.Time]
			committedJSON       string
			historicalUsageJSON string
			subresources        string
			constraints         struct {
				forbidAutogrowth         Option[bool]
				maxQuotaFromOutsideAdmin Option[uint64]
			}
			physicalUsage Option[uint64]
		)
		columns := []any{&domainUUID, &domainName, &projectUUID, &projectName, &projectParentUUID, &azResourceID, &usage, &quota}
		if opts.WithTiming {
			columns = append(columns, &scrapedAt)
		}
		if opts.WithCommitmentStats {
			columns = append(columns, &committedJSON)
		}
		if opts.WithHistoricalUsage {
			columns = append(columns, &historicalUsageJSON)
		}
		if opts.WithSubresources {
			columns = append(columns, &subresources)
		}
		if opts.WithUserSpecifiedConstraints {
			columns = append(columns, &constraints.forbidAutogrowth, &constraints.maxQuotaFromOutsideAdmin)
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
		historicalUsage := None[resourcesv2.ProjectHistoricalReport]()
		quotaDistConfig := cluster.QuotaDistributionConfigForResource(azResource.Path.ServiceType, azResource.Path.ResourceName)
		if opts.WithHistoricalUsage && quotaDistConfig.Model == limesresources.AutogrowQuotaDistribution {
			autogrowConfig := must.BeOK(quotaDistConfig.Autogrow.Unpack()) // safe because model=autogrow
			ts, err := util.ParseTimeSeries[uint64](historicalUsageJSON)
			if err != nil {
				return fmt.Errorf("while parsing historical_usage for project %s: %w", projectName, err)
			}
			historicalUsage = Some(resourcesv2.ProjectHistoricalReport{
				MinUsage: ts.MinOr(0),
				MaxUsage: ts.MaxOr(0),
				Duration: limesrates.Window(max(autogrowConfig.UsageDataRetentionPeriod.Into(), 0)),
			})
		}

		var committed map[liquid.CommitmentStatus]map[limesresources.CommitmentDuration]uint64
		if opts.WithCommitmentStats {
			// cancel out committed values when no commitments are configured for this resource in all projects
			// the database cannot do this, because it does not know the allowed configurations
			commitmentBehavior := cluster.CommitmentBehaviorForResource(azResource.Path.ServiceType, azResource.Path.ResourceName)
			if len(commitmentBehavior.ForDomain(domainName).Durations) != 0 {
				err = json.Unmarshal([]byte(committedJSON), &committed)
				if err != nil {
					return err
				}
			}
		}

		scrapedAtUnix := options.Map(scrapedAt, util.IntoUnixEncodedTime)

		setInProjectResourceReport(filter, cluster, result, azResourceID, common.ProjectMetadata{
			UUID:       projectUUID,
			Name:       projectName,
			ParentUUID: projectParentUUID,
			DomainInfo: common.DomainMetadata{
				UUID: domainUUID,
				Name: domainName,
			},
		}, resourcesv2.ProjectAvailabilityZoneReport{
			Usage:           usage,
			Quota:           quota,
			Committed:       committed,
			PhysicalUsage:   physicalUsage,
			HistoricalUsage: historicalUsage,
			Subresources:    json.RawMessage(subresources),
		}, scrapedAtUnix, constraints)
		return nil
	})

	if err != nil {
		return *result, err
	}
	return *result, nil
}

// setInProjectResourceReport creates or iterates higher level structs on the way to the nested
// location of the db.AZResourceID in the report and assigns the value for resourcesv2.ProjectAvailabilityZoneReport.
func setInProjectResourceReport(filter Filter, cluster *core.Cluster, report *resourcesv2.ProjectGetResponse, azResourceID db.AZResourceID, project common.ProjectMetadata, value resourcesv2.ProjectAvailabilityZoneReport, scrapedAt Option[limes.UnixEncodedTime], constraints struct {
	forbidAutogrowth         Option[bool]
	maxQuotaFromOutsideAdmin Option[uint64]
}) {

	azResource, aExists := filter.GetAZResourceForID(azResourceID)
	if !aExists {
		// defense in depth: a rate was deleted in between, so we ignore the data
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
		report.DomainReports = make(map[string]resourcesv2.ProjectsByDomainReport)
	}
	if _, exists := report.DomainReports[project.DomainInfo.UUID]; !exists {
		report.DomainReports[project.DomainInfo.UUID] = resourcesv2.ProjectsByDomainReport{
			ProjectReports: make(map[string]resourcesv2.ProjectReport),
		}
	}
	domainReport := report.DomainReports[project.DomainInfo.UUID]

	// check project level
	if _, exists := domainReport.ProjectReports[project.UUID]; !exists {
		domainReport.ProjectReports[project.UUID] = resourcesv2.ProjectReport{
			ProjectMetadata: project,
			Areas:           make(map[string]resourcesv2.ProjectAreaReport),
		}
	}
	projectReport := domainReport.ProjectReports[project.UUID]

	// check area level
	if _, exists := projectReport.Areas[area]; !exists {
		projectReport.Areas[area] = resourcesv2.ProjectAreaReport{Services: make(map[db.ServiceType]resourcesv2.ProjectServiceReport)}
	}
	areaReport := projectReport.Areas[area]

	// check service level
	if _, exists := areaReport.Services[service.Type]; !exists {
		areaReport.Services[service.Type] = resourcesv2.ProjectServiceReport{
			ScrapedAt:  scrapedAt,
			Categories: make(map[liquid.CategoryName]resourcesv2.ProjectCategoryReport),
		}
	}
	serviceReport := areaReport.Services[service.Type]

	// check category level
	category := liquid.CategoryName(service.Type)
	if categoryID, exists := resource.CategoryID.Unpack(); exists {
		category = must.BeOK(filter.GetCategoryForID(categoryID)).Name
	}
	if _, exists := serviceReport.Categories[category]; !exists {
		serviceReport.Categories[category] = resourcesv2.ProjectCategoryReport{Resources: make(map[liquid.ResourceName]resourcesv2.ProjectResourceReport)}
	}
	categoryReport := serviceReport.Categories[category]

	// check resource level
	if _, exists := categoryReport.Resources[resource.Name]; !exists {
		categoryReport.Resources[resource.Name] = resourcesv2.ProjectResourceReport{
			AvailabilityZones: make(map[limes.AvailabilityZone]resourcesv2.ProjectAvailabilityZoneReport),
			MaxQuota:          constraints.maxQuotaFromOutsideAdmin,
			ForbidAutogrowth:  constraints.forbidAutogrowth,
		}
	}
	azReport := categoryReport.Resources[resource.Name]

	// check AZ level
	azReport.AvailabilityZones[azResource.AvailabilityZone] = value
}

var projectRateReportQuery = sqlext.SimplifyWhitespace(`
	SELECT d.uuid, d.name, p.uuid, p.name, p.parent_uuid, pra.rate_id, pra.usage_as_bigint, pra.rate_limit, pra.window_ns
	FROM project_rates pra
	JOIN projects p
	ON p.id = pra.project_id
	JOIN domains d
	ON d.id = p.domain_id
	WHERE {{pra.rate_id = ANY($rate_id)}}
	AND {{d.id = ANY($domain_id)}}
	AND {{p.id = ANY($project_id)}}
`)

// GetProjectRates returns a ratesv2.ProjectGetResponse.
func GetProjectRates(cluster *core.Cluster, token *gopherpolicy.Token, filter Filter, opts common.ProjectRateReportOpts, scope Scope) (ratesv2.ProjectGetResponse, error) {
	result := &ratesv2.ProjectGetResponse{}

	// fill info report
	if opts.WithInfo {
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
	rate, rExists := filter.GetRateForID(rateID)
	if !rExists {
		// defense in depth: a rate was deleted in between, so we ignore the data
		return
	}
	// cannot be missing due to referential integrity
	service := must.BeOK(filter.GetServiceForID(rate.ServiceID))
	if !rate.HasUsage && value.ProjectLimit.IsNone() && value.ProjectWindow.IsNone() {
		return
	}
	// note: the database has a non-null constraint here, so we correct this after the fact
	if !rate.HasUsage {
		value.UsageAsBigint = None[string]()
	}

	config := cluster.Config.Liquids[service.Type]
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
	if _, exists := areaReport.Services[service.Type]; !exists {
		areaReport.Services[service.Type] = ratesv2.ProjectServiceReport{Categories: make(map[liquid.CategoryName]ratesv2.ProjectCategoryReport)}
	}
	serviceReport := areaReport.Services[service.Type]

	// check category level
	category := liquid.CategoryName(service.Type)
	if categoryID, exists := rate.CategoryID.Unpack(); exists {
		category = must.BeOK(filter.GetCategoryForID(categoryID)).Name
	}
	if _, exists := serviceReport.Categories[category]; !exists {
		serviceReport.Categories[category] = ratesv2.ProjectCategoryReport{Rates: make(map[liquid.RateName]ratesv2.ProjectRateReport)}
	}
	categoryReport := serviceReport.Categories[category]

	// check rate level
	categoryReport.Rates[rate.Name] = value
}
