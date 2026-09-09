// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/sqlext"
	"go.xyrillian.de/oblast"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

type projectResourceRecord struct {
	ProjectID                db.ProjectID            `db:"project_id"`
	DBServiceType            db.ServiceType          `db:"type"`
	ScrapedAt                *time.Time              `db:"scraped_at"`
	DBResourceName           liquid.ResourceName     `db:"name"`
	MaxQuotaFromOutsideAdmin *uint64                 `db:"max_quota_from_outside_admin"`
	ForbidAutogrowth         bool                    `db:"forbid_autogrowth"`
	Forbidden                bool                    `db:"forbidden"`
	AZ                       *limes.AvailabilityZone `db:"az"`
	Quota                    *uint64                 `db:"quota"`
	Usage                    *uint64                 `db:"usage"`
	PhysicalUsage            *uint64                 `db:"physical_usage"`
	HistoricalUsage          *string                 `db:"historical_usage"`
	BackendQuota             *int64                  `db:"backend_quota"`
	Subresources             *string                 `db:"subresources"`
}

var projectResourceStore = oblast.MustNewStore[projectResourceRecord](oblast.PostgresDialect())

var projectReportResourcesQuery = sqlext.SimplifyWhitespace(`
	SELECT p.id AS project_id, s.type, ps.scraped_at, r.name, pr.max_quota_from_outside_admin, pr.forbid_autogrowth, pr.forbidden, azr.az, pazr.quota, pazr.usage, pazr.physical_usage, pazr.historical_usage, pazr.backend_quota, pazr.subresources
	  FROM services s
	  JOIN resources r ON r.service_id = s.id {{AND r.name = $resource_name}}
	  JOIN az_resources azr ON azr.resource_id = r.id
	  CROSS JOIN projects p
	  JOIN project_services ps ON ps.service_id = s.id AND ps.project_id = p.id
	  JOIN project_resources pr ON pr.resource_id = r.id AND pr.project_id = p.id
	  -- no left join, entries will only appear when there is some project level entry
	  JOIN project_az_resources pazr ON pazr.az_resource_id = azr.id AND pazr.project_id = p.id
	 WHERE %s {{AND s.type = $service_type}}
	 ORDER BY p.uuid, azr.az
`)

type projectCommitmentRecord struct {
	DBServiceType   db.ServiceType                    `db:"type"`
	DBResourceName  liquid.ResourceName               `db:"name"`
	AZ              limes.AvailabilityZone            `db:"az"`
	Duration        limesresources.CommitmentDuration `db:"duration"`
	ConfirmedAmount uint64                            `db:"confirmed"`
	PendingAmount   uint64                            `db:"pending"`
	PlannedAmount   uint64                            `db:"planned"`
}

var projectCommitmentStore = oblast.MustNewStore[projectCommitmentRecord](oblast.PostgresDialect())

var projectReportCommitmentsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	SELECT s.type, r.name, azr.az, pc.duration,
	       COALESCE(SUM(pc.amount) FILTER (WHERE pc.status = {{liquid.CommitmentStatusConfirmed}}), 0) AS confirmed,
	       COALESCE(SUM(pc.amount) FILTER (WHERE pc.status = {{liquid.CommitmentStatusPending}}), 0) AS pending,
	       COALESCE(SUM(pc.amount) FILTER (WHERE pc.status = {{liquid.CommitmentStatusPlanned}}), 0) AS planned
	  FROM services s
	  JOIN resources r on r.service_id = s.id
	  JOIN az_resources azr ON azr.resource_id = r.id AND azr.az != {{liquid.AvailabilityZoneTotal}}
	  JOIN project_commitments pc ON pc.az_resource_id = azr.id
	 WHERE pc.project_id = $1
	 GROUP BY s.type, r.name, azr.az, pc.duration
	`))

type projectRateRecord struct {
	ProjectID      db.ProjectID       `db:"project_id"`
	DBServiceType  db.ServiceType     `db:"type"`
	RatesScrapedAt *time.Time         `db:"scraped_at"`
	DBRateName     liquid.RateName    `db:"name"`
	Limit          *uint64            `db:"rate_limit"`
	Window         *limesrates.Window `db:"window_ns"`
	UsageAsBigint  *string            `db:"usage_as_bigint"`
}

var projectRateStore = oblast.MustNewStore[projectRateRecord](oblast.PostgresDialect())

// Both queries are "ORDER BY p.uuid" to ensure that a) the output order is
// reproducible to keep the tests happy and b) records for the same project
// appear in a cluster, so that the implementation can publish completed
// project reports (and then reclaim their memory usage) as soon as possible.
var projectRateReportQuery = sqlext.SimplifyWhitespace(`
	SELECT p.id AS project_id, s.type, ps.scraped_at, ra.name, pra.rate_limit, pra.window_ns, pra.usage_as_bigint
	  FROM services s
	  JOIN rates ra ON ra.service_id = s.id
	  CROSS JOIN projects p
	  JOIN project_services ps ON ps.service_id = s.id AND ps.project_id = p.id
	  JOIN project_rates pra ON pra.rate_id = ra.id AND pra.project_id = ps.project_id
	 WHERE %s {{AND s.type = $service_type}}
	 ORDER BY p.uuid
`)

// GetProjectResources returns limes.ProjectReport reports for all projects in
// the given domain or, if project is non-nil, for that project only. Only the
// resource data will be filled; use GetProjectRates to get rate data.
//
// Since large domains can contain thousands of project reports, and project
// reports with the highest detail levels can be several MB large, we don't just
// return them all in a big list. Instead, the `submit` callback gets called
// once for each project report once that report is complete.
func GetProjectResources(ctx context.Context, cluster *core.Cluster, domain db.Domain, project *db.Project, now time.Time, dbi db.Interface, filter Filter, sis core.ServiceInfoSnapshot, submit func(*limesresources.ProjectReport) error) error {
	fields := map[string]any{"p.domain_id": domain.ID}
	if project != nil {
		fields["p.id"] = project.ID
	}
	nm := core.BuildResourceNameMapping(cluster, sis)

	// first, query for basic project information
	//
	// (this is important because a filter like `?service=none` is supported,
	// but will yield no results at all in the other queries)
	whereStr, whereArgs := db.BuildSimpleWhereClause(fields, 0)
	queryStr := `SELECT * FROM projects p WHERE ` + whereStr
	allProjects, err := db.ProjectStore.Select(ctx, dbi, queryStr, whereArgs...).Collect()
	if err != nil {
		return err
	}
	allProjectReports := make(map[db.ProjectID]*limesresources.ProjectReport, len(allProjects))
	for _, project := range allProjects {
		allProjectReports[project.ID] = &limesresources.ProjectReport{
			Name:       project.Name,
			UUID:       string(project.UUID),
			ParentUUID: project.ParentUUID,
			Services:   make(limesresources.ProjectServiceReports),
		}
	}

	// avoid collecting the potentially large subresources strings when possible
	queryStr = projectReportResourcesQuery
	if !filter.WithSubresources {
		queryStr = strings.Replace(queryStr, "pazr.subresources", "'' AS subresources", 1)
	}
	queryStr, joinArgs := filter.PrepareQuery(queryStr)
	whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))

	var (
		currentProjectID db.ProjectID
		projectReport    *limesresources.ProjectReport
	)
	err = projectResourceStore.Select(ctx, dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...)...).Foreach(func(r projectResourceRecord) error {
		if !filter.Includes[r.DBServiceType][r.DBResourceName] {
			return nil
		}
		behavior := cluster.BehaviorForResource(r.DBServiceType, r.DBResourceName)
		apiIdentity := behavior.IdentityInV1API

		// if we're moving to a different project, publish the finished report
		// first (and then allow for it to be GCd)
		if projectReport != nil && currentProjectID != r.ProjectID {
			err := finalizeProjectResourceReport(ctx, projectReport, currentProjectID, dbi, filter, nm)
			if err != nil {
				return err
			}
			err = submit(projectReport)
			if err != nil {
				return err
			}
			projectReport = nil
			currentProjectID = 0
		}

		// start new project report when necessary
		if projectReport == nil {
			projectReport = allProjectReports[r.ProjectID]
			delete(allProjectReports, r.ProjectID)
			if projectReport == nil {
				// this can happen if a project was inserted between the first and second query;
				// ignore those projects because we don't have complete information about them
				currentProjectID = 0
				return nil
			} else {
				currentProjectID = r.ProjectID
			}
		}

		// start new service report when necessary
		srvReport := projectReport.Services[apiIdentity.ServiceType]
		if srvReport == nil {
			srvCfg, _ := cluster.Config.GetLiquidConfigurationForType(r.DBServiceType)
			srvReport = &limesresources.ProjectServiceReport{
				Type: apiIdentity.ServiceType, Area: srvCfg.Area,
				Resources: make(limesresources.ProjectResourceReports),
			}
			projectReport.Services[apiIdentity.ServiceType] = srvReport

			if r.ScrapedAt != nil {
				t := limes.UnixEncodedTime{Time: *r.ScrapedAt}
				srvReport.ScrapedAt = &t
			}
		}

		// start new resource report when necessary
		resReport := srvReport.Resources[apiIdentity.Name]
		// we ignore when a resource can't be found in the app layer yet, it will appear with empty values
		resource, _ := sis.GetResourceForPath(db.ResourcePath{ServiceType: r.DBServiceType, ResourceName: r.DBResourceName})
		if resReport == nil {
			resReport = &limesresources.ProjectResourceReport{
				ResourceInfo: behavior.BuildAPIResourceInfo(apiIdentity.Name, resource),
				Usage:        0,
				// all other fields are set below
			}

			if !r.Forbidden {
				resReport.CommitmentConfig = cluster.CommitmentBehaviorForResource(r.DBServiceType, r.DBResourceName).ForDomain(domain.Name).ForAPI(now).AsPointer()
			}

			if filter.WithAZBreakdown {
				resReport.PerAZ = make(limesresources.ProjectAZResourceReports)
			}
			srvReport.Resources[apiIdentity.Name] = resReport
		}

		// fill data from project_az_resources into resource report
		// start with special handling of "total" AZ
		if *r.AZ == liquid.AvailabilityZoneTotal {
			qdConfig := cluster.QuotaDistributionConfigForResource(r.DBServiceType, r.DBResourceName)
			resReport.QuotaDistributionModel = qdConfig.Model

			resReport.Usage = *r.Usage
			if r.PhysicalUsage != nil {
				resReport.PhysicalUsage = r.PhysicalUsage
			}

			if !resReport.NoQuota && r.Quota != nil {
				if resource.Topology != liquid.AZSeparatedTopology {
					resReport.Quota = r.Quota
					resReport.UsableQuota = r.Quota
					if r.BackendQuota != nil && (*r.BackendQuota < 0 || uint64(*r.BackendQuota) != *r.Quota) {
						resReport.BackendQuota = r.BackendQuota
					}
				}
				if r.MaxQuotaFromOutsideAdmin != nil {
					resReport.MaxQuota = r.MaxQuotaFromOutsideAdmin
				}
				resReport.ForbidAutogrowth = r.ForbidAutogrowth
			}
		}

		if *r.AZ != liquid.AvailabilityZoneTotal {
			// we take the subresources from the AZ entries, so that we know from which AZ they come
			if r.Subresources != nil {
				translate := behavior.TranslationRuleInV1API.TranslateSubresources
				if translate != nil {
					var translateErr error
					*r.Subresources, translateErr = translate(*r.Subresources, *r.AZ, resource)
					if translateErr != nil {
						return fmt.Errorf("could not apply TranslationRule to subresources in %s/%s/%s of project %d: %w",
							r.DBServiceType, r.DBResourceName, *r.AZ, currentProjectID, translateErr)
					}
				}
				mergeJSONListInto(&resReport.Subresources, *r.Subresources)
			}

			if filter.WithAZBreakdown {
				resReport.PerAZ[*r.AZ] = &limesresources.ProjectAZResourceReport{
					Quota:         r.Quota,
					Committed:     nil, // will be filled by finalizeProjectResourceReport()
					Usage:         *r.Usage,
					PhysicalUsage: r.PhysicalUsage,
					Subresources:  json.RawMessage(*r.Subresources),
				}

				if *r.HistoricalUsage != "" {
					var duration limesresources.CommitmentDuration
					autogrowCfg, ok := cluster.QuotaDistributionConfigForResource(r.DBServiceType, r.DBResourceName).Autogrow.Unpack()
					if ok {
						duration = limesresources.CommitmentDuration{
							Short: autogrowCfg.UsageDataRetentionPeriod.Into(),
						}
					} else {
						duration = limesresources.CommitmentDuration{
							Short: 0,
						}
					}
					ts, err := util.ParseTimeSeries[uint64](*r.HistoricalUsage)
					if err != nil {
						return err
					}
					resReport.PerAZ[*r.AZ].HistoricalUsage = &limesresources.HistoricalReport{
						MinUsage: ts.MinOr(resReport.Usage),
						MaxUsage: ts.MaxOr(resReport.Usage),
						Duration: duration,
					}
				}
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	// submit final non-empty project report
	if projectReport != nil {
		err := finalizeProjectResourceReport(ctx, projectReport, currentProjectID, dbi, filter, nm)
		if err != nil {
			return err
		}
		err = submit(projectReport)
		if err != nil {
			return err
		}
	}

	// submit all project reports that did not have any resource data on them
	// (e.g. because the request filter was for `?service=none`)
	emptyProjectReports := make([]*limesresources.ProjectReport, 0, len(allProjectReports))
	for _, projectReport := range allProjectReports {
		emptyProjectReports = append(emptyProjectReports, projectReport)
	}
	slices.SortFunc(emptyProjectReports, func(lhs, rhs *limesresources.ProjectReport) int {
		return strings.Compare(lhs.UUID, rhs.UUID)
	})
	for _, projectReport := range emptyProjectReports {
		err = submit(projectReport)
		if err != nil {
			return err
		}
	}

	return nil
}

func finalizeProjectResourceReport(ctx context.Context, projectReport *limesresources.ProjectReport, projectID db.ProjectID, dbi db.Interface, filter Filter, nm core.ResourceNameMapping) error {
	if filter.WithAZBreakdown {
		// if `per_az` is shown, we need to compute the sum of all relevant commitments using a different query
		err := projectCommitmentStore.Select(ctx, dbi, projectReportCommitmentsQuery, projectID).Foreach(func(r projectCommitmentRecord) error {
			apiServiceType, apiResourceName, exists := nm.MapToV1API(r.DBServiceType, r.DBResourceName)
			if !exists {
				return nil
			}
			srvReport := projectReport.Services[apiServiceType]
			if srvReport == nil {
				return nil
			}
			resReport := srvReport.Resources[apiResourceName]
			if resReport == nil {
				return nil
			}
			azReport := resReport.PerAZ[r.AZ]
			if azReport == nil {
				return nil
			}

			if r.ConfirmedAmount > 0 {
				if azReport.Committed == nil {
					azReport.Committed = make(map[string]uint64)
				}
				azReport.Committed[r.Duration.String()] = r.ConfirmedAmount
			}
			if r.PendingAmount > 0 {
				if azReport.PendingCommitments == nil {
					azReport.PendingCommitments = make(map[string]uint64)
				}
				azReport.PendingCommitments[r.Duration.String()] = r.PendingAmount
			}
			if r.PlannedAmount > 0 {
				if azReport.PlannedCommitments == nil {
					azReport.PlannedCommitments = make(map[string]uint64)
				}
				azReport.PlannedCommitments[r.Duration.String()] = r.PlannedAmount
			}

			return nil
		})
		if err != nil {
			return err
		}

		// project_az_resources always has entries for "any", even if the resource
		// is AZ-aware, because ApplyComputedProjectQuota needs somewhere to write
		// the base quotas; we ignore those entries here if the "any" usage is zero
		// and there are other AZs
		// "unknown" may exist because the location for usages or capacities may be
		// unknown, but we only show it if there is non-zero quota/usage.
		for _, srvReport := range projectReport.Services {
			for _, resReport := range srvReport.Resources {
				if len(resReport.PerAZ) >= 2 {
					reportInUnknown := resReport.PerAZ[limes.AvailabilityZoneUnknown]
					if reportInUnknown != nil && (reportInUnknown.Quota == nil || *reportInUnknown.Quota == 0) && reportInUnknown.Usage == 0 {
						delete(resReport.PerAZ, limes.AvailabilityZoneUnknown)
					}
					reportInAny := resReport.PerAZ[limes.AvailabilityZoneAny]
					if reportInAny != nil && (reportInAny.Quota == nil || *reportInAny.Quota == 0) && reportInAny.Usage == 0 {
						delete(resReport.PerAZ, limes.AvailabilityZoneAny)
					}
				}
			}
		}
	}

	return nil
}

// GetProjectRates works just like GetProjects, except that rate data is returned instead of resource data.
func GetProjectRates(ctx context.Context, cluster *core.Cluster, domain db.Domain, project *db.Project, dbi db.Interface, filter Filter, sis core.ServiceInfoSnapshot, submit func(*limesrates.ProjectReport) error) error {
	fields := map[string]any{"p.domain_id": domain.ID}
	if project != nil {
		fields["p.id"] = project.ID
	}
	nm := core.BuildRateNameMapping(cluster, sis)

	// first, query for basic project information
	//
	// (this is important because a filter like `?service=none` is supported,
	// but will yield no results at all in the other queries)
	whereStr, whereArgs := db.BuildSimpleWhereClause(fields, 0)
	queryStr := `SELECT * FROM projects p WHERE ` + whereStr
	allProjects, err := db.ProjectStore.Select(ctx, dbi, queryStr, whereArgs...).Collect()
	if err != nil {
		return err
	}
	allProjectInfos := make(map[db.ProjectID]limes.ProjectInfo, len(allProjects))
	for _, project := range allProjects {
		allProjectInfos[project.ID] = limes.ProjectInfo{
			Name:       project.Name,
			UUID:       string(project.UUID),
			ParentUUID: project.ParentUUID,
		}
	}

	// query for rate data
	queryStr, joinArgs := filter.PrepareQuery(projectRateReportQuery)
	whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))

	var (
		currentProjectID db.ProjectID
		projectReport    *limesrates.ProjectReport
	)
	err = projectRateStore.Select(ctx, dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...)...).Foreach(func(r projectRateRecord) error {
		// if we're moving to a different project, publish the finished report
		// first (and then allow for it to be GCd)
		if projectReport != nil && currentProjectID != r.ProjectID {
			err := submit(projectReport)
			if err != nil {
				return err
			}
			projectReport = nil
		}

		// start new project report when necessary
		if projectReport == nil {
			projectInfo, exists := allProjectInfos[r.ProjectID]
			delete(allProjectInfos, r.ProjectID)
			if exists {
				currentProjectID = r.ProjectID
			} else {
				// this can happen if a project was inserted between the first and second query;
				// ignore those projects because we don't have complete information about them
				currentProjectID = 0
				return nil
			}
			projectReport = initProjectRateReport(projectInfo, cluster, nm, sis)
		}

		// if we don't have a valid rate, we're done with this result row
		rate, ok := sis.GetRateForPath(db.RatePath{ServiceType: r.DBServiceType, RateName: r.DBRateName})
		if !ok {
			return nil
		}
		apiServiceType, apiRateName, exists := nm.MapToV1API(r.DBServiceType, r.DBRateName)
		if !exists {
			return nil
		}

		// start new service report when necessary
		srvReport := projectReport.Services[apiServiceType]
		if srvReport == nil {
			srvCfg, _ := cluster.Config.GetLiquidConfigurationForType(r.DBServiceType)
			srvReport = &limesrates.ProjectServiceReport{
				Type: apiServiceType, Area: srvCfg.Area,
				Rates: make(limesrates.ProjectRateReports),
			}
			projectReport.Services[apiServiceType] = srvReport
		}

		if r.RatesScrapedAt != nil {
			t := limes.UnixEncodedTime{Time: *r.RatesScrapedAt}
			srvReport.ScrapedAt = &t
		}

		// create the rate report if necessary (rates with a limit will already have
		// one because of the default rate limit, so this is only relevant for
		// rates that only have a usage)
		rateReport := srvReport.Rates[apiRateName]
		if rateReport == nil && r.UsageAsBigint != nil && *r.UsageAsBigint != "" && rate.HasUsage {
			// if we are in here, the rate has to exist
			rateReport = &limesrates.ProjectRateReport{
				RateInfo: core.BuildAPIRateInfo(apiRateName, rate.Unit),
			}
			srvReport.Rates[apiRateName] = rateReport
		}

		// fill remaining data into rate report
		if rateReport != nil {
			if r.UsageAsBigint != nil {
				rateReport.UsageAsBigint = *r.UsageAsBigint
			}

			// overwrite the default limit if a different custom limit is
			// configured, but ignore custom limits where there is no default
			// limit
			if rateReport.Limit != 0 && r.Limit != nil && r.Window != nil {
				if rateReport.Limit != *r.Limit || *rateReport.Window != *r.Window {
					rateReport.DefaultLimit = rateReport.Limit
					rateReport.DefaultWindow = rateReport.Window
					rateReport.Limit = *r.Limit
					rateReport.Window = r.Window
				}
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	// submit final non-empty project report
	if projectReport != nil {
		return submit(projectReport)
	}

	// submit all project reports that did not have any resource data on them
	// (e.g. because the request filter was for `?service=none`)
	emptyProjectReports := make([]*limesrates.ProjectReport, 0, len(allProjectInfos))
	for _, projectInfo := range allProjectInfos {
		emptyProjectReports = append(emptyProjectReports, initProjectRateReport(projectInfo, cluster, nm, sis))
	}
	slices.SortFunc(emptyProjectReports, func(lhs, rhs *limesrates.ProjectReport) int {
		return strings.Compare(lhs.UUID, rhs.UUID)
	})
	for _, projectReport := range emptyProjectReports {
		err = submit(projectReport)
		if err != nil {
			return err
		}
	}

	return nil
}

// Builds a fresh ProjectReport with default rate-limits pre-filled from the cluster config.
func initProjectRateReport(projectInfo limes.ProjectInfo, cluster *core.Cluster, nm core.RateNameMapping, sis core.ServiceInfoSnapshot) *limesrates.ProjectReport {
	report := limesrates.ProjectReport{
		ProjectInfo: projectInfo,
		Services:    make(limesrates.ProjectServiceReports),
	}

	for dbServiceType, l := range cluster.Config.Liquids {
		for _, rateLimitConfig := range l.RateLimits.ProjectDefault {
			apiServiceType, apiRateName, exists := nm.MapToV1API(dbServiceType, rateLimitConfig.Name)
			if !exists {
				continue // defense in depth: should not happen because NameMapping iterated through the same structure
			}

			srvReport := report.Services[apiServiceType]
			if srvReport == nil {
				srvCfg, _ := cluster.Config.GetLiquidConfigurationForType(dbServiceType)
				srvReport = &limesrates.ProjectServiceReport{
					Type: apiServiceType, Area: srvCfg.Area,
					Rates: make(limesrates.ProjectRateReports),
				}
				report.Services[apiServiceType] = srvReport
			}

			// we ignore when a rate can't be found in the app layer yet, it will appear with empty values
			rate, _ := sis.GetRateForPath(db.RatePath{ServiceType: dbServiceType, RateName: rateLimitConfig.Name})
			srvReport.Rates[apiRateName] = &limesrates.ProjectRateReport{
				RateInfo: core.BuildAPIRateInfo(apiRateName, rate.Unit),
				Limit:    rateLimitConfig.Limit,
				Window:   &rateLimitConfig.Window,
			}
		}
	}

	return &report
}
