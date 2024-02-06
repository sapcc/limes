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
	"sort"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// NOTE: The subquery emulates the behavior of the old `usage` and `physical_usage` columns on `project_resources`.
var domainReportQuery1 = sqlext.SimplifyWhitespace(`
	WITH project_az_sums AS (
	  SELECT resource_id,
	         SUM(usage) AS usage,
	         SUM(COALESCE(physical_usage, usage)) AS physical_usage,
	         COUNT(physical_usage) > 0 AS has_physical_usage
	    FROM project_az_resources
	   GROUP BY resource_id
	)
	SELECT d.uuid, d.name, ps.type, pr.name, SUM(pr.quota), SUM(pas.usage),
	       SUM(GREATEST(pas.usage - pr.quota, 0)),
	       SUM(GREATEST(pr.backend_quota, 0)), MIN(pr.backend_quota) < 0,
	       SUM(pas.physical_usage), BOOL_OR(pas.has_physical_usage),
	       MIN(ps.scraped_at), MAX(ps.scraped_at)
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	  LEFT OUTER JOIN project_az_sums pas ON pas.resource_id = pr.id
	 WHERE %s GROUP BY d.uuid, d.name, ps.type, pr.name
`)

var domainReportQuery2 = sqlext.SimplifyWhitespace(`
	SELECT d.uuid, d.name, ds.type, dr.name, dr.quota
	  FROM domains d
	  LEFT OUTER JOIN domain_services ds ON ds.domain_id = d.id {{AND ds.type = $service_type}}
	  LEFT OUTER JOIN domain_resources dr ON dr.service_id = ds.id {{AND dr.name = $resource_name}}
	 WHERE %s
`)

var domainReportQuery3 = sqlext.SimplifyWhitespace(`
	WITH project_commitment_sums AS (
	  SELECT az_resource_id, SUM(amount) AS amount
	    FROM project_commitments
	   WHERE state = 'active'
	   GROUP BY az_resource_id
	)
	SELECT d.uuid, d.name, ps.type, pr.name, par.az,
	       SUM(par.quota), SUM(par.usage), SUM(GREATEST(0, COALESCE(pcs.amount, 0) - par.usage))
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	  JOIN project_az_resources par ON par.resource_id = pr.id
	  LEFT OUTER JOIN project_commitment_sums pcs ON pcs.az_resource_id = par.id
	 WHERE %s
	 GROUP BY d.uuid, d.name, ps.type, pr.name, par.az
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
	SELECT d.uuid, d.name, ps.type, pr.name, par.az,
	       pcs.duration, SUM(pcs.active), SUM(pcs.pending), SUM(pcs.planned)
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	  JOIN project_az_resources par ON par.resource_id = pr.id
	  JOIN project_commitment_sums pcs ON pcs.az_resource_id = par.id
	 WHERE %s
	 GROUP BY d.uuid, d.name, ps.type, pr.name, par.az, pcs.duration
`)

// GetDomains returns reports for all domains in the given cluster or, if
// domainID is non-nil, for that domain only.
func GetDomains(cluster *core.Cluster, domainID *db.DomainID, now time.Time, dbi db.Interface, filter Filter) ([]*limesresources.DomainReport, error) {
	clusterCanBurst := cluster.Config.Bursting.MaxMultiplier > 0

	var fields map[string]any
	if domainID != nil {
		fields = map[string]any{"d.id": *domainID}
	}

	//first query: data for projects in this domain
	domains := make(domains)
	queryStr, joinArgs := filter.PrepareQuery(domainReportQuery1)
	whereStr, whereArgs := db.BuildSimpleWhereClause(fields, len(joinArgs))
	err := sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			domainUUID           string
			domainName           string
			serviceType          *string
			resourceName         *string
			projectsQuota        *uint64
			usage                *uint64
			burstUsage           *uint64
			backendQuota         *uint64
			infiniteBackendQuota *bool
			physicalUsage        *uint64
			showPhysicalUsage    *bool
			minScrapedAt         *time.Time
			maxScrapedAt         *time.Time
		)
		err := rows.Scan(
			&domainUUID, &domainName, &serviceType, &resourceName,
			&projectsQuota, &usage, &burstUsage,
			&backendQuota, &infiniteBackendQuota,
			&physicalUsage, &showPhysicalUsage,
			&minScrapedAt, &maxScrapedAt,
		)
		if err != nil {
			return err
		}

		_, service, resource := domains.Find(cluster, domainUUID, domainName, serviceType, resourceName, now)

		if service != nil {
			service.MaxScrapedAt = mergeMaxTime(service.MaxScrapedAt, maxScrapedAt)
			service.MinScrapedAt = mergeMinTime(service.MinScrapedAt, minScrapedAt)
		}

		if resource != nil {
			if usage != nil {
				resource.Usage = *usage
			}
			if clusterCanBurst && burstUsage != nil {
				resource.BurstUsage = *burstUsage
			}
			if !resource.NoQuota {
				if projectsQuota != nil {
					resource.ProjectsQuota = projectsQuota
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
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	//second query: add domain quotas
	queryStr, joinArgs = filter.PrepareQuery(domainReportQuery2)
	whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))
	err = sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			domainUUID   string
			domainName   string
			serviceType  *string
			resourceName *string
			quota        *uint64
		)
		err := rows.Scan(
			&domainUUID, &domainName, &serviceType, &resourceName, &quota,
		)
		if err != nil {
			return err
		}

		_, _, resource := domains.Find(cluster, domainUUID, domainName, serviceType, resourceName, now)

		if resource != nil && quota != nil && !resource.NoQuota {
			resource.DomainQuota = quota
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	if filter.WithAZBreakdown {
		//third query: add basic AZ breakdown (quota, usage, unused commitments)
		queryStr, joinArgs = filter.PrepareQuery(domainReportQuery3)
		whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))
		err = sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
			var (
				domainUUID        string
				domainName        string
				serviceType       string
				resourceName      string
				az                limes.AvailabilityZone
				quota             *uint64
				usage             uint64
				unusedCommitments uint64
			)
			err := rows.Scan(
				&domainUUID, &domainName, &serviceType, &resourceName, &az,
				&quota, &usage, &unusedCommitments,
			)
			if err != nil {
				return err
			}

			_, _, resource := domains.Find(cluster, domainUUID, domainName, &serviceType, &resourceName, now)
			if resource == nil {
				return nil
			}
			if resource.PerAZ == nil {
				resource.PerAZ = make(limesresources.DomainAZResourceReports)
			}
			resource.PerAZ[az] = &limesresources.DomainAZResourceReport{
				Quota:             quota,
				Usage:             usage,
				UnusedCommitments: unusedCommitments,
			}
			return nil
		})
		if err != nil {
			return nil, err
		}

		//TODO: fourth query: add AZ breakdown by commitment duration (Committed, PendingCommitments, PlannedCommitments)
		queryStr, joinArgs = filter.PrepareQuery(domainReportQuery4)
		whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))
		err = sqlext.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
			var (
				domainUUID    string
				domainName    string
				serviceType   string
				resourceName  string
				az            limes.AvailabilityZone
				duration      limesresources.CommitmentDuration
				activeAmount  uint64
				pendingAmount uint64
				plannedAmount uint64
			)
			err := rows.Scan(
				&domainUUID, &domainName, &serviceType, &resourceName, &az,
				&duration, &activeAmount, &pendingAmount, &plannedAmount,
			)
			if err != nil {
				return err
			}

			_, _, resource := domains.Find(cluster, domainUUID, domainName, &serviceType, &resourceName, now)
			if resource == nil || resource.PerAZ[az] == nil {
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

		//project_az_resources always has entries for "any", even if the resource
		//is AZ-aware, because ApplyComputedProjectQuota needs somewhere to write
		//the base quotas; we ignore those entries here if the "any" usage is zero
		//and there are other AZs
		for _, domainReport := range domains {
			for _, srvReport := range domainReport.Services {
				for _, resReport := range srvReport.Resources {
					if len(resReport.PerAZ) >= 2 {
						reportInAny := resReport.PerAZ[limes.AvailabilityZoneAny]
						if reportInAny.Quota == nil && reportInAny.Usage == 0 {
							delete(resReport.PerAZ, limes.AvailabilityZoneAny)
						}
					}
				}
			}
		}
	}

	//flatten result (with stable order to keep the tests happy)
	uuids := make([]string, 0, len(domains))
	for uuid := range domains {
		uuids = append(uuids, uuid)
	}
	sort.Strings(uuids)
	result := make([]*limesresources.DomainReport, len(domains))
	for idx, uuid := range uuids {
		result[idx] = domains[uuid]
	}

	return result, nil
}

type domains map[string]*limesresources.DomainReport

func (d domains) Find(cluster *core.Cluster, domainUUID, domainName string, serviceType, resourceName *string, now time.Time) (*limesresources.DomainReport, *limesresources.DomainServiceReport, *limesresources.DomainResourceReport) {
	domain, exists := d[domainUUID]
	if !exists {
		domain = &limesresources.DomainReport{
			DomainInfo: limes.DomainInfo{
				UUID: domainUUID,
				Name: domainName,
			},
			Services: make(limesresources.DomainServiceReports),
		}
		d[domainUUID] = domain
	}

	if serviceType == nil {
		return domain, nil, nil
	}

	service, exists := domain.Services[*serviceType]
	if !exists {
		if !cluster.HasService(*serviceType) {
			return domain, nil, nil
		}
		service = &limesresources.DomainServiceReport{
			ServiceInfo: cluster.InfoForService(*serviceType),
			Resources:   make(limesresources.DomainResourceReports),
		}
		domain.Services[*serviceType] = service
	}

	if resourceName == nil {
		return domain, service, nil
	}

	resource, exists := service.Resources[*resourceName]
	if !exists {
		if !cluster.HasResource(*serviceType, *resourceName) {
			return domain, service, resource
		}
		localBehavior := cluster.BehaviorForResource(*serviceType, *resourceName, domainName)
		globalBehavior := cluster.BehaviorForResource(*serviceType, *resourceName, "")
		resource = &limesresources.DomainResourceReport{
			ResourceInfo: cluster.InfoForResource(*serviceType, *resourceName),
			Scaling:      globalBehavior.ToScalingBehavior(),
			Annotations:  localBehavior.Annotations,
		}
		if !resource.NoQuota {
			qdConfig := cluster.QuotaDistributionConfigForResource(*serviceType, *resourceName)
			resource.QuotaDistributionModel = qdConfig.Model
			resource.CommitmentConfig = globalBehavior.ToCommitmentConfig(now)
			//this default is used when no `domain_resources` entry exists for this resource
			defaultQuota := uint64(0)
			resource.DomainQuota = &defaultQuota
		}
		service.Resources[*resourceName] = resource
	}

	return domain, service, resource
}
