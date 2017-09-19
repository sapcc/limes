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
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

//Domain contains aggregated data about resource usage in a domain.
type Domain struct {
	UUID     string         `json:"id"`
	Name     string         `json:"name"`
	Services DomainServices `json:"services,keepempty"`
}

//DomainService is a substructure of Domain containing data for
//a single backend service.
type DomainService struct {
	limes.ServiceInfo
	Resources    DomainResources `json:"resources,keepempty"`
	MaxScrapedAt int64           `json:"max_scraped_at,omitempty"`
	MinScrapedAt int64           `json:"min_scraped_at,omitempty"`
}

//DomainResource is a substructure of Domain containing data for
//a single resource.
type DomainResource struct {
	limes.ResourceInfo
	DomainQuota   uint64 `json:"quota,keepempty"`
	ProjectsQuota uint64 `json:"projects_quota,keepempty"`
	Usage         uint64 `json:"usage,keepempty"`
	//These are pointers to values to enable precise control over whether this field is rendered in output.
	BackendQuota         *uint64 `json:"backend_quota,omitempty"`
	InfiniteBackendQuota *bool   `json:"infinite_backend_quota,omitempty"`
}

//DomainServices provides fast lookup of services using a map, but serializes
//to JSON as a list.
type DomainServices map[string]*DomainService

//MarshalJSON implements the json.Marshaler interface.
func (s DomainServices) MarshalJSON() ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	types := make([]string, 0, len(s))
	for typeStr := range s {
		types = append(types, typeStr)
	}
	sort.Strings(types)
	list := make([]*DomainService, len(s))
	for idx, typeStr := range types {
		list[idx] = s[typeStr]
	}
	return json.Marshal(list)
}

//DomainResources provides fast lookup of resources using a map, but serializes
//to JSON as a list.
type DomainResources map[string]*DomainResource

//MarshalJSON implements the json.Marshaler interface.
func (r DomainResources) MarshalJSON() ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	names := make([]string, 0, len(r))
	for name := range r {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]*DomainResource, len(r))
	for idx, name := range names {
		list[idx] = r[name]
	}
	return json.Marshal(list)
}

var domainReportQuery1 = `
	SELECT d.uuid, d.name, ps.type, pr.name, SUM(pr.quota), SUM(pr.usage),
	       SUM(GREATEST(pr.backend_quota, 0)), MIN(pr.backend_quota) < 0, MIN(ps.scraped_at), MAX(ps.scraped_at)
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	 WHERE %s GROUP BY d.uuid, d.name, ps.type, pr.name
`

var domainReportQuery2 = `
	SELECT d.uuid, d.name, ds.type, dr.name, dr.quota
	  FROM domains d
	  LEFT OUTER JOIN domain_services ds ON ds.domain_id = d.id {{AND ds.type = $service_type}}
	  LEFT OUTER JOIN domain_resources dr ON dr.service_id = ds.id {{AND dr.name = $resource_name}}
	 WHERE %s
`

//GetDomains returns Domain reports for all domains in the given cluster or, if
//domainID is non-nil, for that domain only.
func GetDomains(cluster *limes.Cluster, domainID *int64, dbi db.Interface, filter Filter) ([]*Domain, error) {
	fields := map[string]interface{}{"d.cluster_id": cluster.ID}
	if domainID != nil {
		fields["d.id"] = *domainID
	}

	//first query: data for projects in this domain
	domains := make(domains)
	queryStr, joinArgs := filter.PrepareQuery(domainReportQuery1)
	whereStr, whereArgs := db.BuildSimpleWhereClause(fields, len(joinArgs))
	err := db.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			domainUUID           string
			domainName           string
			serviceType          *string
			resourceName         *string
			projectsQuota        *uint64
			usage                *uint64
			backendQuota         *uint64
			infiniteBackendQuota *bool
			minScrapedAt         *util.Time
			maxScrapedAt         *util.Time
		)
		err := rows.Scan(
			&domainUUID, &domainName, &serviceType, &resourceName,
			&projectsQuota, &usage, &backendQuota, &infiniteBackendQuota,
			&minScrapedAt, &maxScrapedAt,
		)
		if err != nil {
			return err
		}

		domain, service, resource := domains.Find(cluster, domainUUID, serviceType, resourceName)

		domain.Name = domainName

		if service != nil {
			if maxScrapedAt != nil {
				service.MaxScrapedAt = time.Time(*maxScrapedAt).Unix()
			}
			if minScrapedAt != nil {
				service.MinScrapedAt = time.Time(*minScrapedAt).Unix()
			}
		}

		if resource != nil {
			if usage != nil {
				resource.Usage = *usage
			}
			if projectsQuota != nil {
				resource.ProjectsQuota = *projectsQuota
				if backendQuota != nil && *projectsQuota != *backendQuota {
					resource.BackendQuota = backendQuota
				}
			}
			if infiniteBackendQuota != nil && *infiniteBackendQuota {
				resource.InfiniteBackendQuota = infiniteBackendQuota
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	//second query: add domain quotas
	queryStr, joinArgs = filter.PrepareQuery(domainReportQuery2)
	whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))
	err = db.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		var (
			domainUUID   string
			domainName   string
			serviceType  *string
			resourceName *string
			quota        *uint64
		)
		err := rows.Scan(
			&domainUUID, &domainName, &serviceType, &resourceName, &quota,
		)
		if err != nil {
			return err
		}

		domain, _, resource := domains.Find(cluster, domainUUID, serviceType, resourceName)

		domain.Name = domainName

		if resource != nil && quota != nil {
			resource.DomainQuota = *quota
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	//flatten result (with stable order to keep the tests happy)
	uuids := make([]string, 0, len(domains))
	for uuid := range domains {
		uuids = append(uuids, uuid)
	}
	sort.Strings(uuids)
	result := make([]*Domain, len(domains))
	for idx, uuid := range uuids {
		result[idx] = domains[uuid]
	}

	return result, nil
}

type domains map[string]*Domain

func (d domains) Find(cluster *limes.Cluster, domainUUID string, serviceType, resourceName *string) (*Domain, *DomainService, *DomainResource) {
	domain, exists := d[domainUUID]
	if !exists {
		domain = &Domain{
			UUID:     domainUUID,
			Services: make(DomainServices),
		}
		d[domainUUID] = domain
	}

	if serviceType == nil {
		return domain, nil, nil
	}

	service, exists := domain.Services[*serviceType]
	if !exists {
		if !cluster.HasService(*serviceType) {
			return domain, nil, nil
		}
		service = &DomainService{
			ServiceInfo: cluster.InfoForService(*serviceType),
			Resources:   make(DomainResources),
		}
		domain.Services[*serviceType] = service
	}

	if resourceName == nil {
		return domain, service, nil
	}

	resource, exists := service.Resources[*resourceName]
	if !exists {
		if !cluster.HasResource(*serviceType, *resourceName) {
			return domain, service, resource
		}
		resource = &DomainResource{
			ResourceInfo: cluster.InfoForResource(*serviceType, *resourceName),
		}
		service.Resources[*resourceName] = resource
	}

	return domain, service, resource
}
