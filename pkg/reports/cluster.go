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
	"strings"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
)

var clusterReportQuery1 = sqlext.SimplifyWhitespace(`
	SELECT ps.type, pr.name,
	       SUM(pr.quota), SUM(pr.usage),
	       SUM(GREATEST(pr.usage - pr.quota, 0)),
	       SUM(COALESCE(pr.physical_usage, pr.usage)), COUNT(pr.physical_usage) > 0,
	       MIN(ps.scraped_at), MAX(ps.scraped_at),
	       MIN(ps.rates_scraped_at), MAX(ps.rates_scraped_at)
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	 WHERE %s GROUP BY d.cluster_id, ps.type, pr.name
`)

var clusterReportQuery2 = sqlext.SimplifyWhitespace(`
	SELECT ds.type, dr.name, SUM(dr.quota)
	  FROM domains d
	  JOIN domain_services ds ON ds.domain_id = d.id {{AND ds.type = $service_type}}
	  LEFT OUTER JOIN domain_resources dr ON dr.service_id = ds.id {{AND dr.name = $resource_name}}
	 WHERE %s GROUP BY d.cluster_id, ds.type, dr.name
`)

var clusterReportQuery3 = sqlext.SimplifyWhitespace(`
	SELECT cs.type, cr.name, cr.capacity,
	       cr.capacity_per_az, cr.subcapacities, cs.scraped_at
	  FROM cluster_services cs
	  LEFT OUTER JOIN cluster_resources cr ON cr.service_id = cs.id {{AND cr.name = $resource_name}}
	 WHERE %s {{AND cs.type = $service_type}}
`)

// GetCluster returns the report for the whole cluster.
// TODO: should db be replaced with dbi?
func GetCluster(cluster *core.Cluster, dbi db.Interface, filter Filter) (*limes.ClusterReport, error) {
	report := &limes.ClusterReport{
		ID:       "current", //the actual cluster ID is now an implementation detail and not shown on the API
		Services: make(limes.ClusterServiceReports),
	}

	if !filter.OnlyRates {
		//first query: collect project usage data in these clusters
		queryStr, joinArgs := filter.PrepareQuery(clusterReportQuery1)
		whereStr, whereArgs := db.BuildSimpleWhereClause(makeClusterFilter("d", cluster.ID), len(joinArgs))
		err := sqlext.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
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
				minRatesScrapedAt *time.Time
				maxRatesScrapedAt *time.Time
			)
			err := rows.Scan(&serviceType, &resourceName,
				&projectsQuota, &usage, &burstUsage,
				&physicalUsage, &showPhysicalUsage,
				&minScrapedAt, &maxScrapedAt,
				&minRatesScrapedAt, &maxRatesScrapedAt)
			if err != nil {
				return err
			}

			service, resource := findInClusterReport(cluster, report, serviceType, resourceName)

			clusterCanBurst := cluster.Config.Bursting.MaxMultiplier > 0

			if service != nil {
				if maxScrapedAt != nil {
					val := maxScrapedAt.Unix()
					if service.MaxScrapedAt == nil || *service.MaxScrapedAt < val {
						service.MaxScrapedAt = &val
					}
				}
				if minScrapedAt != nil {
					val := minScrapedAt.Unix()
					if service.MinScrapedAt == nil || *service.MinScrapedAt > val {
						service.MinScrapedAt = &val
					}
				}
				if filter.WithRates {
					if maxRatesScrapedAt != nil {
						val := maxRatesScrapedAt.Unix()
						if service.MaxRatesScrapedAt == nil || *service.MaxRatesScrapedAt < val {
							service.MaxRatesScrapedAt = &val
						}
					}
					if minRatesScrapedAt != nil {
						val := minRatesScrapedAt.Unix()
						if service.MinRatesScrapedAt == nil || *service.MinRatesScrapedAt > val {
							service.MinRatesScrapedAt = &val
						}
					}
				}
			}

			if resource != nil {
				if projectsQuota != nil && resource.ExternallyManaged && !resource.NoQuota {
					resource.DomainsQuota = projectsQuota
				}
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
		whereStr, whereArgs = db.BuildSimpleWhereClause(makeClusterFilter("d", cluster.ID), len(joinArgs))
		err = sqlext.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
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
			if resource != nil && quota != nil && !resource.ExternallyManaged && !resource.NoQuota {
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
		whereStr, whereArgs = db.BuildSimpleWhereClause(makeClusterFilter("cs", cluster.ID), len(joinArgs))
		err = sqlext.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
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

			scrapedAtUnix := scrapedAt.Unix()
			if report.MaxScrapedAt == nil || *report.MaxScrapedAt < scrapedAtUnix {
				report.MaxScrapedAt = &scrapedAtUnix
			}
			if report.MinScrapedAt == nil || *report.MinScrapedAt > scrapedAtUnix {
				report.MinScrapedAt = &scrapedAtUnix
			}

			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	//include global rate limits from configuration
	if filter.WithRates {
		for _, serviceConfig := range cluster.Config.Services {
			if serviceReport, _ := findInClusterReport(cluster, report, serviceConfig.Type, nil); serviceReport != nil {
				serviceReport.Rates = limes.ClusterRateLimitReports{}

				for _, rateCfg := range serviceConfig.RateLimits.Global {
					serviceReport.Rates[rateCfg.Name] = &limes.ClusterRateLimitReport{
						RateInfo: limes.RateInfo{
							Name: rateCfg.Name,
							Unit: rateCfg.Unit,
						},
						Limit:  rateCfg.Limit,
						Window: rateCfg.Window,
					}
				}
			}
		}
	}

	return report, nil
}

func makeClusterFilter(tableWithClusterID, clusterID string) map[string]interface{} {
	return map[string]interface{}{
		tableWithClusterID + ".cluster_id": clusterID,
	}
}

func findInClusterReport(cluster *core.Cluster, report *limes.ClusterReport, serviceType string, resourceName *string) (*limes.ClusterServiceReport, *limes.ClusterResourceReport) {
	service, exists := report.Services[serviceType]
	if !exists {
		if !cluster.HasService(serviceType) {
			return nil, nil
		}
		service = &limes.ClusterServiceReport{
			ServiceInfo: cluster.InfoForService(serviceType),
			Resources:   make(limes.ClusterResourceReports),
			Rates:       make(limes.ClusterRateLimitReports),
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
		resource = &limes.ClusterResourceReport{
			ResourceInfo: cluster.InfoForResource(serviceType, *resourceName),
		}
		if !resource.ResourceInfo.NoQuota {
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

func getClusterAZReports(capacityPerAZ string, overcommitFactor float64) (limes.ClusterAvailabilityZoneReports, error) {
	azReports := make(limes.ClusterAvailabilityZoneReports)
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
