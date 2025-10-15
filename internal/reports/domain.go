// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

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

// NOTE: The select emulates the behavior of the old `usage` and `physical_usage` columns on `project_resources`.
var domainReportQuery2 = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	WITH project_commitment_sums AS (
	  SELECT project_id, az_resource_id, SUM(amount) AS amount
	    FROM project_commitments
	   WHERE status = {{liquid.CommitmentStatusConfirmed}}
	   GROUP BY project_id, az_resource_id
	)
	SELECT p.domain_id, s.type, r.name, azr.az,
	       SUM(pazr.quota), SUM(pazr.usage),
	       SUM(GREATEST(0, COALESCE(pcs.amount, 0) - pazr.usage)) AS unused_commitments,
	       SUM(GREATEST(0, pazr.usage - COALESCE(pcs.amount, 0))) AS uncommitted_usage,
		   SUM(GREATEST(pazr.backend_quota, 0)) as backend_quota, MIN(pazr.backend_quota) < 0 as infinite_backend_quota,
		   SUM(COALESCE(pazr.physical_usage, pazr.usage)) as physical_usage, COUNT(pazr.physical_usage) > 0 as has_physical_usage,
	       MIN(ps.scraped_at), MAX(ps.scraped_at)
	  FROM services s
	  JOIN resources r ON r.service_id = s.id {{AND r.name = $resource_name}}
	  JOIN az_resources azr ON azr.resource_id = r.id
	  CROSS JOIN projects p
	  JOIN project_services ps ON ps.project_id = p.id AND ps.service_id = s.id
	  JOIN project_resources pr ON pr.project_id = p.id AND pr.resource_id = r.id
	  JOIN project_az_resources pazr ON pazr.project_id = p.id AND pazr.az_resource_id = azr.id
	  LEFT OUTER JOIN project_commitment_sums pcs ON pcs.az_resource_id = azr.id AND pcs.project_id = p.id
	 WHERE %s {{AND s.type = $service_type}}
	 GROUP BY p.domain_id, s.type, r.name, azr.az
`))

var domainReportQuery4 = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	WITH project_commitment_sums AS (
	  SELECT project_id, az_resource_id, duration,
	         COALESCE(SUM(amount) FILTER (WHERE status = {{liquid.CommitmentStatusConfirmed}}), 0) AS confirmed,
	         COALESCE(SUM(amount) FILTER (WHERE status = {{liquid.CommitmentStatusPending}}), 0) AS pending,
	         COALESCE(SUM(amount) FILTER (WHERE status = {{liquid.CommitmentStatusPlanned}}), 0) AS planned
	    FROM project_commitments
	   GROUP BY project_id, az_resource_id, duration
	)
	SELECT p.domain_id, s.type, r.name, azr.az,
	       pcs.duration, SUM(pcs.confirmed), SUM(pcs.pending), SUM(pcs.planned)
	  FROM services s
	  JOIN resources r ON r.service_id = s.id {{AND r.name = $resource_name}}
	  JOIN az_resources azr ON azr.resource_id = r.id AND azr.az != {{liquid.AvailabilityZoneTotal}}
	  CROSS JOIN projects p
	  JOIN project_services ps ON ps.project_id = p.id AND ps.service_id = s.id
	  JOIN project_resources pr ON pr.project_id = p.id AND pr.resource_id = r.id
	  JOIN project_commitment_sums pcs ON pcs.az_resource_id = azr.id AND pcs.project_id = p.id
	 WHERE %s {{AND s.type = $service_type}}
	 GROUP BY p.domain_id, s.type, r.name, azr.az, pcs.duration
`))

// GetDomains returns reports for all domains in the given cluster or, if
// domainID is non-nil, for that domain only.
func GetDomains(cluster *core.Cluster, domainID *db.DomainID, now time.Time, dbi db.Interface, filter Filter, serviceInfos map[db.ServiceType]liquid.ServiceInfo) ([]*limesresources.DomainReport, error) {
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

	// second query: add resource and az-level values
	queryStr := domainReportQuery2
	if !filter.WithAZBreakdown {
		queryStr = strings.Replace(queryStr, "%s", db.ExpandEnumPlaceholders("azr.az = {{liquid.AvailabilityZoneTotal}} AND %s"), 1)
	}
	queryStr, joinArgs := filter.PrepareQuery(queryStr)
	whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))
	err = sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			domainID             db.DomainID
			dbServiceType        db.ServiceType
			dbResourceName       liquid.ResourceName
			az                   limes.AvailabilityZone
			quota                *uint64
			usage                *uint64
			unusedCommitments    *uint64
			uncommittedUsage     *uint64
			backendQuota         *uint64
			infiniteBackendQuota *bool
			physicalUsage        *uint64
			showPhysicalUsage    *bool
			minScrapedAt         *time.Time
			maxScrapedAt         *time.Time
		)
		err := rows.Scan(
			&domainID, &dbServiceType, &dbResourceName, &az,
			&quota, &usage, &unusedCommitments, &uncommittedUsage,
			&backendQuota, &infiniteBackendQuota, &physicalUsage, &showPhysicalUsage, &minScrapedAt, &maxScrapedAt,
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
		service, resource := findInDomainReport(domains[domainID], cluster, dbServiceType, dbResourceName, now, serviceInfos)

		if az == liquid.AvailabilityZoneTotal {
			serviceInfo := core.InfoForService(serviceInfos, dbServiceType)
			resInfo := core.InfoForResource(serviceInfo, dbResourceName)

			service.MaxScrapedAt = mergeMaxTime(service.MaxScrapedAt, maxScrapedAt)
			service.MinScrapedAt = mergeMinTime(service.MinScrapedAt, minScrapedAt)

			if usage != nil {
				resource.Usage = *usage
			}
			if !resource.NoQuota {
				if quota != nil && resInfo.Topology != liquid.AZSeparatedTopology {
					resource.ProjectsQuota = quota
					resource.DomainQuota = quota
					if backendQuota != nil && *quota != *backendQuota {
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
		}

		if filter.WithAZBreakdown && az != liquid.AvailabilityZoneTotal {
			if resource.PerAZ == nil {
				resource.PerAZ = make(limesresources.DomainAZResourceReports)
			}
			sanitizedQuota := uint64(0)
			if quota != nil {
				sanitizedQuota = *quota
			}
			resource.PerAZ[az] = &limesresources.DomainAZResourceReport{
				Quota:             &sanitizedQuota,
				Usage:             *usage,
				UnusedCommitments: *unusedCommitments,
				UncommittedUsage:  *uncommittedUsage,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// fourth query: add AZ breakdown by commitment duration (Committed, PendingCommitments, PlannedCommitments)
	if filter.WithAZBreakdown {
		queryStr, joinArgs = filter.PrepareQuery(domainReportQuery4)
		whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))
		err = sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
			var (
				domainID        db.DomainID
				dbServiceType   db.ServiceType
				dbResourceName  liquid.ResourceName
				az              limes.AvailabilityZone
				duration        limesresources.CommitmentDuration
				confirmedAmount uint64
				pendingAmount   uint64
				plannedAmount   uint64
			)
			err := rows.Scan(
				&domainID, &dbServiceType, &dbResourceName, &az,
				&duration, &confirmedAmount, &pendingAmount, &plannedAmount,
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
			_, resource := findInDomainReport(domains[domainID], cluster, dbServiceType, dbResourceName, now, serviceInfos)

			if resource.PerAZ[az] == nil {
				return nil
			}
			azReport := resource.PerAZ[az]

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
			return nil, err
		}
	}
	// project_az_resources always has entries for "any", even if the resource
	// is AZ-aware, because ApplyComputedProjectQuota needs somewhere to write
	// the base quotas; we ignore those entries here if the "any" usage is zero
	// and there are other AZs
	// "unknown" may exist because the location for usages or capacities may be
	// unknown, but we only show it if there is non-zero quota/usage.
	for _, domainReport := range domains {
		for _, srvReport := range domainReport.Services {
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

func findInDomainReport(domain *limesresources.DomainReport, cluster *core.Cluster, dbServiceType db.ServiceType, dbResourceName liquid.ResourceName, now time.Time, serviceInfos map[db.ServiceType]liquid.ServiceInfo) (*limesresources.DomainServiceReport, *limesresources.DomainResourceReport) {
	behavior := cluster.BehaviorForResource(dbServiceType, dbResourceName)
	apiIdentity := behavior.IdentityInV1API

	service, exists := domain.Services[apiIdentity.ServiceType]
	if !exists {
		srvCfg, _ := cluster.Config.GetLiquidConfigurationForType(dbServiceType)
		service = &limesresources.DomainServiceReport{
			ServiceInfo: limes.ServiceInfo{Type: apiIdentity.ServiceType, Area: srvCfg.Area},
			Resources:   make(limesresources.DomainResourceReports),
		}
		domain.Services[apiIdentity.ServiceType] = service
	}

	resource, exists := service.Resources[apiIdentity.Name]
	if !exists {
		serviceInfo := core.InfoForService(serviceInfos, dbServiceType)
		resInfo := core.InfoForResource(serviceInfo, dbResourceName)
		resource = &limesresources.DomainResourceReport{
			ResourceInfo:     behavior.BuildAPIResourceInfo(apiIdentity.Name, resInfo),
			CommitmentConfig: cluster.CommitmentBehaviorForResource(dbServiceType, dbResourceName).ForDomain(domain.Name).ForAPI(now).AsPointer(),
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
