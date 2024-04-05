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
	"strings"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

var clusterReportQuery1 = sqlext.SimplifyWhitespace(`
	WITH project_commitment_sums AS (
	  SELECT az_resource_id, SUM(amount) AS amount
	    FROM project_commitments
	   WHERE state = 'active'
	   GROUP BY az_resource_id
	)
	SELECT ps.type, pr.name, par.az,
	       SUM(par.usage), SUM(COALESCE(par.physical_usage, par.usage)), COUNT(par.physical_usage) > 0,
	       SUM(GREATEST(0, COALESCE(pcs.amount, 0) - par.usage)),
	       MIN(ps.scraped_at), MAX(ps.scraped_at)
	  FROM project_services ps
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	  LEFT OUTER JOIN project_az_resources par ON par.resource_id = pr.id
	  LEFT OUTER JOIN project_commitment_sums pcs ON pcs.az_resource_id = par.id
	 WHERE TRUE {{AND ps.type = $service_type}}
	 GROUP BY ps.type, pr.name, par.az
`)

// This was split from clusterReportQuery1 because the quota collection and burst usage calculation requires a different `GROUP BY`.
var clusterReportQuery1B = sqlext.SimplifyWhitespace(`
	WITH project_az_sums AS (
	  SELECT resource_id, SUM(usage) AS usage
	    FROM project_az_resources
	   GROUP BY resource_id
	)
	SELECT ps.type, pr.name, SUM(GREATEST(pas.usage - pr.quota, 0))
	  FROM project_services ps
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	  LEFT OUTER JOIN project_az_sums pas ON pas.resource_id = pr.id
	 WHERE TRUE {{AND ps.type = $service_type}}
	 GROUP BY ps.type, pr.name
`)

var clusterReportQuery2 = sqlext.SimplifyWhitespace(`
	SELECT ds.type, dr.name, SUM(dr.quota)
	  FROM domain_services ds
	  LEFT OUTER JOIN domain_resources dr ON dr.service_id = ds.id {{AND dr.name = $resource_name}}
	 WHERE TRUE {{AND ds.type = $service_type}}
	 GROUP BY ds.type, dr.name
`)

var clusterReportQuery3 = sqlext.SimplifyWhitespace(`
	SELECT cs.type, cr.name, car.az, car.raw_capacity, car.usage, car.subcapacities, cc.scraped_at
	  FROM cluster_services cs
	  LEFT OUTER JOIN cluster_resources cr ON cr.service_id = cs.id {{AND cr.name = $resource_name}}
	  LEFT OUTER JOIN cluster_az_resources car ON car.resource_id = cr.id
	  LEFT OUTER JOIN cluster_capacitors cc ON cc.capacitor_id = cr.capacitor_id
	 WHERE TRUE {{AND cs.type = $service_type}}
	 ORDER BY car.az
`)

var clusterReportQuery4 = sqlext.SimplifyWhitespace(`
	WITH project_commitment_sums AS (
	  SELECT az_resource_id, duration,
	         COALESCE(SUM(amount) FILTER (WHERE state = 'active'), 0) AS active,
	         COALESCE(SUM(amount) FILTER (WHERE state = 'pending'), 0) AS pending,
	         COALESCE(SUM(amount) FILTER (WHERE state = 'planned'), 0) AS planned
	    FROM project_commitments
	   GROUP BY az_resource_id, duration
	)
	SELECT ps.type, pr.name, par.az,
	       pcs.duration, SUM(pcs.active), SUM(pcs.pending), SUM(pcs.planned)
	  FROM project_services ps
	  JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	  JOIN project_az_resources par ON par.resource_id = pr.id
	  JOIN project_commitment_sums pcs ON pcs.az_resource_id = par.id
	 WHERE TRUE {{AND ps.type = $service_type}}
	 GROUP BY ps.type, pr.name, par.az, pcs.duration
`)

var clusterRateReportQuery1 = sqlext.SimplifyWhitespace(`
	SELECT type, MIN(rates_scraped_at), MAX(rates_scraped_at)
	  FROM project_services
	 WHERE TRUE {{AND type = $service_type}}
	 GROUP BY type
`)

// GetClusterResources returns the resource data report for the whole cluster.
func GetClusterResources(cluster *core.Cluster, now time.Time, dbi db.Interface, filter Filter) (*limesresources.ClusterReport, error) {
	report := &limesresources.ClusterReport{
		ClusterInfo: limes.ClusterInfo{
			ID: "current", // multi-cluster support has been removed; this value is only included for backwards-compatibility
		},
		Services: make(limesresources.ClusterServiceReports),
	}

	// first query: collect project usage data in these clusters
	queryStr, joinArgs := filter.PrepareQuery(clusterReportQuery1)
	err := sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			serviceType       limes.ServiceType
			resourceName      *limesresources.ResourceName
			availabilityZone  *limes.AvailabilityZone
			usage             *uint64
			physicalUsage     *uint64
			showPhysicalUsage *bool
			unusedCommitments *uint64
			minScrapedAt      *time.Time
			maxScrapedAt      *time.Time
		)
		err := rows.Scan(&serviceType, &resourceName, &availabilityZone,
			&usage, &physicalUsage, &showPhysicalUsage, &unusedCommitments,
			&minScrapedAt, &maxScrapedAt)
		if err != nil {
			return err
		}

		service, resource := findInClusterReport(cluster, report, serviceType, resourceName, now)

		if service != nil {
			service.MaxScrapedAt = mergeMaxTime(service.MaxScrapedAt, maxScrapedAt)
			service.MinScrapedAt = mergeMinTime(service.MinScrapedAt, minScrapedAt)
		}
		if resource == nil || availabilityZone == nil {
			return nil
		}

		resource.Usage += *usage
		if *showPhysicalUsage {
			sumPhysicalUsage := *physicalUsage
			if resource.PhysicalUsage != nil {
				sumPhysicalUsage += *resource.PhysicalUsage
			}
			resource.PhysicalUsage = &sumPhysicalUsage
		}

		if filter.WithAZBreakdown {
			if resource.PerAZ == nil {
				resource.PerAZ = make(limesresources.ClusterAZResourceReports)
			}
			azReport := limesresources.ClusterAZResourceReport{
				ProjectsUsage:     *usage,
				UnusedCommitments: *unusedCommitments,
			}
			if *showPhysicalUsage {
				azReport.PhysicalUsage = physicalUsage
			}
			resource.PerAZ[*availabilityZone] = &azReport
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// first query, addendum: collect burst usage data
	clusterCanBurst := cluster.Config.Bursting.MaxMultiplier > 0
	if clusterCanBurst {
		queryStr, joinArgs = filter.PrepareQuery(clusterReportQuery1B)
		err := sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
			var (
				serviceType  limes.ServiceType
				resourceName *limesresources.ResourceName
				burstUsage   *uint64
			)
			err := rows.Scan(&serviceType, &resourceName, &burstUsage)
			if err != nil {
				return err
			}

			if burstUsage != nil {
				_, resource := findInClusterReport(cluster, report, serviceType, resourceName, now)
				if resource != nil {
					resource.BurstUsage = *burstUsage
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	// second query: collect domain quota data in these clusters
	queryStr, joinArgs = filter.PrepareQuery(clusterReportQuery2)
	err = sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			serviceType  limes.ServiceType
			resourceName *limesresources.ResourceName
			quota        *uint64
		)
		err := rows.Scan(&serviceType, &resourceName, &quota)
		if err != nil {
			return err
		}

		_, resource := findInClusterReport(cluster, report, serviceType, resourceName, now)
		if resource != nil && quota != nil && !resource.NoQuota {
			resource.DomainsQuota = quota
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// third query: collect capacity data for these clusters
	queryStr, joinArgs = filter.PrepareQuery(clusterReportQuery3)
	if !filter.WithSubcapacities {
		queryStr = strings.Replace(queryStr, "car.subcapacities", "''", 1)
	}
	err = sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			serviceType       limes.ServiceType
			resourceName      *limesresources.ResourceName
			availabilityZone  *limes.AvailabilityZone
			rawCapacityInAZ   *uint64
			usageInAZ         *uint64
			subcapacitiesInAZ *string
			scrapedAt         *time.Time
		)
		err := rows.Scan(&serviceType, &resourceName, &availabilityZone,
			&rawCapacityInAZ, &usageInAZ, &subcapacitiesInAZ, &scrapedAt)
		if err != nil {
			return err
		}

		_, resource := findInClusterReport(cluster, report, serviceType, resourceName, now)

		if resource != nil {
			//NOTE: resource.Capacity is computed from this below once data for all AZs was ingested
			if resource.RawCapacity == nil {
				resource.RawCapacity = rawCapacityInAZ
			} else if rawCapacityInAZ != nil {
				resource.RawCapacity = pointerTo(*resource.RawCapacity + *rawCapacityInAZ)
			}
			if subcapacitiesInAZ != nil && *subcapacitiesInAZ != "" && filter.IsSubcapacityAllowed(serviceType, *resourceName) {
				mergeJSONListInto(&resource.Subcapacities, *subcapacitiesInAZ)
			}

			if availabilityZone != nil && rawCapacityInAZ != nil {
				azReport := limesresources.ClusterAvailabilityZoneReport{
					Name:  *availabilityZone,
					Usage: unwrapOrDefault(usageInAZ, 0),
				}
				overcommitFactor := cluster.BehaviorForResource(serviceType, *resourceName, "").OvercommitFactor
				azReport.Capacity = overcommitFactor.ApplyTo(*rawCapacityInAZ)
				if azReport.Capacity != *rawCapacityInAZ {
					azReport.RawCapacity = *rawCapacityInAZ
				}

				if resource.CapacityPerAZ == nil {
					resource.CapacityPerAZ = make(limesresources.ClusterAvailabilityZoneReports)
				}
				resource.CapacityPerAZ[*availabilityZone] = &azReport

				if filter.WithAZBreakdown {
					if resource.PerAZ == nil {
						resource.PerAZ = make(limesresources.ClusterAZResourceReports)
					}
					azReportV2 := resource.PerAZ[*availabilityZone]
					if azReportV2 == nil {
						azReportV2 = &limesresources.ClusterAZResourceReport{}
						resource.PerAZ[*availabilityZone] = azReportV2
					}
					azReportV2.Capacity = azReport.Capacity
					azReportV2.RawCapacity = azReport.RawCapacity
					azReportV2.Usage = usageInAZ
					azReportV2.Subcapacities = json.RawMessage(unwrapOrDefault(subcapacitiesInAZ, ""))
				}
			}
		}

		report.MaxScrapedAt = mergeMaxTime(report.MaxScrapedAt, scrapedAt)
		report.MinScrapedAt = mergeMinTime(report.MinScrapedAt, scrapedAt)

		return nil
	})
	if err != nil {
		return nil, err
	}

	if filter.WithAZBreakdown {
		// fourth query: collect commitment data that is broken down by commitment duration
		queryStr, joinArgs = filter.PrepareQuery(clusterReportQuery4)
		err = sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
			var (
				serviceType   limes.ServiceType
				resourceName  limesresources.ResourceName
				az            limes.AvailabilityZone
				duration      limesresources.CommitmentDuration
				activeAmount  uint64
				pendingAmount uint64
				plannedAmount uint64
			)
			err := rows.Scan(
				&serviceType, &resourceName, &az,
				&duration, &activeAmount, &pendingAmount, &plannedAmount,
			)
			if err != nil {
				return err
			}

			_, resource := findInClusterReport(cluster, report, serviceType, &resourceName, now)
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
	}

	//epilogue: perform some calculations that require the full sum over all AZs to be done
	for serviceType, service := range report.Services {
		for resourceName, resource := range service.Resources {
			overcommitFactor := cluster.BehaviorForResource(serviceType, resourceName, "").OvercommitFactor
			if overcommitFactor == 0 {
				resource.Capacity = resource.RawCapacity
				resource.RawCapacity = nil
			} else if resource.RawCapacity != nil {
				resource.Capacity = pointerTo(overcommitFactor.ApplyTo(*resource.RawCapacity))
			}

			if skipAZBreakdown(resource.CapacityPerAZ) {
				resource.CapacityPerAZ = nil
			}

			// project_az_resources always has entries for "any", even if the resource
			// is AZ-aware, because ApplyComputedProjectQuota needs somewhere to write
			// the base quotas; we ignore those entries here if the "any" usage is
			// zero and there are other AZs
			if len(resource.PerAZ) >= 2 {
				capaInAny := resource.PerAZ[limes.AvailabilityZoneAny]
				if capaInAny.Capacity == 0 && capaInAny.Usage == nil && capaInAny.ProjectsUsage == 0 {
					delete(resource.PerAZ, limes.AvailabilityZoneAny)
				}
			}
		}
	}

	return report, nil
}

// GetClusterRates returns the rate data report for the whole cluster.
func GetClusterRates(cluster *core.Cluster, dbi db.Interface, filter Filter) (*limesrates.ClusterReport, error) {
	report := &limesrates.ClusterReport{
		ClusterInfo: limes.ClusterInfo{
			ID: "current", // multi-cluster support has been removed; this value is only included for backwards-compatibility
		},
		Services: make(limesrates.ClusterServiceReports),
	}

	// collect scraping timestamp summaries
	queryStr, joinArgs := filter.PrepareQuery(clusterRateReportQuery1)
	err := sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			serviceType       limes.ServiceType
			minRatesScrapedAt *time.Time
			maxRatesScrapedAt *time.Time
		)
		err := rows.Scan(&serviceType, &minRatesScrapedAt, &maxRatesScrapedAt)
		if err != nil {
			return err
		}

		if !cluster.HasService(serviceType) {
			return nil
		}
		srvReport, exists := report.Services[serviceType]
		if !exists {
			srvReport = &limesrates.ClusterServiceReport{
				ServiceInfo: cluster.InfoForService(serviceType),
				Rates:       make(limesrates.ClusterRateReports),
			}
			report.Services[serviceType] = srvReport
		}

		srvReport.MaxScrapedAt = mergeMaxTime(srvReport.MaxScrapedAt, maxRatesScrapedAt)
		srvReport.MinScrapedAt = mergeMinTime(srvReport.MinScrapedAt, minRatesScrapedAt)

		return nil
	})
	if err != nil {
		return nil, err
	}

	// include global rate limits from configuration
	for _, serviceConfig := range cluster.Config.Services {
		srvReport := report.Services[serviceConfig.ServiceType]
		if srvReport != nil {
			for _, rateCfg := range serviceConfig.RateLimits.Global {
				srvReport.Rates[rateCfg.Name] = &limesrates.ClusterRateReport{
					RateInfo: limesrates.RateInfo{
						Name: rateCfg.Name,
						Unit: rateCfg.Unit,
					},
					Limit:  rateCfg.Limit,
					Window: rateCfg.Window,
				}
			}
		}
	}

	return report, nil
}

func findInClusterReport(cluster *core.Cluster, report *limesresources.ClusterReport, serviceType limes.ServiceType, resourceName *limesresources.ResourceName, now time.Time) (*limesresources.ClusterServiceReport, *limesresources.ClusterResourceReport) {
	service, exists := report.Services[serviceType]
	if !exists {
		if !cluster.HasService(serviceType) {
			return nil, nil
		}
		service = &limesresources.ClusterServiceReport{
			ServiceInfo: cluster.InfoForService(serviceType),
			Resources:   make(limesresources.ClusterResourceReports),
		}
		report.Services[serviceType] = service
	}

	if resourceName == nil {
		return service, nil
	}

	resource, exists := service.Resources[*resourceName]
	if !exists {
		if !cluster.HasResource(serviceType, *resourceName) {
			return service, nil
		}
		globalBehavior := cluster.BehaviorForResource(serviceType, *resourceName, "")
		resource = &limesresources.ClusterResourceReport{
			ResourceInfo:     cluster.InfoForResource(serviceType, *resourceName),
			CommitmentConfig: globalBehavior.ToCommitmentConfig(now),
		}
		if !resource.ResourceInfo.NoQuota {
			qdConfig := cluster.QuotaDistributionConfigForResource(serviceType, *resourceName)
			resource.QuotaDistributionModel = qdConfig.Model
			// We need to set a default value here. Otherwise zero values will never
			// be reported when there are no `domain_resources` entries to aggregate
			// over.
			defaultDomainsQuota := uint64(0)
			resource.DomainsQuota = &defaultDomainsQuota
		}
		service.Resources[*resourceName] = resource
	}

	return service, resource
}

func skipAZBreakdown(azReports limesresources.ClusterAvailabilityZoneReports) bool {
	for az := range azReports {
		if az != limes.AvailabilityZoneAny {
			return false
		}
	}
	return true
}
