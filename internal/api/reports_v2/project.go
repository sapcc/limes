// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/must"
	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/gg/options"
	"go.xyrillian.de/oblast"

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
					SELECT pc.az_resource_id, pc.project_id, pc.status, pc.duration, SUM(pc.amount) AS total_amount
					FROM project_commitments pc
					JOIN projects p
					ON pc.project_id = p.id
					WHERE {{pc.az_resource_id = ANY($az_resource_id)}}
					AND {{p.domain_id = ANY($domain_id)}}
					AND {{p.id = ANY($project_id)}}
					AND pc.status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}, {{util.CommitmentStatusDeleted}})
					GROUP BY pc.az_resource_id, pc.project_id, pc.status, pc.duration
				) inner_agg
				GROUP BY az_resource_id, project_id, status
			) outer_agg
			GROUP BY az_resource_id, project_id
		)
	}}
	SELECT
		d.uuid AS domain_uuid, d.name AS domain_name,
		p.uuid AS project_uuid, p.name AS project_name, p.parent_uuid,
		azr.id AS az_resource_id, pazr.usage, pazr.quota,
		$with_timing{{ps.scraped_at,}}
		$without_timing{{NULL AS scraped_at,}}
		$with_commitment_stats{{COALESCE(pcps.committed, '{}') AS committed,}}
		$without_commitment_stats{{'' AS committed,}}
		$with_historical_usage{{pazr.historical_usage,}}
		$without_historical_usage{{'' AS historical_usage,}}
		$with_subresources{{pazr.subresources,}}
		$without_subresources{{'' AS subresources,}}
		$with_constraints{{pr.forbid_autogrowth, pr.max_quota_from_outside_admin,}}
		$without_constraints{{NULL AS forbid_autogrowth, NULL AS max_quota_from_outside_admin,}}
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

type projectConstraints struct {
	ForbidAutogrowth         Option[bool]   `db:"forbid_autogrowth"`
	MaxQuotaFromOutsideAdmin Option[uint64] `db:"max_quota_from_outside_admin"`
}

// GetProjectResources returns a resourcesv2.ProjectGetResponse.
func GetProjectResources(ctx context.Context, cluster *core.Cluster, token *gopherpolicy.Token, filter PathFilter, opts common.ProjectResourceReportOpts, scope Scope, timeNow time.Time) (resourcesv2.ProjectGetResponse, error) {
	var result resourcesv2.ProjectGetResponse

	// fill info report
	if opts.WithInfo {
		infoReport, err := GetResourcesInfo(cluster, token, timeNow, filter)
		if err != nil {
			return result, err
		}
		result.InfoReport = Some(infoReport)
	}

	type record struct {
		DomainUUID          string            `db:"domain_uuid"`
		DomainName          string            `db:"domain_name"`
		ProjectUUID         string            `db:"project_uuid"`
		ProjectName         string            `db:"project_name"`
		ProjectParentUUID   string            `db:"parent_uuid"`
		AZResourceID        db.AZResourceID   `db:"az_resource_id"`
		Usage               uint64            `db:"usage"`
		Quota               Option[uint64]    `db:"quota"`
		ScrapedAt           Option[time.Time] `db:"scraped_at"`
		CommittedJSON       string            `db:"committed"`
		HistoricalUsageJSON string            `db:"historical_usage"`
		Subresources        string            `db:"subresources"`
		projectConstraints                    // contains nested subfields with `db:"..."`
		PhysicalUsage       Option[uint64]    `db:"physical_usage"`
	}
	query := EvalProjectResourceExtraProps(projectResourceReportQuery, opts)
	query, args := filter.ExpandServiceFilters(query)
	query, args = scope.ExpandScopeFilters(query, args...)
	err := oblast.MustNewStore[record](oblast.PostgresDialect()).Select(ctx, cluster.DB, query, args...).Foreach(func(r record) error {
		// do some computations on the resulting values
		azResource, aExists := filter.GetAZResourceForID(r.AZResourceID)
		if !aExists {
			// defense in depth: an az_resource was deleted in between, so we ignore the data
			return nil
		}
		historicalUsage := None[resourcesv2.ProjectHistoricalReport]()
		quotaDistConfig := cluster.QuotaDistributionConfigForResource(azResource.Path.ServiceType, azResource.Path.ResourceName)
		if opts.WithHistoricalUsage && quotaDistConfig.Model == limesresources.AutogrowQuotaDistribution {
			autogrowConfig := must.BeOK(quotaDistConfig.Autogrow.Unpack()) // safe because model=autogrow
			ts, err := util.ParseTimeSeries[uint64](r.HistoricalUsageJSON)
			if err != nil {
				return fmt.Errorf("while parsing historical_usage for project %s: %w", r.ProjectName, err)
			}
			historicalUsage = Some(resourcesv2.ProjectHistoricalReport{
				MinUsage: ts.MinOr(0),
				MaxUsage: ts.MaxOr(0),
				Duration: limesrates.Window(max(autogrowConfig.UsageDataRetentionPeriod.Into(), 0)),
			})
		}

		var committed map[liquid.CommitmentStatus]map[limesresources.CommitmentDuration]uint64
		if opts.WithCommitmentStats {
			err := json.Unmarshal([]byte(r.CommittedJSON), &committed)
			if err != nil {
				return fmt.Errorf("while parsing DB commitment stats for %s: %w", azResource.Path, err)
			}

			// do not report commitment stats if the resource does not allow new commitments in this domain
			// (however, if there are pre-existing commitments, report those in the usual way until they all expire or are deleted)
			commitmentBehavior := cluster.CommitmentBehaviorForResource(azResource.Path.ServiceType, azResource.Path.ResourceName)
			if len(commitmentBehavior.ForDomain(r.DomainName).Durations) == 0 && len(committed) == 0 {
				committed = nil
			}
		}

		scrapedAtRFC := options.Map(r.ScrapedAt, common.IntoRFC3339EncodedTime)

		setInProjectResourceReport(filter, cluster, &result, r.AZResourceID, scrapedAtRFC, r.projectConstraints, common.ProjectMetadata{
			UUID:       r.ProjectUUID,
			Name:       r.ProjectName,
			ParentUUID: r.ProjectParentUUID,
			DomainInfo: common.DomainMetadata{
				UUID: r.DomainUUID,
				Name: r.DomainName,
			},
		}, resourcesv2.ProjectAvailabilityZoneReport{
			Usage:           r.Usage,
			Quota:           r.Quota,
			Committed:       committed,
			PhysicalUsage:   r.PhysicalUsage,
			HistoricalUsage: historicalUsage,
			Subresources:    json.RawMessage(r.Subresources),
		})
		return nil
	})
	return result, err
}

// setInProjectResourceReport creates or iterates higher level structs on the way to the nested
// location of the db.AZResourceID in the report and assigns the value for resourcesv2.ProjectAvailabilityZoneReport.
func setInProjectResourceReport(filter PathFilter, cluster *core.Cluster, report *resourcesv2.ProjectGetResponse, azResourceID db.AZResourceID, scrapedAt Option[common.RFC3339EncodedTime], constraints projectConstraints, project common.ProjectMetadata, value resourcesv2.ProjectAvailabilityZoneReport) {
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
	categoryName := must.BeOK(filter.GetCategoryForID(resource.CategoryID)).Name
	if _, exists := serviceReport.Categories[categoryName]; !exists {
		serviceReport.Categories[categoryName] = resourcesv2.ProjectCategoryReport{Resources: make(map[liquid.ResourceName]resourcesv2.ProjectResourceReport)}
	}
	categoryReport := serviceReport.Categories[categoryName]

	// check resource level
	if _, exists := categoryReport.Resources[resource.Name]; !exists {
		categoryReport.Resources[resource.Name] = resourcesv2.ProjectResourceReport{
			AvailabilityZones: make(map[limes.AvailabilityZone]resourcesv2.ProjectAvailabilityZoneReport),
			MaxQuota:          constraints.MaxQuotaFromOutsideAdmin,
			ForbidAutogrowth:  constraints.ForbidAutogrowth,
		}
	}
	azReport := categoryReport.Resources[resource.Name]

	// check AZ level
	azReport.AvailabilityZones[azResource.AvailabilityZone] = value
}

var projectRateReportQuery = sqlext.SimplifyWhitespace(`
	SELECT
		d.uuid AS domain_uuid, d.name AS domain_name,
		p.uuid AS project_uuid, p.name AS project_name, p.parent_uuid,
		pra.rate_id, pra.usage_as_bigint, pra.rate_limit, pra.window_ns
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
func GetProjectRates(ctx context.Context, cluster *core.Cluster, token *gopherpolicy.Token, filter PathFilter, opts common.ProjectRateReportOpts, scope Scope) (ratesv2.ProjectGetResponse, error) {
	var result ratesv2.ProjectGetResponse

	// fill info report
	if opts.WithInfo {
		infoReport, err := GetRatesInfo(cluster, token, filter)
		if err != nil {
			return result, err
		}
		result.InfoReport = Some(infoReport)
	}

	type record struct {
		DomainUUID        string                    `db:"domain_uuid"`
		DomainName        string                    `db:"domain_name"`
		ProjectUUID       string                    `db:"project_uuid"`
		ProjectName       string                    `db:"project_name"`
		ProjectParentUUID string                    `db:"parent_uuid"`
		RateID            db.RateID                 `db:"rate_id"`
		UsageAsBigint     string                    `db:"usage_as_bigint"`
		ProjectLimit      Option[uint64]            `db:"rate_limit"`
		ProjectWindow     Option[limesrates.Window] `db:"window_ns"`
	}
	// the result will have all rates without usage --> we will filter later
	query, args := filter.ExpandServiceFilters(projectRateReportQuery)
	query, args = scope.ExpandScopeFilters(query, args...)
	err := oblast.MustNewStore[record](oblast.PostgresDialect()).Select(ctx, cluster.DB, query, args...).Foreach(func(r record) error {
		setInProjectRateReport(filter, cluster, &result, r.RateID, common.ProjectMetadata{
			UUID:       r.ProjectUUID,
			Name:       r.ProjectName,
			ParentUUID: r.ProjectParentUUID,
			DomainInfo: common.DomainMetadata{
				UUID: r.DomainUUID,
				Name: r.DomainName,
			},
		}, ratesv2.ProjectRateReport{
			UsageAsBigint: Some(r.UsageAsBigint), // note: the database has a non-null constraint here, make None when setting
			ProjectLimit:  r.ProjectLimit,
			ProjectWindow: r.ProjectWindow,
		})
		return nil
	})
	return result, err
}

// setInProjectRateReport creates or iterates higher level structs on the way to the nested
// location of the db.RateID in the report and assigns the value for ratesv2.ProjectRateReport.
// If this rate should not get set because it does not have usage, this is a no-op.
func setInProjectRateReport(filter PathFilter, cluster *core.Cluster, report *ratesv2.ProjectGetResponse, rateID db.RateID, project common.ProjectMetadata, value ratesv2.ProjectRateReport) {
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
	categoryName := must.BeOK(filter.GetCategoryForID(rate.CategoryID)).Name
	if _, exists := serviceReport.Categories[categoryName]; !exists {
		serviceReport.Categories[categoryName] = ratesv2.ProjectCategoryReport{Rates: make(map[liquid.RateName]ratesv2.ProjectRateReport)}
	}
	categoryReport := serviceReport.Categories[categoryName]

	// check rate level
	categoryReport.Rates[rate.Name] = value
}
