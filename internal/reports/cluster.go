// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports

import (
	"context"
	"encoding/json"
	"fmt"
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
)

var clusterReportQuery1 = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	WITH project_commitment_sums AS (
	  SELECT project_id, az_resource_id, SUM(amount) AS amount
	    FROM project_commitments
	   WHERE status = {{liquid.CommitmentStatusConfirmed}}
	   GROUP BY project_id, az_resource_id
	)
	SELECT s.type, r.name, azr.az, SUM(pazr.usage) AS usage,
		   SUM(COALESCE(pazr.physical_usage, pazr.usage)) AS physical_usage, COUNT(pazr.physical_usage) > 0 AS show_physical_usage,
	       SUM(GREATEST(0, COALESCE(pcs.amount, 0) - pazr.usage)) AS unused_commitments,
	       SUM(GREATEST(0, pazr.usage - COALESCE(pcs.amount, 0))) AS uncommitted_usage,
	       SUM(pazr.quota) AS quota, MIN(ps.scraped_at) AS min_scraped_at, MAX(ps.scraped_at) AS max_scraped_at
	  FROM services s
	  JOIN resources r ON r.service_id = s.id {{AND r.name = $resource_name}}
	  JOIN az_resources azr ON azr.resource_id = r.id
	  JOIN project_services ps ON ps.service_id = s.id
	  -- no left join, entries will only appear when there is some project level entry
	  JOIN project_az_resources pazr ON pazr.az_resource_id = azr.id AND pazr.project_id = ps.project_id
	  LEFT OUTER JOIN project_commitment_sums pcs ON pcs.az_resource_id = azr.id AND pcs.project_id = pazr.project_id
	 WHERE TRUE {{AND s.type = $service_type}}
	 GROUP BY s.type, r.name, azr.az
`))

type clusterResourceUsageRecord struct {
	ServiceType       db.ServiceType          `db:"type"`
	ResourceName      liquid.ResourceName     `db:"name"`
	AvailabilityZone  *limes.AvailabilityZone `db:"az"`
	Usage             *uint64                 `db:"usage"`
	PhysicalUsage     *uint64                 `db:"physical_usage"`
	ShowPhysicalUsage *bool                   `db:"show_physical_usage"`
	UnusedCommitments *uint64                 `db:"unused_commitments"`
	UncommittedUsage  *uint64                 `db:"uncommitted_usage"`
	Quota             *uint64                 `db:"quota"`
	MinScrapedAt      *time.Time              `db:"min_scraped_at"`
	MaxScrapedAt      *time.Time              `db:"max_scraped_at"`
}

var clusterReportQuery2 = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	SELECT s.type, r.name, azr.az, azr.raw_capacity, azr.usage, azr.subcapacities AS subcapacities, s.scraped_at
	  FROM services s
	  JOIN resources r ON r.service_id = s.id {{AND r.name = $resource_name}}
	  LEFT OUTER JOIN az_resources azr ON azr.resource_id = r.id
	 WHERE TRUE {{AND s.type = $service_type}}
	 ORDER BY azr.az
`))

type clusterResourceCapacityRecord struct {
	ServiceType      db.ServiceType          `db:"type"`
	ResourceName     liquid.ResourceName     `db:"name"`
	AvailabilityZone *limes.AvailabilityZone `db:"az"`
	RawCapacity      *uint64                 `db:"raw_capacity"`
	Usage            *uint64                 `db:"usage"`
	Subcapacities    *string                 `db:"subcapacities"`
	ScrapedAt        *time.Time              `db:"scraped_at"`
}

var clusterReportQuery3 = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	WITH project_commitment_sums AS (
	  SELECT az_resource_id, duration,
	         COALESCE(SUM(amount) FILTER (WHERE status = {{liquid.CommitmentStatusConfirmed}}), 0) AS confirmed,
	         COALESCE(SUM(amount) FILTER (WHERE status = {{liquid.CommitmentStatusPending}}), 0) AS pending,
	         COALESCE(SUM(amount) FILTER (WHERE status = {{liquid.CommitmentStatusPlanned}}), 0) AS planned
	    FROM project_commitments
	   GROUP BY az_resource_id, duration
	)
	SELECT s.type, r.name, azr.az,
	       pcs.duration, SUM(pcs.confirmed) AS confirmed, SUM(pcs.pending) AS pending, SUM(pcs.planned) AS planned
	  FROM services s
	  JOIN resources r ON r.service_id = s.id {{AND r.name = $resource_name}}
	  JOIN az_resources azr ON azr.resource_id = r.id AND azr.az != {{liquid.AvailabilityZoneTotal}}
	  JOIN project_commitment_sums pcs ON pcs.az_resource_id = azr.id
	 WHERE TRUE {{AND s.type = $service_type}}
	 GROUP BY s.type, r.name, azr.az, pcs.duration
`))

type clusterCommitmentRecord struct {
	ServiceType     db.ServiceType                    `db:"type"`
	ResourceName    liquid.ResourceName               `db:"name"`
	AZ              limes.AvailabilityZone            `db:"az"`
	Duration        limesresources.CommitmentDuration `db:"duration"`
	ConfirmedAmount uint64                            `db:"confirmed"`
	PendingAmount   uint64                            `db:"pending"`
	PlannedAmount   uint64                            `db:"planned"`
}

var clusterRateReportQuery1 = sqlext.SimplifyWhitespace(`
	SELECT s.type, ra.name, MIN(ps.scraped_at) AS min_scraped_at, MAX(ps.scraped_at) AS max_scraped_at
	  FROM services s
	  JOIN rates ra ON ra.service_id = s.id
	  JOIN project_services ps ON ps.service_id = s.id
	  -- TODO: this join reduces the result set to the rates which have been scraped.
	  -- At some point, we want to have the scraped_at statistics per service - not considering rates or resources.
	  JOIN project_rates pra ON pra.rate_id = ra.id AND ps.project_id = pra.project_id
	 WHERE TRUE {{AND s.type = $service_type}}
	 GROUP BY s.type, ra.name
`)

type clusterRateScrapedAtRecord struct {
	ServiceType       db.ServiceType  `db:"type"`
	RateName          liquid.RateName `db:"name"`
	MinRatesScrapedAt *time.Time      `db:"min_scraped_at"`
	MaxRatesScrapedAt *time.Time      `db:"max_scraped_at"`
}

// GetClusterResources returns the resource data report for the whole cluster.
func GetClusterResources(ctx context.Context, cluster *core.Cluster, now time.Time, dbi db.Interface, filter Filter, sis core.ServiceInfoSnapshot) (*limesresources.ClusterReport, error) {
	report := &limesresources.ClusterReport{
		ClusterInfo: limes.ClusterInfo{
			ID: "current", // multi-cluster support has been removed; this value is only included for backwards-compatibility
		},
		Services: make(limesresources.ClusterServiceReports),
	}

	// first query: collect project usage data in these clusters
	queryStr, joinArgs := filter.PrepareQuery(clusterReportQuery1)
	err := oblast.MustNewStore[clusterResourceUsageRecord](oblast.PostgresDialect()).Select(ctx, dbi, queryStr, joinArgs...).Foreach(func(r clusterResourceUsageRecord) error {
		if _, exists := cluster.Config.Liquids[r.ServiceType]; !filter.Includes[r.ServiceType][r.ResourceName] || !exists {
			return nil
		}
		serviceReport, resourceReport, _ := findInClusterReport(cluster, report, r.ServiceType, r.ResourceName, now, sis)

		serviceReport.MaxScrapedAt = mergeMaxTime(serviceReport.MaxScrapedAt, r.MaxScrapedAt)
		serviceReport.MinScrapedAt = mergeMinTime(serviceReport.MinScrapedAt, r.MinScrapedAt)

		if r.AvailabilityZone == nil {
			return nil
		}

		if *r.AvailabilityZone == liquid.AvailabilityZoneTotal {
			// we ignore when a resource can't be found in the app layer yet, we will set the quota here
			resource, _ := sis.GetResourceForPath(db.ResourcePath{ServiceType: r.ServiceType, ResourceName: r.ResourceName})
			resourceReport.Usage = *r.Usage
			if r.Quota != nil && !resourceReport.NoQuota && resource.Topology != liquid.AZSeparatedTopology {
				// NOTE: This is called "DomainsQuota" for historical reasons, but it is actually
				// the sum of all project quotas, since quotas only exist on project level by now.
				resourceReport.DomainsQuota = r.Quota
			}
			if *r.ShowPhysicalUsage {
				resourceReport.PhysicalUsage = r.PhysicalUsage
			}
		}

		if *r.AvailabilityZone != liquid.AvailabilityZoneTotal && filter.WithAZBreakdown {
			if resourceReport.PerAZ == nil {
				resourceReport.PerAZ = make(limesresources.ClusterAZResourceReports)
			}
			azReport := limesresources.ClusterAZResourceReport{
				ProjectsUsage:     *r.Usage,
				UnusedCommitments: *r.UnusedCommitments,
				UncommittedUsage:  *r.UncommittedUsage,
			}
			if *r.ShowPhysicalUsage {
				azReport.PhysicalUsage = r.PhysicalUsage
			}
			resourceReport.PerAZ[*r.AvailabilityZone] = &azReport
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// second query: collect capacity data for these clusters
	queryStr, joinArgs = filter.PrepareQuery(clusterReportQuery2)
	if !filter.WithSubcapacities {
		queryStr = strings.Replace(queryStr, "azr.subcapacities", "''", 1)
	}
	err = oblast.MustNewStore[clusterResourceCapacityRecord](oblast.PostgresDialect()).Select(ctx, dbi, queryStr, joinArgs...).Foreach(func(r clusterResourceCapacityRecord) error {
		if _, exists := cluster.Config.Liquids[r.ServiceType]; !filter.Includes[r.ServiceType][r.ResourceName] || !exists {
			return nil
		}
		_, resourceReport, behavior := findInClusterReport(cluster, report, r.ServiceType, r.ResourceName, now, sis)
		overcommitFactor := behavior.OvercommitFactor

		if r.AvailabilityZone == nil {
			return nil
		}

		if *r.AvailabilityZone == liquid.AvailabilityZoneTotal {
			resourceReport.Capacity = pointerTo(overcommitFactor.ApplyTo(*r.RawCapacity)) //nolint:modernize // pointerTo takes a value, new() creates a zero value
			if *resourceReport.Capacity != *r.RawCapacity {
				resourceReport.RawCapacity = pointerTo(*r.RawCapacity) //nolint:modernize // pointerTo takes a value, new() creates a zero value
			}
		}

		if r.RawCapacity != nil && *r.AvailabilityZone != liquid.AvailabilityZoneTotal {
			azReport := limesresources.ClusterAvailabilityZoneReport{
				Name:  *r.AvailabilityZone,
				Usage: unwrapOrDefault(r.Usage, 0),
			}
			azReport.Capacity = overcommitFactor.ApplyTo(*r.RawCapacity)
			if azReport.Capacity != *r.RawCapacity {
				azReport.RawCapacity = *r.RawCapacity
			}

			if resourceReport.CapacityPerAZ == nil {
				resourceReport.CapacityPerAZ = make(limesresources.ClusterAvailabilityZoneReports)
			}
			resourceReport.CapacityPerAZ[*r.AvailabilityZone] = &azReport

			// only the az-entries have subcapacities!=''
			if r.Subcapacities != nil && *r.Subcapacities != "" && filter.IsSubcapacityAllowed(r.ServiceType, r.ResourceName) {
				translate := behavior.TranslationRuleInV1API.TranslateSubcapacities
				// we ignore when a resource can't be found in the app layer yet, it will appear with empty values
				resource, _ := sis.GetResourceForPath(db.ResourcePath{ServiceType: r.ServiceType, ResourceName: r.ResourceName})
				if translate != nil {
					var err error
					*r.Subcapacities, err = translate(*r.Subcapacities, *r.AvailabilityZone, resource)
					if err != nil {
						return fmt.Errorf("could not apply TranslationRule to subcapacities in %s/%s/%s: %w",
							r.ServiceType, r.ResourceName, *r.AvailabilityZone, err)
					}
				}
				mergeJSONListInto(&resourceReport.Subcapacities, *r.Subcapacities)
			}

			if filter.WithAZBreakdown {
				if resourceReport.PerAZ == nil {
					resourceReport.PerAZ = make(limesresources.ClusterAZResourceReports)
				}
				azReportV2 := resourceReport.PerAZ[*r.AvailabilityZone]
				if azReportV2 == nil {
					azReportV2 = &limesresources.ClusterAZResourceReport{}
					resourceReport.PerAZ[*r.AvailabilityZone] = azReportV2
				}
				azReportV2.Capacity = azReport.Capacity
				azReportV2.RawCapacity = azReport.RawCapacity
				azReportV2.Usage = r.Usage
				azReportV2.Subcapacities = json.RawMessage(unwrapOrDefault(r.Subcapacities, ""))
			}
		}

		report.MaxScrapedAt = mergeMaxTime(report.MaxScrapedAt, r.ScrapedAt)
		report.MinScrapedAt = mergeMinTime(report.MinScrapedAt, r.ScrapedAt)

		return nil
	})
	if err != nil {
		return nil, err
	}

	if filter.WithAZBreakdown {
		// third query: collect commitment data that is broken down by commitment duration
		queryStr, joinArgs = filter.PrepareQuery(clusterReportQuery3)
		err = oblast.MustNewStore[clusterCommitmentRecord](oblast.PostgresDialect()).Select(ctx, dbi, queryStr, joinArgs...).Foreach(func(r clusterCommitmentRecord) error {
			if _, exists := cluster.Config.Liquids[r.ServiceType]; !filter.Includes[r.ServiceType][r.ResourceName] || !exists {
				return nil
			}
			_, resourceReport, _ := findInClusterReport(cluster, report, r.ServiceType, r.ResourceName, now, sis)

			azReport := resourceReport.PerAZ[r.AZ]
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
			return nil, err
		}
	}

	// epilogue: perform some operations on the finished report
	for _, service := range report.Services {
		for _, resource := range service.Resources {
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
func GetClusterRates(ctx context.Context, cluster *core.Cluster, dbi db.Interface, filter Filter, sis core.ServiceInfoSnapshot) (*limesrates.ClusterReport, error) {
	nm := core.BuildRateNameMapping(cluster, sis)
	report := &limesrates.ClusterReport{
		ClusterInfo: limes.ClusterInfo{
			ID: "current", // multi-cluster support has been removed; this value is only included for backwards-compatibility
		},
		Services: make(limesrates.ClusterServiceReports),
	}

	// collect scraping timestamp summaries
	queryStr, joinArgs := filter.PrepareQuery(clusterRateReportQuery1)
	err := oblast.MustNewStore[clusterRateScrapedAtRecord](oblast.PostgresDialect()).Select(ctx, dbi, queryStr, joinArgs...).Foreach(func(r clusterRateScrapedAtRecord) error {
		if _, ok := sis.GetRateForPath(db.RatePath{ServiceType: r.ServiceType, RateName: r.RateName}); !ok {
			return nil
		}
		apiServiceType, _, exists := nm.MapToV1API(r.ServiceType, r.RateName)
		if !exists {
			return nil
		}

		srvReport, exists := report.Services[apiServiceType]
		if !exists {
			srvCfg, _ := cluster.Config.GetLiquidConfigurationForType(r.ServiceType)
			srvReport = &limesrates.ClusterServiceReport{
				Type: apiServiceType, Area: srvCfg.Area,
				Rates: make(limesrates.ClusterRateReports),
			}
			report.Services[apiServiceType] = srvReport
		}

		srvReport.MaxScrapedAt = mergeMaxTime(srvReport.MaxScrapedAt, r.MaxRatesScrapedAt)
		srvReport.MinScrapedAt = mergeMinTime(srvReport.MinScrapedAt, r.MinRatesScrapedAt)

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
					Type: apiServiceType, Area: srvCfg.Area,
					Rates: make(limesrates.ClusterRateReports),
				}
				report.Services[apiServiceType] = srvReport
			}
			srvReport.Rates[apiRateName] = &limesrates.ClusterRateReport{
				RateInfo: core.BuildAPIRateInfo(apiRateName, rateConfig.Unit),
				Limit:    rateConfig.Limit,
				Window:   rateConfig.Window,
			}
		}
	}

	return report, nil
}

func findInClusterReport(cluster *core.Cluster, report *limesresources.ClusterReport, dbServiceType db.ServiceType, dbResourceName liquid.ResourceName, now time.Time, sis core.ServiceInfoSnapshot) (*limesresources.ClusterServiceReport, *limesresources.ClusterResourceReport, core.ResourceBehavior) {
	behavior := cluster.BehaviorForResource(dbServiceType, dbResourceName)
	apiIdentity := behavior.IdentityInV1API

	serviceReport, exists := report.Services[apiIdentity.ServiceType]
	if !exists {
		srvCfg, _ := cluster.Config.GetLiquidConfigurationForType(dbServiceType)
		serviceReport = &limesresources.ClusterServiceReport{
			Type: apiIdentity.ServiceType, Area: srvCfg.Area,
			Resources: make(limesresources.ClusterResourceReports),
		}
		report.Services[apiIdentity.ServiceType] = serviceReport
	}

	resourceReport, exists := serviceReport.Resources[apiIdentity.Name]
	if !exists {
		// we ignore when a resource can't be found in the app layer yet, it will appear with empty values
		resource, _ := sis.GetResourceForPath(db.ResourcePath{ServiceType: dbServiceType, ResourceName: dbResourceName})
		resourceReport = &limesresources.ClusterResourceReport{
			ResourceInfo:     behavior.BuildAPIResourceInfo(apiIdentity.Name, resource),
			CommitmentConfig: cluster.CommitmentBehaviorForResource(dbServiceType, dbResourceName).ForCluster().ForAPI(now).AsPointer(),
		}
		if !resourceReport.NoQuota {
			qdConfig := cluster.QuotaDistributionConfigForResource(dbServiceType, dbResourceName)
			resourceReport.QuotaDistributionModel = qdConfig.Model
			// We need to set a default value here. Otherwise zero values will never
			// be reported when there are no `domain_resources` entries to aggregate
			// over.
			defaultDomainsQuota := uint64(0)
			resourceReport.DomainsQuota = &defaultDomainsQuota
		}
		serviceReport.Resources[apiIdentity.Name] = resourceReport
	}

	return serviceReport, resourceReport, behavior
}

func skipAZBreakdown(azReports limesresources.ClusterAvailabilityZoneReports) bool {
	for az := range azReports {
		if az != limes.AvailabilityZoneAny {
			return false
		}
	}
	return true
}
