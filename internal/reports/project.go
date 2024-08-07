/*******************************************************************************
*
* Copyright 2017-2020 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

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
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// NOTE: Both queries use LEFT OUTER JOIN to generate at least one result row
// per known project, to ensure that each project gets a report even if its
// resource data and/or rate data is incomplete.
//
// Both queries are "ORDER BY p.uuid" to ensure that a) the output order is
// reproducible to keep the tests happy and b) records for the same project
// appear in a cluster, so that the implementation can publish completed
// project reports (and then reclaim their memory usage) as soon as possible.
var (
	projectRateReportQuery = sqlext.SimplifyWhitespace(`
	SELECT p.uuid, p.name, COALESCE(p.parent_uuid, ''), ps.type, ps.rates_scraped_at, pra.name, pra.rate_limit, pra.window_ns, pra.usage_as_bigint
	  FROM projects p
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_rates pra ON pra.service_id = ps.id
	 WHERE %s
	 ORDER BY p.uuid
`)

	projectReportResourcesQuery = sqlext.SimplifyWhitespace(`
	SELECT p.id, ps.type, ps.scraped_at, pr.name, pr.quota, pr.max_quota_from_admin, par.az, par.quota, par.usage, par.physical_usage, par.historical_usage, pr.backend_quota, par.subresources
	  FROM projects p
	  JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	  LEFT OUTER JOIN project_az_resources par ON par.resource_id = pr.id
	 WHERE %s
	 ORDER BY p.uuid, par.az
`)

	projectReportCommitmentsQuery = sqlext.SimplifyWhitespace(`
	SELECT ps.type, pr.name, par.az, pc.duration,
	       COALESCE(SUM(pc.amount) FILTER (WHERE pc.state = 'active'),  0) AS active,
	       COALESCE(SUM(pc.amount) FILTER (WHERE pc.state = 'pending'), 0) AS pending,
	       COALESCE(SUM(pc.amount) FILTER (WHERE pc.state = 'planned'), 0) AS planned
	  FROM project_services ps
	  JOIN project_resources pr ON pr.service_id = ps.id
	  JOIN project_az_resources par ON par.resource_id = pr.id
	  JOIN project_commitments pc ON pc.az_resource_id = par.id
	 WHERE ps.project_id = $1
	 GROUP BY ps.type, pr.name, par.az, pc.duration
	`)
)

// GetProjectResources returns limes.ProjectReport reports for all projects in
// the given domain or, if project is non-nil, for that project only. Only the
// resource data will be filled; use GetProjectRates to get rate data.
//
// Since large domains can contain thousands of project reports, and project
// reports with the highest detail levels can be several MB large, we don't just
// return them all in a big list. Instead, the `submit` callback gets called
// once for each project report once that report is complete.
func GetProjectResources(cluster *core.Cluster, domain db.Domain, project *db.Project, now time.Time, dbi db.Interface, filter Filter, submit func(*limesresources.ProjectReport) error) error {
	fields := map[string]any{"p.domain_id": domain.ID}
	if project != nil {
		fields["p.id"] = project.ID
	}

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
				UUID:       project.UUID,
				ParentUUID: project.ParentUUID,
			},
			Services: make(limesresources.ProjectServiceReports),
		}
	}

	// avoid collecting the potentially large subresources strings when possible
	queryStr = projectReportResourcesQuery
	if !filter.WithSubresources {
		queryStr = strings.Replace(queryStr, "par.subresources", "''", 1)
	}
	queryStr, joinArgs := filter.PrepareQuery(queryStr)
	whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))

	var (
		currentProjectID db.ProjectID
		projectReport    *limesresources.ProjectReport
	)
	err = sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			projectID         db.ProjectID
			dbServiceType     limes.ServiceType
			scrapedAt         *time.Time
			dbResourceName    limesresources.ResourceName
			quota             *uint64
			maxQuotaFromAdmin *uint64
			az                *limes.AvailabilityZone
			azQuota           *uint64
			azUsage           *uint64
			azPhysicalUsage   *uint64
			azHistoricalUsage *string
			backendQuota      *int64
			azSubresources    *string
		)
		err := rows.Scan(
			&projectID, &dbServiceType, &scrapedAt, &dbResourceName,
			&quota, &maxQuotaFromAdmin,
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
			err := finalizeProjectResourceReport(projectReport, currentProjectID, dbi, filter)
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
			srvReport = &limesresources.ProjectServiceReport{
				ServiceInfo: cluster.InfoForService(dbServiceType).ForAPI(apiIdentity.ServiceType),
				Resources:   make(limesresources.ProjectResourceReports),
			}
			projectReport.Services[apiIdentity.ServiceType] = srvReport

			if scrapedAt != nil {
				t := limes.UnixEncodedTime{Time: *scrapedAt}
				srvReport.ScrapedAt = &t
			}
		}

		// start new resource report when necessary
		resReport := srvReport.Resources[apiIdentity.ResourceName]
		if resReport == nil {
			resInfo := cluster.InfoForResource(dbServiceType, dbResourceName)
			resReport = &limesresources.ProjectResourceReport{
				ResourceInfo:     behavior.BuildAPIResourceInfo(apiIdentity.ResourceName, resInfo),
				Usage:            0,
				CommitmentConfig: behavior.ToCommitmentConfig(now),
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
					resReport.MaxQuota = maxQuotaFromAdmin
					if backendQuota != nil && (*backendQuota < 0 || uint64(*backendQuota) != *quota) {
						resReport.BackendQuota = backendQuota
					}
				}
			}

			srvReport.Resources[apiIdentity.ResourceName] = resReport
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
				config := cluster.QuotaDistributionConfigForResource(dbServiceType, dbResourceName)
				var duration limesresources.CommitmentDuration
				if config.Autogrow != nil {
					retentionPeriod := config.Autogrow.UsageDataRetentionPeriod
					duration = limesresources.CommitmentDuration{
						Short: retentionPeriod.Into(),
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
		err := finalizeProjectResourceReport(projectReport, currentProjectID, dbi, filter)
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

func finalizeProjectResourceReport(projectReport *limesresources.ProjectReport, projectID db.ProjectID, dbi db.Interface, filter Filter) error {
	if filter.WithAZBreakdown {
		// if `per_az` is shown, we need to compute the sum of all active commitments using a different query
		err := sqlext.ForeachRow(dbi, projectReportCommitmentsQuery, []any{projectID}, func(rows *sql.Rows) error {
			var (
				serviceType   limes.ServiceType
				resourceName  limesresources.ResourceName
				az            limes.AvailabilityZone
				duration      limesresources.CommitmentDuration
				activeAmount  uint64
				pendingAmount uint64
				plannedAmount uint64
			)
			err := rows.Scan(&serviceType, &resourceName, &az, &duration, &activeAmount, &pendingAmount, &plannedAmount)
			if err != nil {
				return err
			}

			srvReport := projectReport.Services[serviceType]
			if srvReport == nil {
				return nil
			}
			resReport := srvReport.Resources[resourceName]
			if resReport == nil {
				return nil
			}
			azReport := resReport.PerAZ[az]
			if azReport == nil {
				return nil
			}

			if activeAmount > 0 {
				if azReport.Committed == nil {
					azReport.Committed = make(map[string]uint64)
				}
				azReport.Committed[duration.String()] = activeAmount
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
func GetProjectRates(cluster *core.Cluster, domain db.Domain, project *db.Project, dbi db.Interface, filter Filter, submit func(*limesrates.ProjectReport) error) error {
	fields := map[string]any{"p.domain_id": domain.ID}
	if project != nil {
		fields["p.id"] = project.ID
	}

	// query for rate data
	queryStr, joinArgs := filter.PrepareQuery(projectRateReportQuery)
	whereStr, whereArgs := db.BuildSimpleWhereClause(fields, len(joinArgs))

	var projectReport *limesrates.ProjectReport
	err := sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			projectUUID       string
			projectName       string
			projectParentUUID string
			serviceType       *limes.ServiceType
			ratesScrapedAt    *time.Time
			rateName          *limesrates.RateName
			limit             *uint64
			window            *limesrates.Window
			usageAsBigint     *string
		)
		err := rows.Scan(
			&projectUUID, &projectName, &projectParentUUID,
			&serviceType, &ratesScrapedAt,
			&rateName, &limit, &window, &usageAsBigint,
		)
		if err != nil {
			return err
		}

		// if we're moving to a different project, publish the finished report
		// first (and then allow for it to be GCd)
		if projectReport != nil && projectReport.UUID != projectUUID {
			err := submit(projectReport)
			if err != nil {
				return err
			}
			projectReport = nil
		}

		// start new project report when necessary
		if projectReport == nil {
			projectReport = &limesrates.ProjectReport{
				ProjectInfo: limes.ProjectInfo{
					Name:       projectName,
					UUID:       projectUUID,
					ParentUUID: projectParentUUID,
				},
				Services: make(limesrates.ProjectServiceReports),
			}
		}

		// if we don't have a valid service type, we're done with this result row
		if serviceType == nil || !cluster.HasService(*serviceType) {
			return nil
		}

		// start new service report when necessary
		srvReport := projectReport.Services[*serviceType]
		if srvReport == nil {
			srvReport = &limesrates.ProjectServiceReport{
				ServiceInfo: cluster.InfoForService(*serviceType).ForAPI(*serviceType),
				Rates:       make(limesrates.ProjectRateReports),
			}
			projectReport.Services[*serviceType] = srvReport

			if ratesScrapedAt != nil {
				t := limes.UnixEncodedTime{Time: *ratesScrapedAt}
				srvReport.ScrapedAt = &t
			}

			// fill new service report with default rate limits
			if svcConfig, err := cluster.Config.GetServiceConfigurationForType(*serviceType); err == nil {
				if len(svcConfig.RateLimits.ProjectDefault) > 0 {
					srvReport.Rates = make(limesrates.ProjectRateReports, len(svcConfig.RateLimits.ProjectDefault))
					for _, rateLimit := range svcConfig.RateLimits.ProjectDefault {
						srvReport.Rates[rateLimit.Name] = &limesrates.ProjectRateReport{
							RateInfo: cluster.InfoForRate(srvReport.Type, rateLimit.Name),
							Limit:    rateLimit.Limit,
							Window:   pointerTo(rateLimit.Window),
						}
					}
				}
			}
		}

		// if we don't have a rate name, we're done with this result row
		if rateName == nil {
			return nil
		}

		// create the rate report if necessary (rates with a limit will already have
		// one because of the default rate limit that was applied above, so this is
		// only relevant for rates that only have a usage)
		rateReport := srvReport.Rates[*rateName]
		if rateReport == nil && usageAsBigint != nil && *usageAsBigint != "" && cluster.HasUsageForRate(*serviceType, *rateName) {
			rateReport = &limesrates.ProjectRateReport{
				RateInfo: cluster.InfoForRate(*serviceType, *rateName),
			}
			srvReport.Rates[*rateName] = rateReport
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

	// submit final project report
	if projectReport != nil {
		return submit(projectReport)
	}
	return nil
}
