// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

var clusterReportQuery1 = sqlext.SimplifyWhitespace(`
	WITH project_commitment_sums AS (
	  SELECT project_id, az_resource_id, SUM(amount) AS amount
	    FROM project_commitments
	   WHERE state = 'active'
	   GROUP BY project_id, az_resource_id
	)
	SELECT cs.type, cr.name, cazr.az,
	       SUM(pazr.usage), SUM(COALESCE(pazr.physical_usage, pazr.usage)), COUNT(pazr.physical_usage) > 0,
	       SUM(GREATEST(0, COALESCE(pcs.amount, 0) - pazr.usage)),
	       SUM(GREATEST(0, pazr.usage - COALESCE(pcs.amount, 0))),
	       MIN(ps.SCRAPED_AT), MAX(ps.SCRAPED_AT)
	  FROM cluster_services cs
	  JOIN cluster_resources cr ON cr.service_id = cs.id {{AND cr.name = $resource_name}}
	  JOIN cluster_az_resources cazr ON cazr.resource_id = cr.id
	  JOIN project_services ps ON ps.service_id = cs.id
	  -- no left join, entries will only appear when there is some project level entry
	  JOIN project_az_resources pazr ON pazr.az_resource_id = cazr.id AND pazr.project_id = ps.project_id
	  LEFT OUTER JOIN project_commitment_sums pcs ON pcs.az_resource_id = cazr.id AND pcs.project_id = pazr.project_id
	 WHERE TRUE {{AND cs.type = $service_type}}
	 GROUP BY cs.type, cr.name, cazr.az
`)

var clusterReportQuery2 = sqlext.SimplifyWhitespace(`
	SELECT cs.type, cr.name, SUM(pr.quota)
	  FROM cluster_services cs
	  JOIN cluster_resources cr ON cr.service_id = cs.id {{AND cr.name = $resource_name}}
	  JOIN project_resources pr ON pr.resource_id = cr.id
	 WHERE TRUE {{AND cs.type = $service_type}}
	 GROUP BY cs.type, cr.name
`)

var clusterReportQuery3 = sqlext.SimplifyWhitespace(`
	SELECT cs.type, cr.name, cazr.az, cazr.raw_capacity, cazr.usage, cazr.subcapacities, cs.scraped_at
	  FROM cluster_services cs
	  JOIN cluster_resources cr ON cr.service_id = cs.id {{AND cr.name = $resource_name}}
	  LEFT OUTER JOIN cluster_az_resources cazr ON cazr.resource_id = cr.id
	 WHERE TRUE {{AND cs.type = $service_type}}
	 ORDER BY cazr.az
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
	SELECT cs.type, cr.name, cazr.az,
	       pcs.duration, SUM(pcs.active), SUM(pcs.pending), SUM(pcs.planned)
	  FROM cluster_services cs
	  JOIN cluster_resources cr ON cr.service_id = cs.id {{AND cr.name = $resource_name}}
	  JOIN cluster_az_resources cazr ON cazr.resource_id = cr.id
	  JOIN project_commitment_sums pcs ON pcs.az_resource_id = cazr.id
	 WHERE TRUE {{AND cs.type = $service_type}}
	 GROUP BY cs.type, cr.name, cazr.az, pcs.duration
`)

var clusterRateReportQuery1 = sqlext.SimplifyWhitespace(`
	SELECT cs.type, cra.name, MIN(ps.scraped_at), MAX(ps.scraped_at)
	  FROM cluster_services cs
	  JOIN cluster_rates cra ON cra.service_id = cs.id
	  JOIN project_services ps ON ps.service_id = cs.id
	  -- TODO: this join reduces the result set to the rates which have been scraped.
	  -- At some point, we want to have the scraped_at statistics per service - not considering rates or resources.
	  JOIN project_rates pra ON pra.rate_id = cra.id AND ps.project_id = pra.project_id
	 WHERE TRUE {{AND cs.type = $service_type}}
	 GROUP BY cs.type, cra.name
`)

// GetClusterResources returns the resource data report for the whole cluster.
func GetClusterResources(cluster *core.Cluster, now time.Time, dbi db.Interface, filter Filter, serviceInfos map[db.ServiceType]liquid.ServiceInfo) (*limesresources.ClusterReport, error) {
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
			dbServiceType     db.ServiceType
			dbResourceName    liquid.ResourceName
			availabilityZone  *limes.AvailabilityZone
			usage             *uint64
			physicalUsage     *uint64
			showPhysicalUsage *bool
			unusedCommitments *uint64
			uncommittedUsage  *uint64
			minScrapedAt      *time.Time
			maxScrapedAt      *time.Time
		)
		err := rows.Scan(&dbServiceType, &dbResourceName, &availabilityZone,
			&usage, &physicalUsage, &showPhysicalUsage, &unusedCommitments, &uncommittedUsage,
			&minScrapedAt, &maxScrapedAt)
		if err != nil {
			return err
		}
		if _, exists := cluster.Config.Liquids[dbServiceType]; !filter.Includes[dbServiceType][dbResourceName] || !exists {
			return nil
		}
		service, resource, _ := findInClusterReport(cluster, report, dbServiceType, dbResourceName, now, serviceInfos)

		service.MaxScrapedAt = mergeMaxTime(service.MaxScrapedAt, maxScrapedAt)
		service.MinScrapedAt = mergeMinTime(service.MinScrapedAt, minScrapedAt)

		if availabilityZone == nil {
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
				UncommittedUsage:  *uncommittedUsage,
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

	// second query: collect quota data (this is a separate query because we need
	// to stop at the resource level and not break down by AZ)
	queryStr, joinArgs = filter.PrepareQuery(clusterReportQuery2)
	err = sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			dbServiceType  db.ServiceType
			dbResourceName liquid.ResourceName
			quota          *uint64
		)
		err := rows.Scan(&dbServiceType, &dbResourceName, &quota)
		if err != nil {
			return err
		}
		if _, exists := cluster.Config.Liquids[dbServiceType]; !filter.Includes[dbServiceType][dbResourceName] || !exists {
			return nil
		}
		_, resource, _ := findInClusterReport(cluster, report, dbServiceType, dbResourceName, now, serviceInfos)

		if quota != nil && !resource.NoQuota {
			// NOTE: This is called "DomainsQuota" for historical reasons, but it is actually
			// the sum of all project quotas, since quotas only exist on project level by now.
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
		queryStr = strings.Replace(queryStr, "cazr.subcapacities", "''", 1)
	}
	err = sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			dbServiceType     db.ServiceType
			dbResourceName    liquid.ResourceName
			availabilityZone  *limes.AvailabilityZone
			rawCapacityInAZ   *uint64
			usageInAZ         *uint64
			subcapacitiesInAZ *string
			scrapedAt         *time.Time
		)
		err := rows.Scan(&dbServiceType, &dbResourceName, &availabilityZone,
			&rawCapacityInAZ, &usageInAZ, &subcapacitiesInAZ, &scrapedAt)
		if err != nil {
			return err
		}
		if _, exists := cluster.Config.Liquids[dbServiceType]; !filter.Includes[dbServiceType][dbResourceName] || !exists {
			return nil
		}
		_, resource, behavior := findInClusterReport(cluster, report, dbServiceType, dbResourceName, now, serviceInfos)

		// NOTE: resource.Capacity is computed from this below once data for all AZs was ingested
		if resource.RawCapacity == nil {
			resource.RawCapacity = rawCapacityInAZ
		} else if rawCapacityInAZ != nil {
			resource.RawCapacity = pointerTo(*resource.RawCapacity + *rawCapacityInAZ)
		}
		if subcapacitiesInAZ != nil && *subcapacitiesInAZ != "" && filter.IsSubcapacityAllowed(dbServiceType, dbResourceName) {
			translate := behavior.TranslationRuleInV1API.TranslateSubcapacities
			if translate != nil {
				serviceInfo := core.InfoForService(serviceInfos, dbServiceType)
				resInfo := core.InfoForResource(serviceInfo, dbResourceName)
				*subcapacitiesInAZ, err = translate(*subcapacitiesInAZ, *availabilityZone, dbResourceName, resInfo)
				if err != nil {
					return fmt.Errorf("could not apply TranslationRule to subcapacities in %s/%s/%s: %w",
						dbServiceType, dbResourceName, *availabilityZone, err)
				}
			}
			mergeJSONListInto(&resource.Subcapacities, *subcapacitiesInAZ)
		}

		if availabilityZone != nil && rawCapacityInAZ != nil {
			azReport := limesresources.ClusterAvailabilityZoneReport{
				Name:  *availabilityZone,
				Usage: unwrapOrDefault(usageInAZ, 0),
			}
			overcommitFactor := behavior.OvercommitFactor
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
				dbServiceType  db.ServiceType
				dbResourceName liquid.ResourceName
				az             limes.AvailabilityZone
				duration       limesresources.CommitmentDuration
				activeAmount   uint64
				pendingAmount  uint64
				plannedAmount  uint64
			)
			err := rows.Scan(
				&dbServiceType, &dbResourceName, &az,
				&duration, &activeAmount, &pendingAmount, &plannedAmount,
			)
			if err != nil {
				return err
			}
			if _, exists := cluster.Config.Liquids[dbServiceType]; !filter.Includes[dbServiceType][dbResourceName] || !exists {
				return nil
			}
			_, resource, _ := findInClusterReport(cluster, report, dbServiceType, dbResourceName, now, serviceInfos)

			azReport := resource.PerAZ[az]
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
			return nil, err
		}
	}

	// epilogue: perform some calculations that require the full sum over all AZs to be done
	nm := core.BuildResourceNameMapping(cluster, serviceInfos)
	for apiServiceType, service := range report.Services {
		for apiResourceName, resource := range service.Resources {
			dbServiceType, dbResourceName, exists := nm.MapFromV1API(apiServiceType, apiResourceName)
			if !exists {
				// defense in depth: should not happen; we should not have created entries for non-existent resources
				continue
			}

			overcommitFactor := cluster.BehaviorForResource(dbServiceType, dbResourceName).OvercommitFactor
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
			// zero and there are other AZs.
			// "unknown" may exist because the location for usages or capacities may be
			// unknown.
			if len(resource.CapacityPerAZ) >= 2 {
				capaInUnknown := resource.CapacityPerAZ[limes.AvailabilityZoneUnknown]
				if capaInUnknown != nil && capaInUnknown.Capacity == 0 && capaInUnknown.Usage == 0 && capaInUnknown.RawCapacity == 0 {
					delete(resource.CapacityPerAZ, limes.AvailabilityZoneUnknown)
				}
				// defense in depth: any should never have capacity, but better check it too
				capaInAny := resource.CapacityPerAZ[limes.AvailabilityZoneAny]
				if capaInAny != nil && capaInAny.Capacity == 0 && capaInAny.Usage == 0 && capaInAny.RawCapacity == 0 {
					delete(resource.CapacityPerAZ, limes.AvailabilityZoneAny)
				}
			}

			if len(resource.PerAZ) >= 2 {
				capaInUnknown := resource.PerAZ[limes.AvailabilityZoneUnknown]
				if capaInUnknown != nil && capaInUnknown.Capacity == 0 && (capaInUnknown.Usage == nil || *capaInUnknown.Usage == 0) && capaInUnknown.ProjectsUsage == 0 && (capaInUnknown.PhysicalUsage == nil || *capaInUnknown.PhysicalUsage == 0) && len(capaInUnknown.Subcapacities) == 0 {
					delete(resource.PerAZ, limes.AvailabilityZoneUnknown)
				}
				capaInAny := resource.PerAZ[limes.AvailabilityZoneAny]
				if capaInAny != nil && capaInAny.Capacity == 0 && (capaInAny.Usage == nil || *capaInAny.Usage == 0) && capaInAny.ProjectsUsage == 0 && (capaInAny.PhysicalUsage == nil || *capaInAny.PhysicalUsage == 0) && len(capaInAny.Subcapacities) == 0 {
					delete(resource.PerAZ, limes.AvailabilityZoneAny)
				}
			}
		}
	}

	return report, nil
}

// GetClusterRates returns the rate data report for the whole cluster.
func GetClusterRates(cluster *core.Cluster, dbi db.Interface, filter Filter, serviceInfos map[db.ServiceType]liquid.ServiceInfo) (*limesrates.ClusterReport, error) {
	nm := core.BuildRateNameMapping(cluster, serviceInfos)
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
			dbServiceType     db.ServiceType
			dbRateName        liquid.RateName
			minRatesScrapedAt *time.Time
			maxRatesScrapedAt *time.Time
		)
		err := rows.Scan(&dbServiceType, &dbRateName, &minRatesScrapedAt, &maxRatesScrapedAt)
		if err != nil {
			return err
		}

		if !core.HasService(serviceInfos, dbServiceType) {
			return nil
		}
		apiServiceType, _, exists := nm.MapToV1API(dbServiceType, dbRateName)
		if !exists {
			return nil
		}

		srvReport, exists := report.Services[apiServiceType]
		if !exists {
			srvCfg, _ := cluster.Config.GetLiquidConfigurationForType(dbServiceType)
			srvReport = &limesrates.ClusterServiceReport{
				ServiceInfo: limes.ServiceInfo{Type: apiServiceType, Area: srvCfg.Area},
				Rates:       make(limesrates.ClusterRateReports),
			}
			report.Services[apiServiceType] = srvReport
		}

		srvReport.MaxScrapedAt = mergeMaxTime(srvReport.MaxScrapedAt, maxRatesScrapedAt)
		srvReport.MinScrapedAt = mergeMinTime(srvReport.MinScrapedAt, minRatesScrapedAt)

		return nil
	})
	if err != nil {
		return nil, err
	}

	// include global rate limits from configuration
	for dbServiceType, l := range cluster.Config.Liquids {
		for _, rateConfig := range l.RateLimits.Global {
			dbRateName := rateConfig.Name
			apiServiceType, apiRateName, exists := nm.MapToV1API(dbServiceType, dbRateName)
			if !exists {
				continue // defense in depth: should not happen because NameMapping iterated through the same structure
			}

			srvReport, exists := report.Services[apiServiceType]
			if !exists {
				srvCfg, _ := cluster.Config.GetLiquidConfigurationForType(dbServiceType)
				srvReport = &limesrates.ClusterServiceReport{
					ServiceInfo: limes.ServiceInfo{Type: apiServiceType, Area: srvCfg.Area},
					Rates:       make(limesrates.ClusterRateReports),
				}
				report.Services[apiServiceType] = srvReport
			}
			srvReport.Rates[apiRateName] = &limesrates.ClusterRateReport{
				RateInfo: core.BuildAPIRateInfo(apiRateName, liquid.RateInfo{Unit: rateConfig.Unit}),
				Limit:    rateConfig.Limit,
				Window:   rateConfig.Window,
			}
		}
	}

	return report, nil
}

func findInClusterReport(cluster *core.Cluster, report *limesresources.ClusterReport, dbServiceType db.ServiceType, dbResourceName liquid.ResourceName, now time.Time, serviceInfos map[db.ServiceType]liquid.ServiceInfo) (*limesresources.ClusterServiceReport, *limesresources.ClusterResourceReport, core.ResourceBehavior) {
	behavior := cluster.BehaviorForResource(dbServiceType, dbResourceName)
	apiIdentity := behavior.IdentityInV1API

	service, exists := report.Services[apiIdentity.ServiceType]
	if !exists {
		srvCfg, _ := cluster.Config.GetLiquidConfigurationForType(dbServiceType)
		service = &limesresources.ClusterServiceReport{
			ServiceInfo: limes.ServiceInfo{Type: apiIdentity.ServiceType, Area: srvCfg.Area},
			Resources:   make(limesresources.ClusterResourceReports),
		}
		report.Services[apiIdentity.ServiceType] = service
	}

	resource, exists := service.Resources[apiIdentity.Name]
	if !exists {
		serviceInfo := core.InfoForService(serviceInfos, dbServiceType)
		resInfo := core.InfoForResource(serviceInfo, dbResourceName)
		resource = &limesresources.ClusterResourceReport{
			ResourceInfo:     behavior.BuildAPIResourceInfo(apiIdentity.Name, resInfo),
			CommitmentConfig: cluster.CommitmentBehaviorForResource(dbServiceType, dbResourceName).ForCluster().ForAPI(now).AsPointer(),
		}
		if !resource.NoQuota {
			qdConfig := cluster.QuotaDistributionConfigForResource(dbServiceType, dbResourceName)
			resource.QuotaDistributionModel = qdConfig.Model
			// We need to set a default value here. Otherwise zero values will never
			// be reported when there are no `domain_resources` entries to aggregate
			// over.
			defaultDomainsQuota := uint64(0)
			resource.DomainsQuota = &defaultDomainsQuota
		}
		service.Resources[apiIdentity.Name] = resource
	}

	return service, resource, behavior
}

func skipAZBreakdown(azReports limesresources.ClusterAvailabilityZoneReports) bool {
	for az := range azReports {
		if az != limes.AvailabilityZoneAny {
			return false
		}
	}
	return true
}
