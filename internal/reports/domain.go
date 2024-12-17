/*******************************************************************************
*
* Copyright 2017-2024 SAP SE
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
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

var domainReportQuery1 = sqlext.SimplifyWhitespace(`
	SELECT d.id, d.uuid, d.name
	  FROM domains d
	 WHERE %s
`)

// NOTE: The subquery emulates the behavior of the old `usage` and `physical_usage` columns on `project_resources`.
var domainReportQuery2 = sqlext.SimplifyWhitespace(`
	WITH project_az_sums AS (
	  SELECT resource_id,
	         SUM(usage) AS usage,
	         SUM(COALESCE(physical_usage, usage)) AS physical_usage,
	         COUNT(physical_usage) > 0 AS has_physical_usage
	    FROM project_az_resources
	   GROUP BY resource_id
	)
	SELECT p.domain_id, ps.type, pr.name, SUM(pr.quota), SUM(pas.usage),
	       SUM(GREATEST(pr.backend_quota, 0)), MIN(pr.backend_quota) < 0,
	       SUM(pas.physical_usage), BOOL_OR(pas.has_physical_usage),
	       MIN(ps.scraped_at), MAX(ps.scraped_at)
	  FROM projects p
	  JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	  LEFT OUTER JOIN project_az_sums pas ON pas.resource_id = pr.id
	 WHERE %s GROUP BY p.domain_id, ps.type, pr.name
`)

var domainReportQuery3 = sqlext.SimplifyWhitespace(`
	WITH project_commitment_sums AS (
	  SELECT az_resource_id, SUM(amount) AS amount
	    FROM project_commitments
	   WHERE state = 'active'
	   GROUP BY az_resource_id
	)
	SELECT p.domain_id, ps.type, pr.name, par.az,
	       SUM(par.quota), SUM(par.usage),
	       SUM(GREATEST(0, COALESCE(pcs.amount, 0) - par.usage)),
	       SUM(GREATEST(0, par.usage - COALESCE(pcs.amount, 0)))
	  FROM projects p
	  JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	  JOIN project_az_resources par ON par.resource_id = pr.id
	  LEFT OUTER JOIN project_commitment_sums pcs ON pcs.az_resource_id = par.id
	 WHERE %s
	 GROUP BY p.domain_id, ps.type, pr.name, par.az
`)

var domainReportQuery4 = sqlext.SimplifyWhitespace(`
	WITH project_commitment_sums AS (
	  SELECT az_resource_id, duration,
	         COALESCE(SUM(amount) FILTER (WHERE state = 'active'), 0) AS active,
	         COALESCE(SUM(amount) FILTER (WHERE state = 'pending'), 0) AS pending,
	         COALESCE(SUM(amount) FILTER (WHERE state = 'planned'), 0) AS planned
	    FROM project_commitments
	   GROUP BY az_resource_id, duration
	)
	SELECT p.domain_id, ps.type, pr.name, par.az,
	       pcs.duration, SUM(pcs.active), SUM(pcs.pending), SUM(pcs.planned)
	  FROM projects p
	  JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	  JOIN project_az_resources par ON par.resource_id = pr.id
	  JOIN project_commitment_sums pcs ON pcs.az_resource_id = par.id
	 WHERE %s
	 GROUP BY p.domain_id, ps.type, pr.name, par.az, pcs.duration
`)

// GetDomains returns reports for all domains in the given cluster or, if
// domainID is non-nil, for that domain only.
func GetDomains(cluster *core.Cluster, domainID *db.DomainID, now time.Time, dbi db.Interface, filter Filter) ([]*limesresources.DomainReport, error) {
	var fields map[string]any
	if domainID != nil {
		fields = map[string]any{"d.id": *domainID}
	}

	// first query: basic information about domains
	//
	// (this is important because a filter like `?service=none` is supported,
	// but will yield no results at all in the other queries)
	domains := make(map[db.DomainID]*limesresources.DomainReport)
	whereStr, whereArgs := db.BuildSimpleWhereClause(fields, 0)
	err := sqlext.ForeachRow(dbi, fmt.Sprintf(domainReportQuery1, whereStr), whereArgs, func(rows *sql.Rows) error {
		var (
			domainID   db.DomainID
			domainInfo limes.DomainInfo
		)
		err := rows.Scan(&domainID, &domainInfo.UUID, &domainInfo.Name)
		if err != nil {
			return err
		}

		domains[domainID] = &limesresources.DomainReport{
			DomainInfo: domainInfo,
			Services:   make(limesresources.DomainServiceReports),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// in the other queries, the filter must target `p.domain_id` instead of `d.id`
	if domainID != nil {
		fields = map[string]any{"p.domain_id": *domainID}
	}

	// second query: data for projects in this domain
	queryStr, joinArgs := filter.PrepareQuery(domainReportQuery2)
	whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))
	err = sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			domainID             db.DomainID
			dbServiceType        db.ServiceType
			dbResourceName       liquid.ResourceName
			projectsQuota        *uint64
			usage                *uint64
			backendQuota         *uint64
			infiniteBackendQuota *bool
			physicalUsage        *uint64
			showPhysicalUsage    *bool
			minScrapedAt         *time.Time
			maxScrapedAt         *time.Time
		)
		err := rows.Scan(
			&domainID, &dbServiceType, &dbResourceName,
			&projectsQuota, &usage,
			&backendQuota, &infiniteBackendQuota,
			&physicalUsage, &showPhysicalUsage,
			&minScrapedAt, &maxScrapedAt,
		)
		if err != nil {
			return err
		}
		if domains[domainID] == nil {
			return nil
		}
		if !filter.Includes[dbServiceType][dbResourceName] {
			return nil
		}
		service, resource := findInDomainReport(domains[domainID], cluster, dbServiceType, dbResourceName, now)

		service.MaxScrapedAt = mergeMaxTime(service.MaxScrapedAt, maxScrapedAt)
		service.MinScrapedAt = mergeMinTime(service.MinScrapedAt, minScrapedAt)

		if usage != nil {
			resource.Usage = *usage
		}
		if !resource.NoQuota {
			if projectsQuota != nil {
				resource.ProjectsQuota = projectsQuota
				resource.DomainQuota = projectsQuota
				if backendQuota != nil && *projectsQuota != *backendQuota {
					resource.BackendQuota = backendQuota
				}
			}
			if infiniteBackendQuota != nil && *infiniteBackendQuota {
				resource.InfiniteBackendQuota = infiniteBackendQuota
			}
		}
		if showPhysicalUsage != nil && *showPhysicalUsage {
			resource.PhysicalUsage = physicalUsage
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	if filter.WithAZBreakdown {
		// third query: add basic AZ breakdown (quota, usage, unused commitments)
		queryStr, joinArgs = filter.PrepareQuery(domainReportQuery3)
		whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))
		err = sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
			var (
				domainID          db.DomainID
				dbServiceType     db.ServiceType
				dbResourceName    liquid.ResourceName
				az                limes.AvailabilityZone
				quota             *uint64
				usage             uint64
				unusedCommitments uint64
				uncommittedUsage  uint64
			)
			err := rows.Scan(
				&domainID, &dbServiceType, &dbResourceName, &az,
				&quota, &usage, &unusedCommitments, &uncommittedUsage,
			)
			if err != nil {
				return err
			}
			if domains[domainID] == nil {
				return nil
			}
			if !filter.Includes[dbServiceType][dbResourceName] {
				return nil
			}
			_, resource := findInDomainReport(domains[domainID], cluster, dbServiceType, dbResourceName, now)

			if resource.PerAZ == nil {
				resource.PerAZ = make(limesresources.DomainAZResourceReports)
			}
			resource.PerAZ[az] = &limesresources.DomainAZResourceReport{
				Quota:             quota,
				Usage:             usage,
				UnusedCommitments: unusedCommitments,
				UncommittedUsage:  uncommittedUsage,
			}
			return nil
		})
		if err != nil {
			return nil, err
		}

		// fourth query: add AZ breakdown by commitment duration (Committed, PendingCommitments, PlannedCommitments)
		queryStr, joinArgs = filter.PrepareQuery(domainReportQuery4)
		whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))
		err = sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
			var (
				domainID       db.DomainID
				dbServiceType  db.ServiceType
				dbResourceName liquid.ResourceName
				az             limes.AvailabilityZone
				duration       limesresources.CommitmentDuration
				activeAmount   uint64
				pendingAmount  uint64
				plannedAmount  uint64
			)
			err := rows.Scan(
				&domainID, &dbServiceType, &dbResourceName, &az,
				&duration, &activeAmount, &pendingAmount, &plannedAmount,
			)
			if err != nil {
				return err
			}
			if domains[domainID] == nil {
				return nil
			}
			if !filter.Includes[dbServiceType][dbResourceName] {
				return nil
			}
			_, resource := findInDomainReport(domains[domainID], cluster, dbServiceType, dbResourceName, now)

			if resource.PerAZ[az] == nil {
				return nil
			}
			azReport := resource.PerAZ[az]

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
			return nil, err
		}

		// project_az_resources always has entries for "any", even if the resource
		// is AZ-aware, because ApplyComputedProjectQuota needs somewhere to write
		// the base quotas; we ignore those entries here if the "any" usage is zero
		// and there are other AZs
		for _, domainReport := range domains {
			for _, srvReport := range domainReport.Services {
				for _, resReport := range srvReport.Resources {
					if len(resReport.PerAZ) >= 2 {
						reportInAny := resReport.PerAZ[limes.AvailabilityZoneAny]
						// AZSeparatedToplogy does not provide the "any" AZ.
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
	}

	// flatten result (with stable order to keep the tests happy)
	result := make([]*limesresources.DomainReport, 0, len(domains))
	for _, domainReport := range domains {
		result = append(result, domainReport)
	}
	slices.SortFunc(result, func(lhs, rhs *limesresources.DomainReport) int {
		return strings.Compare(lhs.UUID, rhs.UUID)
	})

	return result, nil
}

func findInDomainReport(domain *limesresources.DomainReport, cluster *core.Cluster, dbServiceType db.ServiceType, dbResourceName liquid.ResourceName, now time.Time) (*limesresources.DomainServiceReport, *limesresources.DomainResourceReport) {
	behavior := cluster.BehaviorForResource(dbServiceType, dbResourceName)
	apiIdentity := behavior.IdentityInV1API

	service, exists := domain.Services[apiIdentity.ServiceType]
	if !exists {
		service = &limesresources.DomainServiceReport{
			ServiceInfo: cluster.InfoForService(dbServiceType).ForAPI(apiIdentity.ServiceType),
			Resources:   make(limesresources.DomainResourceReports),
		}
		domain.Services[apiIdentity.ServiceType] = service
	}

	resource, exists := service.Resources[apiIdentity.Name]
	if !exists {
		resInfo := cluster.InfoForResource(dbServiceType, dbResourceName)
		resource = &limesresources.DomainResourceReport{
			ResourceInfo:     behavior.BuildAPIResourceInfo(apiIdentity.Name, resInfo),
			CommitmentConfig: behavior.ToCommitmentConfig(now),
		}
		if !resource.NoQuota {
			qdConfig := cluster.QuotaDistributionConfigForResource(dbServiceType, dbResourceName)
			resource.QuotaDistributionModel = qdConfig.Model
			// this default is used when no `domain_resources` entry exists for this resource
			defaultQuota := uint64(0)
			resource.DomainQuota = &defaultQuota
		}
		service.Resources[apiIdentity.Name] = resource
	}

	return service, resource
}
