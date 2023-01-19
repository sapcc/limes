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
	SELECT ps.type, pr.name,
	       SUM(pr.quota), SUM(pr.usage),
	       SUM(GREATEST(pr.usage - pr.quota, 0)),
	       SUM(COALESCE(pr.physical_usage, pr.usage)), COUNT(pr.physical_usage) > 0,
	       MIN(ps.scraped_at), MAX(ps.scraped_at)
	  FROM project_services ps
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
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
	SELECT cs.type, cr.name, cr.capacity,
	       cr.capacity_per_az, cr.subcapacities, cs.scraped_at
	  FROM cluster_services cs
	  LEFT OUTER JOIN cluster_resources cr ON cr.service_id = cs.id {{AND cr.name = $resource_name}}
	 WHERE TRUE {{AND cs.type = $service_type}}
`)

var clusterRateReportQuery1 = sqlext.SimplifyWhitespace(`
	SELECT type, MIN(rates_scraped_at), MAX(rates_scraped_at)
	  FROM project_services
	 WHERE TRUE {{AND type = $service_type}}
	 GROUP BY type
`)

// GetClusterResources returns the resource data report for the whole cluster.
func GetClusterResources(cluster *core.Cluster, dbi db.Interface, filter Filter) (*limesresources.ClusterReport, error) {
	report := &limesresources.ClusterReport{
		ClusterInfo: limes.ClusterInfo{
			ID: "current", //multi-cluster support has been removed; this value is only included for backwards-compatibility
		},
		Services: make(limesresources.ClusterServiceReports),
	}

	//first query: collect project usage data in these clusters
	queryStr, joinArgs := filter.PrepareQuery(clusterReportQuery1)
	err := sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			serviceType       string
			resourceName      *string
			projectsQuota     *uint64
			usage             *uint64
			burstUsage        *uint64
			physicalUsage     *uint64
			showPhysicalUsage *bool
			minScrapedAt      *time.Time
			maxScrapedAt      *time.Time
		)
		err := rows.Scan(&serviceType, &resourceName,
			&projectsQuota, &usage, &burstUsage,
			&physicalUsage, &showPhysicalUsage,
			&minScrapedAt, &maxScrapedAt)
		if err != nil {
			return err
		}

		service, resource := findInClusterReport(cluster, report, serviceType, resourceName)

		clusterCanBurst := cluster.Config.Bursting.MaxMultiplier > 0

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
			if showPhysicalUsage != nil && *showPhysicalUsage {
				resource.PhysicalUsage = physicalUsage
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	//second query: collect domain quota data in these clusters
	queryStr, joinArgs = filter.PrepareQuery(clusterReportQuery2)
	err = sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			serviceType  string
			resourceName *string
			quota        *uint64
		)
		err := rows.Scan(&serviceType, &resourceName, &quota)
		if err != nil {
			return err
		}

		_, resource := findInClusterReport(cluster, report, serviceType, resourceName)
		if resource != nil && quota != nil && !resource.NoQuota {
			resource.DomainsQuota = quota
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	//third query: collect capacity data for these clusters
	queryStr, joinArgs = filter.PrepareQuery(clusterReportQuery3)
	if !filter.WithSubcapacities {
		queryStr = strings.Replace(queryStr, "cr.subcapacities", "''", 1)
	}
	err = sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			serviceType   string
			resourceName  *string
			rawCapacity   *uint64
			capacityPerAZ *string
			subcapacities *string
			scrapedAt     time.Time
		)
		err := rows.Scan(&serviceType, &resourceName, &rawCapacity,
			&capacityPerAZ, &subcapacities, &scrapedAt)
		if err != nil {
			return err
		}

		_, resource := findInClusterReport(cluster, report, serviceType, resourceName)

		if resource != nil {
			overcommitFactor := cluster.BehaviorForResource(serviceType, *resourceName, "").OvercommitFactor
			if overcommitFactor == 0 {
				resource.Capacity = rawCapacity
			} else {
				resource.RawCapacity = rawCapacity
				capacity := uint64(float64(*rawCapacity) * overcommitFactor)
				resource.Capacity = &capacity
			}
			if subcapacities != nil && *subcapacities != "" && filter.IsSubcapacityAllowed(serviceType, *resourceName) {
				resource.Subcapacities = json.RawMessage(*subcapacities)
			}
			if capacityPerAZ != nil && *capacityPerAZ != "" {
				azReports, err := getClusterAZReports(*capacityPerAZ, overcommitFactor)
				if err != nil {
					return err
				}
				resource.CapacityPerAZ = azReports
			}
		}

		report.MaxScrapedAt = mergeMaxTime(report.MaxScrapedAt, &scrapedAt)
		report.MinScrapedAt = mergeMinTime(report.MinScrapedAt, &scrapedAt)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return report, nil
}

// GetClusterResources returns the rate data report for the whole cluster.
func GetClusterRates(cluster *core.Cluster, dbi db.Interface, filter Filter) (*limesrates.ClusterReport, error) {
	report := &limesrates.ClusterReport{
		ClusterInfo: limes.ClusterInfo{
			ID: "current", //multi-cluster support has been removed; this value is only included for backwards-compatibility
		},
		Services: make(limesrates.ClusterServiceReports),
	}

	//collect scraping timestamp summaries
	queryStr, joinArgs := filter.PrepareQuery(clusterRateReportQuery1)
	err := sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			serviceType       string
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

	//include global rate limits from configuration
	for _, serviceConfig := range cluster.Config.Services {
		srvReport := report.Services[serviceConfig.Type]
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

func findInClusterReport(cluster *core.Cluster, report *limesresources.ClusterReport, serviceType string, resourceName *string) (*limesresources.ClusterServiceReport, *limesresources.ClusterResourceReport) {
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
		resource = &limesresources.ClusterResourceReport{
			ResourceInfo: cluster.InfoForResource(serviceType, *resourceName),
		}
		if !resource.ResourceInfo.NoQuota {
			qdConfig := cluster.QuotaDistributionConfigForResource(serviceType, *resourceName)
			resource.QuotaDistributionModel = qdConfig.Model
			//We need to set a default value here. Otherwise zero values will never
			//be reported when there are no `domain_resources` entries to aggregate
			//over.
			defaultDomainsQuota := uint64(0)
			resource.DomainsQuota = &defaultDomainsQuota
		}
		service.Resources[*resourceName] = resource
	}

	return service, resource
}

func getClusterAZReports(capacityPerAZ string, overcommitFactor float64) (limesresources.ClusterAvailabilityZoneReports, error) {
	azReports := make(limesresources.ClusterAvailabilityZoneReports)
	err := json.Unmarshal([]byte(capacityPerAZ), &azReports)
	if err != nil {
		return nil, err
	}

	if overcommitFactor != 0 {
		for _, report := range azReports {
			report.RawCapacity = report.Capacity
			report.Capacity = uint64(float64(report.Capacity) * overcommitFactor)
		}
	}

	return azReports, nil
}
