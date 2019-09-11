/*******************************************************************************
*
* Copyright 2017 SAP SE
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
	"strings"
	"time"

	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
)

var clusterReportQuery1 = `
	SELECT d.cluster_id, ps.type, pr.name,
	       SUM(pr.quota), SUM(pr.usage),
	       SUM(GREATEST(pr.usage - pr.quota, 0)),
	       SUM(COALESCE(pr.physical_usage, pr.usage)), COUNT(pr.physical_usage) > 0,
	       MIN(ps.scraped_at), MAX(ps.scraped_at)
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	 WHERE %s GROUP BY d.cluster_id, ps.type, pr.name
`

var clusterReportQuery2 = `
	SELECT d.cluster_id, ds.type, dr.name, SUM(dr.quota)
	  FROM domains d
	  LEFT OUTER JOIN domain_services ds ON ds.domain_id = d.id {{AND ds.type = $service_type}}
	  LEFT OUTER JOIN domain_resources dr ON dr.service_id = ds.id {{AND dr.name = $resource_name}}
	 WHERE %s GROUP BY d.cluster_id, ds.type, dr.name
`

var clusterReportQuery3 = `
	SELECT cs.cluster_id, cs.type, cr.name, cr.capacity, cr.comment, cr.subcapacities, cs.scraped_at
	  FROM cluster_services cs
	  LEFT OUTER JOIN cluster_resources cr ON cr.service_id = cs.id {{AND cr.name = $resource_name}}
	 WHERE %s {{AND cs.type = $service_type}}
`

var clusterReportQuery4 = `
	SELECT ds.type, dr.name, SUM(dr.quota)
	  FROM domain_services ds
	  JOIN domain_resources dr ON dr.service_id = ds.id
	 WHERE %s GROUP BY ds.type, dr.name
`

var clusterReportQuery5 = `
	SELECT ps.type, pr.name, SUM(pr.usage),
	       SUM(COALESCE(pr.physical_usage, pr.usage)), COUNT(pr.physical_usage) > 0
	  FROM project_services ps
	  JOIN project_resources pr ON pr.service_id = ps.id
	 WHERE %s GROUP BY ps.type, pr.name
`

var clusterReportQuery6 = `
	SELECT cs.cluster_id, cs.type
	  FROM cluster_services cs
     WHERE %s {{AND cs.type = $service_type}}
`

//GetClusters returns reports for all clusters or, if clusterID is
//non-nil, for that cluster only.
//
//In contrast to nearly everything else in Limes, his needs the full
//core.Configuration (instead of just the current core.ClusterConfiguration)
//to look at the services enabled in other clusters.
func GetClusters(config core.Configuration, clusterID *string, dbi db.Interface, filter Filter) ([]*limes.ClusterReport, error) {
	//first query: collect project usage data in these clusters
	clusters := make(clusters)

	if !filter.OnlyRates {
		queryStr, joinArgs := filter.PrepareQuery(clusterReportQuery1)
		whereStr, whereArgs := db.BuildSimpleWhereClause(makeClusterFilter("d", clusterID), len(joinArgs))
		err := db.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
			var (
				clusterID         string
				serviceType       *string
				resourceName      *string
				projectsQuota     *uint64
				usage             *uint64
				burstUsage        *uint64
				physicalUsage     *uint64
				showPhysicalUsage *bool
				minScrapedAt      *time.Time
				maxScrapedAt      *time.Time
			)
			err := rows.Scan(&clusterID, &serviceType, &resourceName,
				&projectsQuota, &usage, &burstUsage,
				&physicalUsage, &showPhysicalUsage,
				&minScrapedAt, &maxScrapedAt)
			if err != nil {
				return err
			}

			_, service, resource := clusters.Find(config, clusterID, serviceType, resourceName)

			clusterConfig, exists := config.Clusters[clusterID]
			clusterCanBurst := exists && clusterConfig.Config.Bursting.MaxMultiplier > 0

			if service != nil {
				if maxScrapedAt != nil {
					val := time.Time(*maxScrapedAt).Unix()
					if service.MaxScrapedAt == nil || *service.MaxScrapedAt < val {
						service.MaxScrapedAt = &val
					}
				}
				if minScrapedAt != nil {
					val := time.Time(*minScrapedAt).Unix()
					if service.MinScrapedAt == nil || *service.MinScrapedAt > val {
						service.MinScrapedAt = &val
					}
				}
			}

			if resource != nil {
				if projectsQuota != nil && resource.ExternallyManaged {
					resource.DomainsQuota = *projectsQuota
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
		whereStr, whereArgs = db.BuildSimpleWhereClause(makeClusterFilter("d", clusterID), len(joinArgs))
		err = db.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
			var (
				clusterID    string
				serviceType  *string
				resourceName *string
				quota        *uint64
			)
			err := rows.Scan(&clusterID, &serviceType, &resourceName, &quota)
			if err != nil {
				return err
			}

			_, _, resource := clusters.Find(config, clusterID, serviceType, resourceName)

			if resource != nil && quota != nil && !resource.ExternallyManaged {
				resource.DomainsQuota = *quota
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
		whereStr, whereArgs = db.BuildSimpleWhereClause(makeClusterFilter("cs", clusterID), len(joinArgs))
		err = db.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
			var (
				clusterID     string
				serviceType   string
				resourceName  *string
				rawCapacity   *uint64
				comment       *string
				subcapacities *string
				scrapedAt     time.Time
			)
			err := rows.Scan(&clusterID, &serviceType, &resourceName, &rawCapacity, &comment, &subcapacities, &scrapedAt)
			if err != nil {
				return err
			}

			cluster, _, resource := clusters.Find(config, clusterID, &serviceType, resourceName)

			if resource != nil {
				overcommitFactor := config.Clusters[clusterID].BehaviorForResource(serviceType, *resourceName, "").OvercommitFactor
				if overcommitFactor == 0 {
					resource.Capacity = rawCapacity
				} else {
					resource.RawCapacity = rawCapacity
					capacity := uint64(float64(*rawCapacity) * overcommitFactor)
					resource.Capacity = &capacity
				}
				if comment != nil {
					resource.Comment = *comment
				}
				if subcapacities != nil && *subcapacities != "" {
					resource.Subcapacities = limes.JSONString(*subcapacities)
				}
			}

			if cluster != nil {
				scrapedAtUnix := time.Time(scrapedAt).Unix()
				if cluster.MaxScrapedAt == nil || *cluster.MaxScrapedAt < scrapedAtUnix {
					cluster.MaxScrapedAt = &scrapedAtUnix
				}
				if cluster.MinScrapedAt == nil || *cluster.MinScrapedAt > scrapedAtUnix {
					cluster.MinScrapedAt = &scrapedAtUnix
				}
			}

			return nil
		})
		if err != nil {
			return nil, err
		}

		//enumerate shared services
		isSharedService := make(map[string]bool)
		for clusterID := range clusters {
			clusterConfig, exists := config.Clusters[clusterID]
			if exists {
				for serviceType, shared := range clusterConfig.IsServiceShared {
					if shared {
						isSharedService[serviceType] = true
					}
				}
			}
		}

		if len(isSharedService) > 0 {

			if !filter.LocalQuotaUsageOnly {

				//fourth query: aggregate domain quota for shared services
				sharedQuotaSums := make(map[string]map[string]uint64)

				sharedServiceTypes := make([]string, 0, len(isSharedService))
				for serviceType := range isSharedService {
					sharedServiceTypes = append(sharedServiceTypes, serviceType)
				}
				whereStr, queryArgs := db.BuildSimpleWhereClause(map[string]interface{}{"ds.type": sharedServiceTypes}, 0)
				err = db.ForeachRow(db.DB, fmt.Sprintf(clusterReportQuery4, whereStr), queryArgs, func(rows *sql.Rows) error {
					var (
						serviceType  string
						resourceName string
						quota        uint64
					)
					err := rows.Scan(&serviceType, &resourceName, &quota)
					if err != nil {
						return err
					}

					if sharedQuotaSums[serviceType] == nil {
						sharedQuotaSums[serviceType] = make(map[string]uint64)
					}
					sharedQuotaSums[serviceType][resourceName] = quota
					return nil
				})
				if err != nil {
					return nil, err
				}

				//fifth query: aggregate project quota for shared services
				whereStr, queryArgs = db.BuildSimpleWhereClause(map[string]interface{}{"ps.type": sharedServiceTypes}, 0)
				type usageSum struct {
					Usage         uint64
					PhysicalUsage *uint64
				}
				sharedUsageSums := make(map[string]map[string]usageSum)
				err = db.ForeachRow(db.DB, fmt.Sprintf(clusterReportQuery5, whereStr), queryArgs, func(rows *sql.Rows) error {
					var (
						serviceType       string
						resourceName      string
						usage             uint64
						physicalUsage     *uint64
						showPhysicalUsage bool
					)
					err := rows.Scan(&serviceType, &resourceName, &usage,
						&physicalUsage, &showPhysicalUsage)
					if err != nil {
						return err
					}

					u := usageSum{Usage: usage}
					if showPhysicalUsage {
						u.PhysicalUsage = physicalUsage
					}

					if sharedUsageSums[serviceType] == nil {
						sharedUsageSums[serviceType] = make(map[string]usageSum)
					}
					sharedUsageSums[serviceType][resourceName] = u
					return nil
				})
				if err != nil {
					return nil, err
				}

				for _, cluster := range clusters {
					isSharedService := make(map[string]bool)
					for serviceType, shared := range config.Clusters[cluster.ID].IsServiceShared {
						//NOTE: cluster config is guaranteed to exist due to earlier validation
						if shared {
							isSharedService[serviceType] = true
						}
					}

					for _, service := range cluster.Services {
						if isSharedService[service.Type] && sharedQuotaSums[service.Type] != nil {
							for _, resource := range service.Resources {
								quota, exists := sharedQuotaSums[service.Type][resource.Name]
								if exists {
									resource.DomainsQuota = quota
								}
								u, exists := sharedUsageSums[service.Type][resource.Name]
								if exists {
									resource.Usage = u.Usage
									resource.PhysicalUsage = u.PhysicalUsage
								}
							}
						}
					}
				}
			}

			//third query again, but this time to collect shared capacities
			queryStr, joinArgs = filter.PrepareQuery(clusterReportQuery3)
			if !filter.WithSubcapacities {
				queryStr = strings.Replace(queryStr, "cr.subcapacities", "''", 1)
			}
			filter := map[string]interface{}{"cs.cluster_id": "shared"}
			whereStr, whereArgs = db.BuildSimpleWhereClause(filter, len(joinArgs))
			err = db.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
				var (
					sharedClusterID string
					serviceType     string
					resourceName    *string
					rawCapacity     *uint64
					comment         *string
					subcapacities   *string
					scrapedAt       time.Time
				)
				err := rows.Scan(&sharedClusterID, &serviceType, &resourceName, &rawCapacity, &comment, &subcapacities, &scrapedAt)
				if err != nil {
					return err
				}

				for _, cluster := range clusters {
					if !config.Clusters[cluster.ID].IsServiceShared[serviceType] {
						continue
					}

					_, _, resource := clusters.Find(config, cluster.ID, &serviceType, resourceName)

					if resource != nil {
						overcommitFactor := config.Clusters[cluster.ID].BehaviorForResource(serviceType, *resourceName, "").OvercommitFactor
						if overcommitFactor == 0 {
							resource.Capacity = rawCapacity
						} else {
							resource.RawCapacity = rawCapacity
							capacity := uint64(float64(*rawCapacity) * overcommitFactor)
							resource.Capacity = &capacity
						}
						if comment != nil {
							resource.Comment = *comment
						}
						if subcapacities != nil && *subcapacities != "" {
							resource.Subcapacities = limes.JSONString(*subcapacities)
						}
					}

					scrapedAtUnix := time.Time(scrapedAt).Unix()
					if cluster.MaxScrapedAt == nil || *cluster.MaxScrapedAt < scrapedAtUnix {
						cluster.MaxScrapedAt = &scrapedAtUnix
					}
					if cluster.MinScrapedAt == nil || *cluster.MinScrapedAt > scrapedAtUnix {
						cluster.MinScrapedAt = &scrapedAtUnix
					}
				}

				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}

	if filter.WithRates {
		if clusterConfig, exists := config.Clusters[*clusterID]; exists {
			for _, serviceConfig := range clusterConfig.Config.Services {
				if _, serviceReport, _ := clusters.Find(config, *clusterID, &serviceConfig.Type, nil); serviceReport != nil {
					serviceReport.Rates = limes.ClusterRateLimitReports{}

					for _, configuredRateLimit := range serviceConfig.Rates.Global {
						rl, exists := serviceReport.Rates[configuredRateLimit.TargetTypeURI]
						if !exists {
							rl = &limes.ClusterRateLimitReport{
								TargetTypeURI: configuredRateLimit.TargetTypeURI,
								Actions:       make(limes.ClusterRateLimitActionReports),
							}
						}

						for _, configuredAction := range configuredRateLimit.Actions {
							act, exists := rl.Actions[configuredAction.Name]
							if !exists {
								act = &limes.ClusterRateLimitActionReport{
									Name: configuredAction.Name,
								}
							}
							act.Limit = configuredAction.Limit
							act.Unit = limes.Unit(configuredAction.Unit)
							rl.Actions[act.Name] = act
						}
						serviceReport.Rates[rl.TargetTypeURI] = rl
					}
				}
			}
		}
	}

	//flatten result (with stable order to keep the tests happy)
	ids := make([]string, 0, len(clusters))
	for id := range clusters {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]*limes.ClusterReport, len(clusters))
	for idx, id := range ids {
		result[idx] = clusters[id]
	}

	return result, nil
}

func makeClusterFilter(tableWithClusterID string, clusterID *string) map[string]interface{} {
	fields := make(map[string]interface{})
	if clusterID != nil {
		fields[tableWithClusterID+".cluster_id"] = *clusterID
	}
	return fields
}

type clusters map[string]*limes.ClusterReport

func (c clusters) Find(config core.Configuration, clusterID string, serviceType, resourceName *string) (*limes.ClusterReport, *limes.ClusterServiceReport, *limes.ClusterResourceReport) {
	clusterConfig, exists := config.Clusters[clusterID]
	if !exists {
		return nil, nil, nil
	}

	cluster, exists := c[clusterID]
	if !exists {
		cluster = &limes.ClusterReport{
			ID:       clusterID,
			Services: make(limes.ClusterServiceReports),
		}
		c[clusterID] = cluster
	}

	if serviceType == nil {
		return cluster, nil, nil
	}

	service, exists := cluster.Services[*serviceType]
	if !exists {
		if !clusterConfig.HasService(*serviceType) {
			return cluster, nil, nil
		}
		service = &limes.ClusterServiceReport{
			Shared:      clusterConfig.IsServiceShared[*serviceType],
			ServiceInfo: clusterConfig.InfoForService(*serviceType),
			Resources:   make(limes.ClusterResourceReports),
			Rates:       make(limes.ClusterRateLimitReports),
		}
		cluster.Services[*serviceType] = service
	}

	if resourceName == nil {
		return cluster, service, nil
	}

	resource, exists := service.Resources[*resourceName]
	if !exists {
		if !clusterConfig.HasResource(*serviceType, *resourceName) {
			return cluster, service, nil
		}
		resource = &limes.ClusterResourceReport{
			ResourceInfo: clusterConfig.InfoForResource(*serviceType, *resourceName),
		}
		service.Resources[*resourceName] = resource
	}

	return cluster, service, resource
}
