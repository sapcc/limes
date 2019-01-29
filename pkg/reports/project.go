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
	"github.com/sapcc/limes/pkg/util"
)

var projectReportQuery = `
	SELECT p.uuid, p.name, COALESCE(p.parent_uuid, ''), p.has_bursting, ps.type, ps.scraped_at, pr.name, pr.quota, pr.usage, pr.backend_quota, pr.subresources
	  FROM projects p
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	 WHERE %s
`

//GetProjects returns limes.ProjectReport reports for all projects in the given domain or,
//if projectID is non-nil, for that project only.
func GetProjects(cluster *core.Cluster, domainID int64, projectID *int64, dbi db.Interface, filter Filter, withSubresources bool) ([]*limes.ProjectReport, error) {
	clusterCanBurst := cluster.Config.Bursting.MaxMultiplier > 0

	fields := map[string]interface{}{"p.domain_id": domainID}
	if projectID != nil {
		fields["p.id"] = *projectID
	}

	//avoid collecting the potentially large subresources strings when possible
	queryStr := projectReportQuery
	if !withSubresources {
		queryStr = strings.Replace(queryStr, "pr.subresources", "''", 1)
	}

	projects := make(map[string]*limes.ProjectReport)
	queryStr, joinArgs := filter.PrepareQuery(queryStr)
	whereStr, whereArgs := db.BuildSimpleWhereClause(fields, len(joinArgs))
	err := db.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			projectUUID        string
			projectName        string
			projectParentUUID  string
			projectHasBursting bool
			serviceType        *string
			scrapedAt          *util.Time
			resourceName       *string
			quota              *uint64
			usage              *uint64
			backendQuota       *int64
			subresources       *string
		)
		err := rows.Scan(
			&projectUUID, &projectName, &projectParentUUID, &projectHasBursting,
			&serviceType, &scrapedAt, &resourceName,
			&quota, &usage, &backendQuota, &subresources,
		)
		if err != nil {
			return err
		}

		project, exists := projects[projectUUID]
		if !exists {
			project = &limes.ProjectReport{
				UUID:       projectUUID,
				Name:       projectName,
				ParentUUID: projectParentUUID,
				Services:   make(limes.ProjectServiceReports),
			}
			projects[projectUUID] = project

			if clusterCanBurst {
				project.Bursting = &limes.ProjectBurstingInfo{
					Enabled:    projectHasBursting,
					Multiplier: cluster.Config.Bursting.MaxMultiplier,
				}
			}
		}

		if serviceType != nil {
			service, exists := project.Services[*serviceType]
			if !exists {
				if cluster.HasService(*serviceType) {
					service = &limes.ProjectServiceReport{
						ServiceInfo: cluster.InfoForService(*serviceType),
						Resources:   make(limes.ProjectResourceReports),
					}
					if scrapedAt != nil {
						val := time.Time(*scrapedAt).Unix()
						service.ScrapedAt = &val
					}
					project.Services[*serviceType] = service
				}
			}

			if resourceName != nil {
				if cluster.HasResource(*serviceType, *resourceName) {
					subresourcesValue := ""
					if subresources != nil {
						subresourcesValue = *subresources
					}

					behavior := cluster.BehaviorForResource(*serviceType, *resourceName)
					resource := &limes.ProjectResourceReport{
						ResourceInfo: cluster.InfoForResource(*serviceType, *resourceName),
						Scaling:      behavior.ToScalingBehavior(),
						Usage:        *usage,
						BackendQuota: nil, //see below
						Subresources: limes.JSONString(subresourcesValue),
					}
					if usage != nil {
						resource.Usage = *usage
					}
					if quota != nil {
						resource.Quota = *quota
						desiredQuota := *quota
						if projectHasBursting && clusterCanBurst {
							desiredQuota = behavior.MaxBurstMultiplier.ApplyTo(*quota)
						}
						if backendQuota != nil && (*backendQuota < 0 || uint64(*backendQuota) != desiredQuota) {
							resource.BackendQuota = backendQuota
						}
					}
					if projectHasBursting && clusterCanBurst && quota != nil && usage != nil {
						if *usage > *quota {
							resource.BurstUsage = *usage - *quota
						}
					}
					service.Resources[*resourceName] = resource
				}

			}
		}

		return nil
	})
	if err != nil {
		return nil, err
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
