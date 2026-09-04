// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/sqlext"
	"go.xyrillian.de/gg/options"
	"go.xyrillian.de/oblast"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

var domainReportQuery1 = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	WITH project_commitment_sums AS (
	  SELECT project_id, az_resource_id, SUM(amount) AS amount
	    FROM project_commitments
	   WHERE status = {{liquid.CommitmentStatusConfirmed}}
	   GROUP BY project_id, az_resource_id
	)
	SELECT p.domain_id, s.type, r.name, azr.az,
	       SUM(pazr.quota) as sum_quota, SUM(pazr.usage) as sum_usage,
	       SUM(GREATEST(0, COALESCE(pcs.amount, 0) - pazr.usage)) AS unused_commitments,
	       SUM(GREATEST(0, pazr.usage - COALESCE(pcs.amount, 0))) AS uncommitted_usage,
		   SUM(GREATEST(pazr.backend_quota, 0)) as backend_quota, MIN(pazr.backend_quota) < 0 as infinite_backend_quota,
		   SUM(COALESCE(pazr.physical_usage, pazr.usage)) as physical_usage, COUNT(pazr.physical_usage) > 0 as has_physical_usage,
	       MIN(ps.scraped_at) as min_scraped_at, MAX(ps.scraped_at) as max_scraped_at
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

var domainReportQuery2 = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
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
func GetDomains(ctx context.Context, cluster *core.Cluster, domainID *db.DomainID, now time.Time, dbi db.Interface, filter Filter, sis core.ServiceInfoSnapshot) ([]*limesresources.DomainReport, error) {
	var (
		fields    map[string]any
		whereArgs []any
	)
	whereStr := "TRUE"
	if domainID != nil {
		fields = map[string]any{"d.id": *domainID}
		whereStr = "id = $1"
		whereArgs = []any{*domainID}
	}

	// first query: basic information about domains
	//
	// (this is important because a filter like `?service=none` is supported,
	// but will yield no results at all in the other queries)
	domains := make(map[db.DomainID]*limesresources.DomainReport)
	err := db.DomainStore.SelectWhere(ctx, dbi, whereStr, whereArgs...).Foreach(func(d db.Domain) error {
		domains[d.ID] = &limesresources.DomainReport{
			UUID:     d.UUID,
			Name:     d.Name,
			Services: make(limesresources.DomainServiceReports),
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
	queryStr := domainReportQuery1
	if !filter.WithAZBreakdown {
		queryStr = strings.Replace(queryStr, "%s", db.ExpandEnumPlaceholders("azr.az = {{liquid.AvailabilityZoneTotal}} AND %s"), 1)
	}
	queryStr, joinArgs := filter.PrepareQuery(queryStr)
	whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))

	type record struct {
		DomainID             db.DomainID            `db:"domain_id"`
		DBServiceType        db.ServiceType         `db:"type"`
		DBResourceName       liquid.ResourceName    `db:"name"`
		AZ                   limes.AvailabilityZone `db:"az"`
		Quota                *uint64                `db:"sum_quota"`
		Usage                *uint64                `db:"sum_usage"`
		UnusedCommitments    *uint64                `db:"unused_commitments"`
		UncommittedUsage     *uint64                `db:"uncommitted_usage"`
		BackendQuota         *uint64                `db:"backend_quota"`
		InfiniteBackendQuota *bool                  `db:"infinite_backend_quota"`
		PhysicalUsage        *uint64                `db:"physical_usage"`
		ShowPhysicalUsage    *bool                  `db:"has_physical_usage"`
		MinScrapedAt         *time.Time             `db:"min_scraped_at"`
		MaxScrapedAt         *time.Time             `db:"max_scraped_at"`
	}
	err = oblast.MustNewStore[record](oblast.PostgresDialect()).Select(ctx, cluster.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...)...).Foreach(func(r record) error {
		if domains[r.DomainID] == nil {
			return nil
		}
		if !filter.Includes[r.DBServiceType][r.DBResourceName] {
			return nil
		}
		serviceReport, resourceReport := findInDomainReport(domains[r.DomainID], cluster, r.DBServiceType, r.DBResourceName, now, sis)

		if r.AZ == liquid.AvailabilityZoneTotal {
			// we ignore when a resource can't be found in the app layer yet, it will appear with empty values
			resource, _ := sis.GetResourceForPath(db.ResourcePath{ServiceType: r.DBServiceType, ResourceName: r.DBResourceName})
			serviceReport.MaxScrapedAt = mergeMaxTime(serviceReport.MaxScrapedAt, r.MaxScrapedAt)
			serviceReport.MinScrapedAt = mergeMinTime(serviceReport.MinScrapedAt, r.MinScrapedAt)

			if r.Usage != nil {
				resourceReport.Usage = *r.Usage
			}
			if !resourceReport.NoQuota {
				if r.Quota != nil && resource.Topology != liquid.AZSeparatedTopology {
					resourceReport.ProjectsQuota = r.Quota
					resourceReport.DomainQuota = r.Quota
					if r.BackendQuota != nil && *r.Quota != *r.BackendQuota {
						resourceReport.BackendQuota = r.BackendQuota
					}
				}
				if r.InfiniteBackendQuota != nil && *r.InfiniteBackendQuota {
					resourceReport.InfiniteBackendQuota = r.InfiniteBackendQuota
				}
			}
			if r.ShowPhysicalUsage != nil && *r.ShowPhysicalUsage {
				resourceReport.PhysicalUsage = r.PhysicalUsage
			}
		}

		if filter.WithAZBreakdown && r.AZ != liquid.AvailabilityZoneTotal {
			if resourceReport.PerAZ == nil {
				resourceReport.PerAZ = make(limesresources.DomainAZResourceReports)
			}
			sanitizedQuota := options.FromPointer(r.Quota).UnwrapOr(0)
			resourceReport.PerAZ[r.AZ] = &limesresources.DomainAZResourceReport{
				Quota:             &sanitizedQuota,
				Usage:             *r.Usage,
				UnusedCommitments: *r.UnusedCommitments,
				UncommittedUsage:  *r.UncommittedUsage,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// fourth query: add AZ breakdown by commitment duration (Committed, PendingCommitments, PlannedCommitments)
	if filter.WithAZBreakdown {
		queryStr, joinArgs = filter.PrepareQuery(domainReportQuery2)
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
			_, resourceReport := findInDomainReport(domains[domainID], cluster, dbServiceType, dbResourceName, now, sis)

			if resourceReport.PerAZ[az] == nil {
				return nil
			}
			azReport := resourceReport.PerAZ[az]

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

func findInDomainReport(domain *limesresources.DomainReport, cluster *core.Cluster, dbServiceType db.ServiceType, dbResourceName liquid.ResourceName, now time.Time, sis core.ServiceInfoSnapshot) (*limesresources.DomainServiceReport, *limesresources.DomainResourceReport) {
	behavior := cluster.BehaviorForResource(dbServiceType, dbResourceName)
	apiIdentity := behavior.IdentityInV1API

	serviceReport, exists := domain.Services[apiIdentity.ServiceType]
	if !exists {
		srvCfg, _ := cluster.Config.GetLiquidConfigurationForType(dbServiceType)
		serviceReport = &limesresources.DomainServiceReport{
			Type: apiIdentity.ServiceType, Area: srvCfg.Area,
			Resources: make(limesresources.DomainResourceReports),
		}
		domain.Services[apiIdentity.ServiceType] = serviceReport
	}

	resourceReport, exists := serviceReport.Resources[apiIdentity.Name]
	if !exists {
		// we ignore when a resource can't be found in the app layer yet, it will appear with empty values
		resource, _ := sis.GetResourceForPath(db.ResourcePath{ServiceType: dbServiceType, ResourceName: dbResourceName})
		resourceReport = &limesresources.DomainResourceReport{
			ResourceInfo:     behavior.BuildAPIResourceInfo(apiIdentity.Name, resource),
			CommitmentConfig: cluster.CommitmentBehaviorForResource(dbServiceType, dbResourceName).ForDomain(domain.Name).ForAPI(now).AsPointer(),
		}
		if !resourceReport.NoQuota {
			qdConfig := cluster.QuotaDistributionConfigForResource(dbServiceType, dbResourceName)
			resourceReport.QuotaDistributionModel = qdConfig.Model
			// this default is used when no `domain_resources` entry exists for this resourceReport
			defaultQuota := uint64(0)
			resourceReport.DomainQuota = &defaultQuota
		}
		serviceReport.Resources[apiIdentity.Name] = resourceReport
	}

	return serviceReport, resourceReport
}
