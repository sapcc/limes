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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

//Project contains all data about resource usage in a project.
type Project struct {
	UUID       string          `json:"id"`
	Name       string          `json:"name"`
	ParentUUID string          `json:"parent_id"`
	Services   ProjectServices `json:"services,keepempty"`
}

//ProjectService is a substructure of Project containing data for
//a single backend service.
type ProjectService struct {
	limes.ServiceInfo
	Resources ProjectResources `json:"resources,keepempty"`
	ScrapedAt int64            `json:"scraped_at,omitempty"`
}

//ProjectResource is a substructure of Project containing data for
//a single resource.
type ProjectResource struct {
	limes.ResourceInfo
	Quota uint64 `json:"quota,keepempty"`
	Usage uint64 `json:"usage,keepempty"`
	//This is a pointer to a value to enable precise control over whether this field is rendered in output.
	BackendQuota *int64          `json:"backend_quota,omitempty"`
	Subresources util.JSONString `json:"subresources,omitempty"`
}

//ProjectServices provides fast lookup of services using a map, but serializes
//to JSON as a list.
type ProjectServices map[string]*ProjectService

//MarshalJSON implements the json.Marshaler interface.
func (s ProjectServices) MarshalJSON() ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	types := make([]string, 0, len(s))
	for typeStr := range s {
		types = append(types, typeStr)
	}
	sort.Strings(types)
	list := make([]*ProjectService, len(s))
	for idx, typeStr := range types {
		list[idx] = s[typeStr]
	}
	return json.Marshal(list)
}

//ProjectResources provides fast lookup of resources using a map, but serializes
//to JSON as a list.
type ProjectResources map[string]*ProjectResource

//MarshalJSON implements the json.Marshaler interface.
func (r ProjectResources) MarshalJSON() ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]*ProjectResource, len(r))
	for idx, name := range names {
		list[idx] = r[name]
	}
	return json.Marshal(list)
}

var projectReportQuery = `
	SELECT p.uuid, p.name, COALESCE(p.parent_uuid, ''), ps.type, ps.scraped_at, pr.name, pr.quota, pr.usage, pr.backend_quota, pr.subresources
	  FROM projects p
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	 WHERE %s
`

//GetProjects returns Project reports for all projects in the given domain or,
//if projectID is non-nil, for that project only.
func GetProjects(cluster *limes.Cluster, domainID int64, projectID *int64, dbi db.Interface, filter Filter, withSubresources bool) ([]*Project, error) {
	fields := map[string]interface{}{"p.domain_id": domainID}
	if projectID != nil {
		fields["p.id"] = *projectID
	}

	//avoid collecting the potentially large subresources strings when possible
	queryStr := projectReportQuery
	if !withSubresources {
		queryStr = strings.Replace(queryStr, "pr.subresources", "''", 1)
	}

	queryStr, joinArgs := filter.PrepareQuery(queryStr)
	whereStr, whereArgs := db.BuildSimpleWhereClause(fields, len(joinArgs))
	rows, err := dbi.Query(fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...)...)
	if err != nil {
		return nil, err
	}

	projects := make(map[string]*Project)
	for rows.Next() {
		var (
			projectUUID       string
			projectName       string
			projectParentUUID string
			serviceType       *string
			scrapedAt         *util.Time
			resourceName      *string
			quota             *uint64
			usage             *uint64
			backendQuota      *int64
			subresources      *string
		)
		err := rows.Scan(
			&projectUUID, &projectName, &projectParentUUID,
			&serviceType, &scrapedAt, &resourceName,
			&quota, &usage, &backendQuota, &subresources,
		)
		if err != nil {
			rows.Close()
			return nil, err
		}

		project, exists := projects[projectUUID]
		if !exists {
			project = &Project{
				UUID:       projectUUID,
				Name:       projectName,
				ParentUUID: projectParentUUID,
				Services:   make(ProjectServices),
			}
			projects[projectUUID] = project
		}

		if serviceType == nil {
			continue
		}

		service, exists := project.Services[*serviceType]
		if !exists {
			if !cluster.HasService(*serviceType) {
				continue
			}
			service = &ProjectService{
				ServiceInfo: cluster.InfoForService(*serviceType),
				Resources:   make(ProjectResources),
			}
			if scrapedAt != nil {
				service.ScrapedAt = time.Time(*scrapedAt).Unix()
			}
			project.Services[*serviceType] = service
		}

		if resourceName == nil {
			continue
		}
		if !cluster.HasResource(*serviceType, *resourceName) {
			continue
		}

		subresourcesValue := ""
		if subresources != nil {
			subresourcesValue = *subresources
		}

		resource := &ProjectResource{
			ResourceInfo: cluster.InfoForResource(*serviceType, *resourceName),
			Usage:        *usage,
			BackendQuota: nil, //see below
			Subresources: util.JSONString(subresourcesValue),
		}
		if usage != nil {
			resource.Usage = *usage
		}
		if quota != nil {
			resource.Quota = *quota
			if backendQuota != nil && (*backendQuota < 0 || uint64(*backendQuota) != *quota) {
				resource.BackendQuota = backendQuota
			}
		}
		service.Resources[*resourceName] = resource
	}
	err = rows.Err()
	if err != nil {
		rows.Close()
		return nil, err
	}
	err = rows.Close()
	if err != nil {
		return nil, err
	}

	//flatten result (with stable order to keep the tests happy)
	uuids := make([]string, 0, len(projects))
	for uuid := range projects {
		uuids = append(uuids, uuid)
	}
	sort.Strings(uuids)
	result := make([]*Project, len(projects))
	for idx, uuid := range uuids {
		result[idx] = projects[uuid]
	}

	return result, nil
}
