// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports

import (
	"database/sql"
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

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// Both queries are "ORDER BY p.uuid" to ensure that a) the output order is
// reproducible to keep the tests happy and b) records for the same project
// appear in a cluster, so that the implementation can publish completed
// project reports (and then reclaim their memory usage) as soon as possible.
var (
	projectRateReportQuery = sqlext.SimplifyWhitespace(`
	SELECT p.id, cs.type, ps.scraped_at, cra.name, pra.rate_limit, pra.window_ns, pra.usage_as_bigint
	  FROM services cs
	  JOIN rates cra ON cra.service_id = cs.id
	  CROSS JOIN projects p
	  JOIN project_services ps ON ps.service_id = cs.id AND ps.project_id = p.id
	  JOIN project_rates pra ON pra.rate_id = cra.id AND pra.project_id = ps.project_id
	 WHERE %s {{AND cs.type = $service_type}}
	 ORDER BY p.uuid
`)

	projectReportResourcesQuery = sqlext.SimplifyWhitespace(`
	SELECT p.id, cs.type, ps.scraped_at, cr.name, pr.quota, pr.max_quota_from_outside_admin, pr.max_quota_from_local_admin, pr.forbid_autogrowth, cazr.az, pazr.quota, pazr.usage, pazr.physical_usage, pazr.historical_usage, pr.backend_quota, pazr.subresources
	  FROM services cs
	  JOIN resources cr ON cr.service_id = cs.id {{AND cr.name = $resource_name}}
	  JOIN az_resources cazr ON cazr.resource_id = cr.id
	  CROSS JOIN projects p
	  JOIN project_services ps ON ps.service_id = cs.id AND ps.project_id = p.id
	  JOIN project_resources pr ON pr.resource_id = cr.id AND pr.project_id = p.id
	  -- no left join, entries will only appear when there is some project level entry
	  JOIN project_az_resources pazr ON pazr.az_resource_id = cazr.id AND pazr.project_id = p.id
	 WHERE %s {{AND cs.type = $service_type}}
	 ORDER BY p.uuid, cazr.az
`)

	projectReportCommitmentsQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	SELECT cs.type, cr.name, cazr.az, pc.duration,
	       COALESCE(SUM(pc.amount) FILTER (WHERE pc.status = {{liquid.CommitmentStatusConfirmed}}), 0) AS confirmed,
	       COALESCE(SUM(pc.amount) FILTER (WHERE pc.status = {{liquid.CommitmentStatusPending}}), 0) AS pending,
	       COALESCE(SUM(pc.amount) FILTER (WHERE pc.status = {{liquid.CommitmentStatusPlanned}}), 0) AS planned
	  FROM services cs
	  JOIN resources cr on cr.service_id = cs.id
	  JOIN az_resources cazr ON cazr.resource_id = cr.id
	  JOIN project_commitments pc ON pc.az_resource_id = cazr.id
	 WHERE pc.project_id = $1
	 GROUP BY cs.type, cr.name, cazr.az, pc.duration
	`))
)

// GetProjectResources returns limes.ProjectReport reports for all projects in
// the given domain or, if project is non-nil, for that project only. Only the
// resource data will be filled; use GetProjectRates to get rate data.
//
// Since large domains can contain thousands of project reports, and project
// reports with the highest detail levels can be several MB large, we don't just
// return them all in a big list. Instead, the `submit` callback gets called
// once for each project report once that report is complete.
func GetProjectResources(cluster *core.Cluster, domain db.Domain, project *db.Project, now time.Time, dbi db.Interface, filter Filter, serviceInfos map[db.ServiceType]liquid.ServiceInfo, submit func(*limesresources.ProjectReport) error) error {
	fields := map[string]any{"p.domain_id": domain.ID}
	if project != nil {
		fields["p.id"] = project.ID
	}
	nm := core.BuildResourceNameMapping(cluster, serviceInfos)

	// first, query for basic project information
	//
	// (this is important because a filter like `?service=none` is supported,
	// but will yield no results at all in the other queries)
	whereStr, whereArgs := db.BuildSimpleWhereClause(fields, 0)
	queryStr := `SELECT * FROM projects p WHERE ` + whereStr
	var allProjects []db.Project
	_, err := dbi.Select(&allProjects, queryStr, whereArgs...)
	if err != nil {
		return err
	}
	allProjectReports := make(map[db.ProjectID]*limesresources.ProjectReport, len(allProjects))
	for _, project := range allProjects {
		allProjectReports[project.ID] = &limesresources.ProjectReport{
			ProjectInfo: limes.ProjectInfo{
				Name:       project.Name,
				UUID:       string(project.UUID),
				ParentUUID: project.ParentUUID,
			},
			Services: make(limesresources.ProjectServiceReports),
		}
	}

	// avoid collecting the potentially large subresources strings when possible
	queryStr = projectReportResourcesQuery
	if !filter.WithSubresources {
		queryStr = strings.Replace(queryStr, "pazr.subresources", "''", 1)
	}
	queryStr, joinArgs := filter.PrepareQuery(queryStr)
	whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))

	var (
		currentProjectID db.ProjectID
		projectReport    *limesresources.ProjectReport
	)
	err = sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			projectID                db.ProjectID
			dbServiceType            db.ServiceType
			scrapedAt                *time.Time
			dbResourceName           liquid.ResourceName
			quota                    *uint64
			maxQuotaFromOutsideAdmin *uint64
			MaxQuotaFromLocalAdmin   *uint64
			ForbidAutogrowth         bool
			az                       *limes.AvailabilityZone
			azQuota                  *uint64
			azUsage                  *uint64
			azPhysicalUsage          *uint64
			azHistoricalUsage        *string
			backendQuota             *int64
			azSubresources           *string
		)
		err := rows.Scan(
			&projectID, &dbServiceType, &scrapedAt, &dbResourceName,
			&quota, &maxQuotaFromOutsideAdmin, &MaxQuotaFromLocalAdmin, &ForbidAutogrowth,
			&az, &azQuota, &azUsage, &azPhysicalUsage, &azHistoricalUsage, &backendQuota, &azSubresources,
		)
		if err != nil {
			return err
		}
		if !filter.Includes[dbServiceType][dbResourceName] {
			return nil
		}
		behavior := cluster.BehaviorForResource(dbServiceType, dbResourceName)
		apiIdentity := behavior.IdentityInV1API

		// if we're moving to a different project, publish the finished report
		// first (and then allow for it to be GCd)
		if projectReport != nil && currentProjectID != projectID {
			err := finalizeProjectResourceReport(projectReport, currentProjectID, dbi, filter, nm)
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
			projectReport = allProjectReports[projectID]
			delete(allProjectReports, projectID)
			if projectReport == nil {
				// this can happen if a project was inserted between the first and second query;
				// ignore those projects because we don't have complete information about them
				currentProjectID = 0
				return nil
			} else {
				currentProjectID = projectID
			}
		}

		// start new service report when necessary
		srvReport := projectReport.Services[apiIdentity.ServiceType]
		if srvReport == nil {
			srvCfg, _ := cluster.Config.GetLiquidConfigurationForType(dbServiceType)
			srvReport = &limesresources.ProjectServiceReport{
				ServiceInfo: limes.ServiceInfo{Type: apiIdentity.ServiceType, Area: srvCfg.Area},
				Resources:   make(limesresources.ProjectResourceReports),
			}
			projectReport.Services[apiIdentity.ServiceType] = srvReport

			if scrapedAt != nil {
				t := limes.UnixEncodedTime{Time: *scrapedAt}
				srvReport.ScrapedAt = &t
			}
		}

		// start new resource report when necessary
		resReport := srvReport.Resources[apiIdentity.Name]
		if resReport == nil {
			serviceInfo := core.InfoForService(serviceInfos, dbServiceType)
			resInfo := core.InfoForResource(serviceInfo, dbResourceName)
			resReport = &limesresources.ProjectResourceReport{
				ResourceInfo:     behavior.BuildAPIResourceInfo(apiIdentity.Name, resInfo),
				Usage:            0,
				CommitmentConfig: cluster.CommitmentBehaviorForResource(dbServiceType, dbResourceName).ForDomain(domain.Name).ForAPI(now).AsPointer(),
				// all other fields are set below
			}

			if filter.WithAZBreakdown {
				resReport.PerAZ = make(limesresources.ProjectAZResourceReports)
			}

			if !resReport.NoQuota {
				qdConfig := cluster.QuotaDistributionConfigForResource(dbServiceType, dbResourceName)
				resReport.QuotaDistributionModel = qdConfig.Model
				if quota != nil {
					resReport.Quota = quota
					resReport.UsableQuota = quota
					if maxQuotaFromOutsideAdmin != nil && MaxQuotaFromLocalAdmin == nil {
						resReport.MaxQuota = maxQuotaFromOutsideAdmin
					}
					if MaxQuotaFromLocalAdmin != nil && maxQuotaFromOutsideAdmin == nil {
						resReport.MaxQuota = MaxQuotaFromLocalAdmin
					}
					if maxQuotaFromOutsideAdmin != nil && MaxQuotaFromLocalAdmin != nil {
						maxQuota := min(*maxQuotaFromOutsideAdmin, *MaxQuotaFromLocalAdmin)
						resReport.MaxQuota = &maxQuota
					}
					if backendQuota != nil && (*backendQuota < 0 || uint64(*backendQuota) != *quota) {
						resReport.BackendQuota = backendQuota
					}
					resReport.ForbidAutogrowth = ForbidAutogrowth
				}
			}

			srvReport.Resources[apiIdentity.Name] = resReport
		}

		// fill data from project_az_resources into resource report
		if az == nil {
			return nil // no project_az_resources available
		}
		resReport.Usage += *azUsage
		if azPhysicalUsage != nil {
			sum := unwrapOrDefault(resReport.PhysicalUsage, 0) + *azPhysicalUsage
			resReport.PhysicalUsage = &sum
		}
		if azSubresources != nil {
			translate := behavior.TranslationRuleInV1API.TranslateSubresources
			if translate != nil {
				serviceInfo := core.InfoForService(serviceInfos, dbServiceType)
				resInfo := core.InfoForResource(serviceInfo, dbResourceName)
				*azSubresources, err = translate(*azSubresources, *az, dbResourceName, resInfo)
				if err != nil {
					return fmt.Errorf("could not apply TranslationRule to subresources in %s/%s/%s of project %d: %w",
						dbServiceType, dbResourceName, *az, currentProjectID, err)
				}
			}
			mergeJSONListInto(&resReport.Subresources, *azSubresources)
		}

		if filter.WithAZBreakdown {
			resReport.PerAZ[*az] = &limesresources.ProjectAZResourceReport{
				Quota:         azQuota,
				Committed:     nil, // will be filled by finalizeProjectResourceReport()
				Usage:         *azUsage,
				PhysicalUsage: azPhysicalUsage,
				Subresources:  json.RawMessage(*azSubresources),
			}

			if *azHistoricalUsage != "" {
				var duration limesresources.CommitmentDuration
				autogrowCfg, ok := cluster.QuotaDistributionConfigForResource(dbServiceType, dbResourceName).Autogrow.Unpack()
				if ok {
					duration = limesresources.CommitmentDuration{
						Short: autogrowCfg.UsageDataRetentionPeriod.Into(),
					}
				} else {
					duration = limesresources.CommitmentDuration{
						Short: 0,
					}
				}
				ts, err := util.ParseTimeSeries[uint64](*azHistoricalUsage)
				if err != nil {
					return err
				}
				resReport.PerAZ[*az].HistoricalUsage = &limesresources.HistoricalReport{
					MinUsage: ts.MinOr(resReport.Usage),
					MaxUsage: ts.MaxOr(resReport.Usage),
					Duration: duration,
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
		err := finalizeProjectResourceReport(projectReport, currentProjectID, dbi, filter, nm)
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

func finalizeProjectResourceReport(projectReport *limesresources.ProjectReport, projectID db.ProjectID, dbi db.Interface, filter Filter, nm core.ResourceNameMapping) error {
	if filter.WithAZBreakdown {
		// if `per_az` is shown, we need to compute the sum of all relevant commitments using a different query
		err := sqlext.ForeachRow(dbi, projectReportCommitmentsQuery, []any{projectID}, func(rows *sql.Rows) error {
			var (
				dbServiceType   db.ServiceType
				dbResourceName  liquid.ResourceName
				az              limes.AvailabilityZone
				duration        limesresources.CommitmentDuration
				confirmedAmount uint64
				pendingAmount   uint64
				plannedAmount   uint64
			)
			err := rows.Scan(&dbServiceType, &dbResourceName, &az, &duration, &confirmedAmount, &pendingAmount, &plannedAmount)
			if err != nil {
				return err
			}
			apiServiceType, apiResourceName, exists := nm.MapToV1API(dbServiceType, dbResourceName)
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
			azReport := resReport.PerAZ[az]
			if azReport == nil {
				return nil
			}

			if confirmedAmount > 0 {
				if azReport.Committed == nil {
					azReport.Committed = make(map[string]uint64)
				}
				azReport.Committed[duration.String()] = confirmedAmount
			}
			if pendingAmount > 0 {
				if azReport.PendingCommitments == nil {
					azReport.PendingCommitments = make(map[string]uint64)
				}
				azReport.PendingCommitments[duration.String()] = pendingAmount
			}
			if plannedAmount > 0 {
				if azReport.PlannedCommitments == nil {
					azReport.PlannedCommitments = make(map[string]uint64)
				}
				azReport.PlannedCommitments[duration.String()] = plannedAmount
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
		for _, srvReport := range projectReport.Services {
			for _, resReport := range srvReport.Resources {
				if len(resReport.PerAZ) >= 2 {
					reportInAny := resReport.PerAZ[limes.AvailabilityZoneAny]
					// AZSeparatedTopology does not provide the "any" AZ.
					if reportInAny == nil {
						continue
					}
					if (reportInAny.Quota == nil || *reportInAny.Quota == 0) && reportInAny.Usage == 0 {
						delete(resReport.PerAZ, limes.AvailabilityZoneAny)
					}
				}
			}
		}
	}

	return nil
}

// GetProjectRates works just like GetProjects, except that rate data is returned instead of resource data.
func GetProjectRates(cluster *core.Cluster, domain db.Domain, project *db.Project, dbi db.Interface, filter Filter, serviceInfos map[db.ServiceType]liquid.ServiceInfo, submit func(*limesrates.ProjectReport) error) error {
	fields := map[string]any{"p.domain_id": domain.ID}
	if project != nil {
		fields["p.id"] = project.ID
	}
	nm := core.BuildRateNameMapping(cluster, serviceInfos)

	// first, query for basic project information
	//
	// (this is important because a filter like `?service=none` is supported,
	// but will yield no results at all in the other queries)
	whereStr, whereArgs := db.BuildSimpleWhereClause(fields, 0)
	queryStr := `SELECT * FROM projects p WHERE ` + whereStr
	var allProjects []db.Project
	_, err := dbi.Select(&allProjects, queryStr, whereArgs...)
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
	err = sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			projectID      db.ProjectID
			dbServiceType  db.ServiceType
			ratesScrapedAt *time.Time
			dbRateName     liquid.RateName
			limit          *uint64
			window         *limesrates.Window
			usageAsBigint  *string
		)
		err := rows.Scan(
			&projectID, &dbServiceType, &ratesScrapedAt,
			&dbRateName, &limit, &window, &usageAsBigint,
		)
		if err != nil {
			return err
		}

		// if we're moving to a different project, publish the finished report
		// first (and then allow for it to be GCd)
		if projectReport != nil && currentProjectID != projectID {
			err := submit(projectReport)
			if err != nil {
				return err
			}
			projectReport = nil
		}

		// start new project report when necessary
		if projectReport == nil {
			projectInfo, exists := allProjectInfos[projectID]
			delete(allProjectInfos, projectID)
			if exists {
				currentProjectID = projectID
			} else {
				// this can happen if a project was inserted between the first and second query;
				// ignore those projects because we don't have complete information about them
				currentProjectID = 0
				return nil
			}
			projectReport = initProjectRateReport(projectInfo, cluster, nm, serviceInfos)
		}

		// if we don't have a valid service type, we're done with this result row
		if !core.HasService(serviceInfos, dbServiceType) {
			return nil
		}
		apiServiceType, apiRateName, exists := nm.MapToV1API(dbServiceType, dbRateName)
		if !exists {
			return nil
		}

		// start new service report when necessary
		srvReport := projectReport.Services[apiServiceType]
		if srvReport == nil {
			srvCfg, _ := cluster.Config.GetLiquidConfigurationForType(dbServiceType)
			srvReport = &limesrates.ProjectServiceReport{
				ServiceInfo: limes.ServiceInfo{Type: apiServiceType, Area: srvCfg.Area},
				Rates:       make(limesrates.ProjectRateReports),
			}
			projectReport.Services[apiServiceType] = srvReport
		}

		if ratesScrapedAt != nil {
			t := limes.UnixEncodedTime{Time: *ratesScrapedAt}
			srvReport.ScrapedAt = &t
		}

		// create the rate report if necessary (rates with a limit will already have
		// one because of the default rate limit, so this is only relevant for
		// rates that only have a usage)
		rateReport := srvReport.Rates[apiRateName]
		if rateReport == nil && usageAsBigint != nil && *usageAsBigint != "" && core.HasUsageForRate(serviceInfos, dbServiceType, dbRateName) {
			rateInfo := core.InfoForRate(serviceInfos, dbServiceType, dbRateName)
			rateReport = &limesrates.ProjectRateReport{
				RateInfo: core.BuildAPIRateInfo(apiRateName, rateInfo),
			}
			srvReport.Rates[apiRateName] = rateReport
		}

		// fill remaining data into rate report
		if rateReport != nil {
			if usageAsBigint != nil {
				rateReport.UsageAsBigint = *usageAsBigint
			}

			// overwrite the default limit if a different custom limit is
			// configured, but ignore custom limits where there is no default
			// limit
			if rateReport.Limit != 0 && limit != nil && window != nil {
				if rateReport.Limit != *limit || *rateReport.Window != *window {
					rateReport.DefaultLimit = rateReport.Limit
					rateReport.DefaultWindow = rateReport.Window
					rateReport.Limit = *limit
					rateReport.Window = window
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
		emptyProjectReports = append(emptyProjectReports, initProjectRateReport(projectInfo, cluster, nm, serviceInfos))
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
func initProjectRateReport(projectInfo limes.ProjectInfo, cluster *core.Cluster, nm core.RateNameMapping, serviceInfos map[db.ServiceType]liquid.ServiceInfo) *limesrates.ProjectReport {
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
					ServiceInfo: limes.ServiceInfo{Type: apiServiceType, Area: srvCfg.Area},
					Rates:       make(limesrates.ProjectRateReports),
				}
				report.Services[apiServiceType] = srvReport
			}

			rateInfo := core.InfoForRate(serviceInfos, dbServiceType, rateLimitConfig.Name)
			srvReport.Rates[apiRateName] = &limesrates.ProjectRateReport{
				RateInfo: core.BuildAPIRateInfo(apiRateName, rateInfo),
				Limit:    rateLimitConfig.Limit,
				Window:   &rateLimitConfig.Window,
			}
		}
	}

	return &report
}
