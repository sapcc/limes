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
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
)

//NOTE: Both queries use LEFT OUTER JOIN to generate at least one result row
//per known project, to ensure that each project gets a report even if its
//resource data and/or rate data is incomplete.
//
//The query for resource data uses "ORDER BY p.uuid" to ensure that a) the
//output order is reproducible to keep the tests happy and b) records for the
//same project appear in a cluster, so that the implementation can publish
//completed project reports (and then reclaim their memory usage) as soon as
//possible.
var (
	projectRateLimitReportQuery = db.SimplifyWhitespaceInSQL(`
	SELECT p.uuid, p.name, COALESCE(p.parent_uuid, ''), ps.type, ps.scraped_at, ps.rates_scraped_at, pra.name, pra.rate_limit, pra.window_ns, pra.usage_as_bigint
	  FROM projects p
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_rates pra ON pra.service_id = ps.id
	 WHERE %s
`)
	projectReportQuery = db.SimplifyWhitespaceInSQL(`
	SELECT p.uuid, p.name, COALESCE(p.parent_uuid, ''), p.has_bursting, ps.type, ps.scraped_at, ps.rates_scraped_at, pr.name, pr.quota, pr.usage, pr.physical_usage, pr.backend_quota, pr.subresources
	  FROM projects p
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	 WHERE %s
	 ORDER BY p.uuid
`)
)

//GetProjects returns limes.ProjectReport reports for all projects in the given domain or,
//if project is non-nil, for that project only.
//
//Since large domains can contain thousands of project reports, and project
//reports with the highest detail levels can be several MB large, we don't just
//return them all in a big list. Instead, the `submit` callback gets called
//once for each project report once that report is complete.
func GetProjects(cluster *core.Cluster, domain db.Domain, project *db.Project, dbi db.Interface, filter Filter, submit func(*limes.ProjectReport) error) error {
	clusterCanBurst := cluster.Config.Bursting.MaxMultiplier > 0

	fields := map[string]interface{}{"p.domain_id": domain.ID}
	if project != nil {
		fields["p.id"] = project.ID
	}

	projects := make(projects)

	//if rates are requested, fill default rate limits into ProjectServiceReport
	//instances as they are created
	onCreateService := func(serviceReport *limes.ProjectServiceReport) {
		if filter.WithRates {
			if svcConfig, err := cluster.Config.GetServiceConfigurationForType(serviceReport.Type); err == nil {
				if len(svcConfig.RateLimits.ProjectDefault) > 0 {
					serviceReport.Rates = make(limes.ProjectRateLimitReports, len(svcConfig.RateLimits.ProjectDefault))
					for _, rateLimit := range svcConfig.RateLimits.ProjectDefault {
						serviceReport.Rates[rateLimit.Name] = &limes.ProjectRateLimitReport{
							RateInfo: cluster.InfoForRate(serviceReport.Type, rateLimit.Name),
							Limit:    rateLimit.Limit,
							Window:   p2window(rateLimit.Window),
						}
					}
				}
			}
		}
	}

	//phase 1: collect rate data
	//
	//We do this phase first because this data is small enough to easily fit in RAM.

	if filter.WithRates {
		queryStr, joinArgs := filter.PrepareQuery(projectRateLimitReportQuery)
		whereStr, whereArgs := db.BuildSimpleWhereClause(fields, len(joinArgs))
		err := db.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
			var (
				projectUUID       string
				projectName       string
				projectParentUUID string
				serviceType       *string
				scrapedAt         *time.Time
				ratesScrapedAt    *time.Time
				rateName          *string
				limit             *uint64
				window            *limes.Window
				usageAsBigint     *string
			)
			err := rows.Scan(
				&projectUUID, &projectName, &projectParentUUID,
				&serviceType, &scrapedAt, &ratesScrapedAt,
				&rateName, &limit, &window, &usageAsBigint,
			)
			if err != nil {
				return err
			}

			_, srvReport, _ := projects.Find(cluster, projectUUID, projectName, projectParentUUID, serviceType, nil, scrapedAt, ratesScrapedAt, onCreateService)
			if srvReport != nil && rateName != nil {
				rateReport := srvReport.Rates[*rateName]

				//we previously created report entries for all rates that have a
				//default limit; create missing report entries for rates that only have
				//a usage
				if rateReport == nil && usageAsBigint != nil && *usageAsBigint != "" && cluster.HasUsageForRate(*serviceType, *rateName) {
					rateReport = &limes.ProjectRateLimitReport{
						RateInfo: cluster.InfoForRate(*serviceType, *rateName),
					}
					srvReport.Rates[*rateName] = rateReport
				}

				if rateReport != nil {
					if usageAsBigint != nil {
						rateReport.UsageAsBigint = *usageAsBigint
					}

					//overwrite the default limit if a different custom limit is
					//configured, but ignore custom limits where there is no default
					//limit
					if rateReport.Limit != 0 && limit != nil && window != nil {
						if rateReport.Limit != *limit || *rateReport.Window != *window {
							rateReport.DefaultLimit = rateReport.Limit
							rateReport.DefaultWindow = rateReport.Window
							rateReport.Limit = *limit
							rateReport.Window = window
						}
					}
				}
			}

			return nil
		})
		if err != nil {
			return err
		}
	}

	//phase 2: collect resource data
	//
	//Data collected in this phase can be very large if subresources are requested.
	//To ensure that we don't run OOM, we retrieve data ordered by project and
	//send out a completed project report as soon as the data for the next
	//project comes in.

	if !filter.OnlyRates {
		//avoid collecting the potentially large subresources strings when possible
		queryStr := projectReportQuery
		if !filter.WithSubresources {
			queryStr = strings.Replace(queryStr, "pr.subresources", "''", 1)
		}
		queryStr, joinArgs := filter.PrepareQuery(queryStr)
		whereStr, whereArgs := db.BuildSimpleWhereClause(fields, len(joinArgs))
		var currentProjectUUID string
		err := db.ForeachRow(dbi, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
			var (
				projectUUID        string
				projectName        string
				projectParentUUID  string
				projectHasBursting bool
				serviceType        *string
				scrapedAt          *time.Time
				ratesScrapedAt     *time.Time
				resourceName       *string
				quota              *uint64
				usage              *uint64
				physicalUsage      *uint64
				backendQuota       *int64
				subresources       *string
			)
			err := rows.Scan(
				&projectUUID, &projectName, &projectParentUUID, &projectHasBursting,
				&serviceType, &scrapedAt, &ratesScrapedAt, &resourceName,
				&quota, &usage, &physicalUsage, &backendQuota, &subresources,
			)
			if err != nil {
				return err
			}

			if currentProjectUUID != projectUUID {
				//we're moving to a different project, so it's time to publish the
				//finished project report and then allow for it to be GCd
				if currentProjectUUID != "" {
					submit(projects[currentProjectUUID])
					delete(projects, currentProjectUUID)
				}
				currentProjectUUID = projectUUID
			}

			projectReport, _, resReport := projects.Find(cluster, projectUUID, projectName, projectParentUUID, serviceType, resourceName, timeIf(scrapedAt, !filter.OnlyRates), timeIf(ratesScrapedAt, filter.WithRates), onCreateService)
			if projectReport != nil && clusterCanBurst {
				projectReport.Bursting = &limes.ProjectBurstingInfo{
					Enabled:    projectHasBursting,
					Multiplier: cluster.Config.Bursting.MaxMultiplier,
				}
			}

			if resReport != nil {
				subresourcesValue := ""
				if subresources != nil {
					subresourcesValue = *subresources
				}
				resReport.PhysicalUsage = physicalUsage
				resReport.BackendQuota = nil //See below.
				resReport.Subresources = limes.JSONString(subresourcesValue)

				behavior := cluster.BehaviorForResource(*serviceType, *resourceName, domain.Name+"/"+projectName)
				resReport.Scaling = behavior.ToScalingBehavior()
				resReport.Annotations = behavior.Annotations

				if usage != nil {
					resReport.Usage = *usage
				}
				if quota != nil && !resReport.NoQuota {
					resReport.Quota = quota
					resReport.UsableQuota = quota
					if projectHasBursting && clusterCanBurst {
						usableQuota := behavior.MaxBurstMultiplier.ApplyTo(*quota)
						resReport.UsableQuota = &usableQuota
					}
					if backendQuota != nil && (*backendQuota < 0 || uint64(*backendQuota) != *resReport.UsableQuota) {
						resReport.BackendQuota = backendQuota
					}
				}
				if projectHasBursting && clusterCanBurst && quota != nil && usage != nil {
					if *usage > *quota {
						resReport.BurstUsage = *usage - *quota
					}
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}

	//phase 3: submit all remaining reports
	//
	//Either we did have phase 2 and only the last project report remains to be
	//published, or we did not have phase 2 and all project reports need to be
	//published now.

	//submit with stable order to keep the tests happy
	uuids := make([]string, 0, len(projects))
	for uuid := range projects {
		uuids = append(uuids, uuid)
	}
	sort.Strings(uuids)
	for _, uuid := range uuids {
		err := submit(projects[uuid])
		if err != nil {
			return err
		}
	}

	return nil
}

func p2window(val limes.Window) *limes.Window {
	return &val
}

type projects map[string]*limes.ProjectReport

func (p projects) Find(cluster *core.Cluster, projectUUID, projectName, projectParentUUID string, serviceType, resourceName *string, scrapedAt, ratesScrapedAt *time.Time, onCreateService func(*limes.ProjectServiceReport)) (*limes.ProjectReport, *limes.ProjectServiceReport, *limes.ProjectResourceReport) {
	//Ensure the ProjectReport exists.
	project, exists := p[projectUUID]
	if !exists {
		project = &limes.ProjectReport{
			Name:       projectName,
			UUID:       projectUUID,
			ParentUUID: projectParentUUID,
			Services:   make(limes.ProjectServiceReports),
		}
		p[projectUUID] = project
	}

	if serviceType == nil {
		return project, nil, nil
	}
	// Ensure the ProjectServiceReport exists if the serviceType is given.
	service, exists := project.Services[*serviceType]
	if !exists {
		if !cluster.HasService(*serviceType) {
			return project, nil, nil
		}
		service = &limes.ProjectServiceReport{
			ServiceInfo: cluster.InfoForService(*serviceType),
			Resources:   make(limes.ProjectResourceReports),
			Rates:       make(limes.ProjectRateLimitReports),
		}
		if scrapedAt != nil {
			scrapedAtUnix := scrapedAt.Unix()
			service.ScrapedAt = &scrapedAtUnix
		}
		if ratesScrapedAt != nil {
			ratesScrapedAtUnix := ratesScrapedAt.Unix()
			service.RatesScrapedAt = &ratesScrapedAtUnix
		}
		if onCreateService != nil {
			onCreateService(service)
		}
		project.Services[*serviceType] = service
	}

	//Ensure the ProjectResourceReport exists if the resourceName is given.
	var resource *limes.ProjectResourceReport
	if resourceName != nil {
		resource, exists = service.Resources[*resourceName]
		if !exists && cluster.HasResource(*serviceType, *resourceName) {
			resource = &limes.ProjectResourceReport{
				ResourceInfo: cluster.InfoForResource(*serviceType, *resourceName),
			}
			service.Resources[*resourceName] = resource
		}
	}

	return project, service, resource
}

func timeIf(t *time.Time, cond bool) *time.Time {
	if !cond {
		return nil
	}
	return t
}
