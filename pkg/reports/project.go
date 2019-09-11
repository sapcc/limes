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

var (
	projectReportQuery = `
	SELECT p.uuid, p.name, COALESCE(p.parent_uuid, ''), p.has_bursting, ps.type, ps.scraped_at, pr.name, pr.quota, pr.usage, pr.physical_usage, pr.backend_quota, pr.subresources
	  FROM projects p
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	 WHERE %s
`
	projectRateLimitReportQuery = `
	SELECT p.uuid, p.name, COALESCE(p.parent_uuid, ''), ps.type, ps.scraped_at, prl.target_type_uri, prl.action, prl.rate_limit, prl.unit
	  FROM projects p
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_rate_limits prl ON prl.service_id = ps.id
	 WHERE %s
`
)

//GetProjects returns limes.ProjectReport reports for all projects in the given domain or,
//if projectID is non-nil, for that project only.
func GetProjects(cluster *core.Cluster, domain db.Domain, projectID *int64, dbi db.Interface, filter Filter) ([]*limes.ProjectReport, error) {
	clusterCanBurst := cluster.Config.Bursting.MaxMultiplier > 0

	fields := map[string]interface{}{"p.domain_id": domain.ID}
	if projectID != nil {
		fields["p.id"] = *projectID
	}

	projects := make(projects)

	// Do not collect project resources if only rates are requested.
	if !filter.OnlyRates {
		//avoid collecting the potentially large subresources strings when possible
		queryStr := projectReportQuery
		if !filter.WithSubresources {
			queryStr = strings.Replace(queryStr, "pr.subresources", "''", 1)
		}
		queryStr, joinArgs := filter.PrepareQuery(queryStr)
		whereStr, whereArgs := db.BuildSimpleWhereClause(fields, len(joinArgs))
		err := db.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
			var (
				projectUUID        string
				projectName        string
				projectParentUUID  string
				projectHasBursting bool
				serviceType        *string
				scrapedAt          *time.Time
				resourceName       *string
				quota              *uint64
				usage              *uint64
				physicalUsage      *uint64
				backendQuota       *int64
				subresources       *string
			)
			err := rows.Scan(
				&projectUUID, &projectName, &projectParentUUID, &projectHasBursting,
				&serviceType, &scrapedAt, &resourceName,
				&quota, &usage, &physicalUsage, &backendQuota, &subresources,
			)
			if err != nil {
				return err
			}

			projectReport, _, resReport, _ := projects.Find(cluster, projectUUID, projectName, projectParentUUID, serviceType, resourceName, nil, nil, scrapedAt)
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
				if quota != nil {
					resReport.Quota = *quota
					resReport.UsableQuota = *quota
					if projectHasBursting && clusterCanBurst {
						resReport.UsableQuota = behavior.MaxBurstMultiplier.ApplyTo(*quota)
					}
					if backendQuota != nil && (*backendQuota < 0 || uint64(*backendQuota) != resReport.UsableQuota) {
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
			return nil, err
		}
	}

	if filter.WithRates {
		queryStr, joinArgs := filter.PrepareQuery(projectRateLimitReportQuery)
		whereStr, whereArgs := db.BuildSimpleWhereClause(fields, len(joinArgs))
		err := db.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
			var (
				projectUUID,
				projectName,
				projectParentUUID string
				serviceType *string
				scrapedAt   *time.Time
				targetTypeURI,
				actionName *string
				limit *uint64
				unit  *string
			)
			err := rows.Scan(
				&projectUUID, &projectName, &projectParentUUID,
				&serviceType, &scrapedAt,
				&targetTypeURI, &actionName, &limit, &unit,
			)
			if err != nil {
				return err
			}

			_, _, _, rateLimitReport := projects.Find(cluster, projectUUID, projectName, projectParentUUID, serviceType, nil, targetTypeURI, actionName, scrapedAt)
			if rateLimitReport != nil && limit != nil && unit != nil {
				rateLimitReport.Actions[*actionName].Limit = *limit
				rateLimitReport.Actions[*actionName].Unit = limes.Unit(*unit)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}

		//Enrich the report with the default rate limits.
		for _, projectReport := range projects {
			for _, serviceReport := range projectReport.Services {
				if svcConfig, err := cluster.Config.GetServiceConfigurationForType(serviceReport.Type); err == nil {
					for _, defaultRateLimit := range svcConfig.Rates.ProjectDefault {
						rateLimitReport, exists := serviceReport.Rates[defaultRateLimit.TargetTypeURI]
						if !exists {
							rateLimitReport = &limes.ProjectRateLimitReport{
								TargetTypeURI: defaultRateLimit.TargetTypeURI,
								Actions:       make(limes.ProjectRateLimitActionReports),
							}
						}

						for _, defaultAction := range defaultRateLimit.Actions {
							rl, exists := rateLimitReport.Actions[defaultAction.Name]
							if !exists {
								rl = &limes.ProjectRateLimitActionReport{
									Name:  defaultAction.Name,
									Limit: defaultAction.Limit,
									Unit:  limes.Unit(defaultAction.Unit),
								}
							}
							//Indicate that the project rate limit or unit deviates from the default by adding
							// defaultLimit and/or defaultUnit.
							if rl.Limit != defaultAction.Limit {
								rl.DefaultLimit = defaultAction.Limit
							}
							if rl.Unit != limes.Unit(defaultAction.Unit) {
								rl.DefaultUnit = limes.Unit(defaultAction.Unit)
							}
							rateLimitReport.Actions[defaultAction.Name] = rl
						}
						serviceReport.Rates[defaultRateLimit.TargetTypeURI] = rateLimitReport
					}
				}
			}
		}
	}

	//flatten result (with stable order to keep the tests happy)
	uuids := make([]string, 0, len(projects))
	for uuid := range projects {
		uuids = append(uuids, uuid)
	}
	sort.Strings(uuids)
	result := make([]*limes.ProjectReport, len(projects))
	for idx, uuid := range uuids {
		result[idx] = projects[uuid]
	}

	return result, nil
}

type projects map[string]*limes.ProjectReport

func (p projects) Find(cluster *core.Cluster, projectUUID, projectName, projectParentUUID string, serviceType, resourceName, targetTypeURI, action *string, scrapedAt *time.Time) (*limes.ProjectReport, *limes.ProjectServiceReport, *limes.ProjectResourceReport, *limes.ProjectRateLimitReport) {
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
		return project, nil, nil, nil
	}
	// Ensure the ProjectServiceReport exists if the serviceType is given.
	service, exists := project.Services[*serviceType]
	if !exists {
		if !cluster.HasService(*serviceType) {
			return project, nil, nil, nil
		}
		service = &limes.ProjectServiceReport{
			ServiceInfo: cluster.InfoForService(*serviceType),
			Resources:   make(limes.ProjectResourceReports),
			Rates:       make(limes.ProjectRateLimitReports),
		}
		if scrapedAt != nil {
			scrapedAtUnix := time.Time(*scrapedAt).Unix()
			service.ScrapedAt = &scrapedAtUnix
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

	//Ensure the ProjectRateLimitReport exists if the targetTypeURI, action are given.
	var rateLimit *limes.ProjectRateLimitReport
	if targetTypeURI != nil && action != nil {
		rateLimit, exists = service.Rates[*targetTypeURI]
		if !exists {
			rateLimit = &limes.ProjectRateLimitReport{
				TargetTypeURI: *targetTypeURI,
				Actions:       make(limes.ProjectRateLimitActionReports),
			}
		}
		_, exists := rateLimit.Actions[*action]
		if !exists {
			rateLimit.Actions[*action] = &limes.ProjectRateLimitActionReport{Name: *action}
		}
		service.Rates[*targetTypeURI] = rateLimit
	}

	return project, service, resource, rateLimit
}
