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
	"time"

	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/util"
)

var domainReportQuery1 = `
	SELECT d.uuid, d.name, ps.type, pr.name, SUM(pr.quota), SUM(pr.usage),
	       SUM(GREATEST(pr.usage - pr.quota, 0)),
	       SUM(GREATEST(pr.backend_quota, 0)), MIN(pr.backend_quota) < 0,
	       SUM(COALESCE(pr.physical_usage, pr.usage)), COUNT(pr.physical_usage) > 0,
	       MIN(ps.scraped_at), MAX(ps.scraped_at)
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

//GetDomains returns reports for all domains in the given cluster or, if
//domainID is non-nil, for that domain only.
func GetDomains(cluster *core.Cluster, domainID *int64, dbi db.Interface, filter Filter) ([]*limes.DomainReport, error) {
	clusterCanBurst := cluster.Config.Bursting.MaxMultiplier > 0

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
			burstUsage           *uint64
			backendQuota         *uint64
			infiniteBackendQuota *bool
			physicalUsage        *uint64
			showPhysicalUsage    *bool
			minScrapedAt         *util.Time
			maxScrapedAt         *util.Time
		)
		err := rows.Scan(
			&domainUUID, &domainName, &serviceType, &resourceName,
			&projectsQuota, &usage, &burstUsage,
			&backendQuota, &infiniteBackendQuota,
			&physicalUsage, &showPhysicalUsage,
			&minScrapedAt, &maxScrapedAt,
		)
		if err != nil {
			return err
		}

		domain, service, resource := domains.Find(cluster, domainUUID, serviceType, resourceName)

		domain.Name = domainName

		if service != nil {
			if maxScrapedAt != nil {
				val := time.Time(*maxScrapedAt).Unix()
				service.MaxScrapedAt = &val
			}
			if minScrapedAt != nil {
				val := time.Time(*minScrapedAt).Unix()
				service.MinScrapedAt = &val
			}
		}

		if resource != nil {
			if usage != nil {
				resource.Usage = *usage
			}
			if clusterCanBurst && burstUsage != nil {
				resource.BurstUsage = *burstUsage
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
			if showPhysicalUsage != nil && *showPhysicalUsage {
				resource.PhysicalUsage = physicalUsage
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

	//for externally managed resources, set domain quota = sum(project quotas)
	//statically to display consistent data
	for _, domain := range domains {
		for _, srv := range domain.Services {
			for _, res := range srv.Resources {
				if res.ResourceInfo.ExternallyManaged {
					res.DomainQuota = res.ProjectsQuota
				}
			}
		}
	}

	//flatten result (with stable order to keep the tests happy)
	uuids := make([]string, 0, len(domains))
	for uuid := range domains {
		uuids = append(uuids, uuid)
	}
	sort.Strings(uuids)
	result := make([]*limes.DomainReport, len(domains))
	for idx, uuid := range uuids {
		result[idx] = domains[uuid]
	}

	return result, nil
}

type domains map[string]*limes.DomainReport

func (d domains) Find(cluster *core.Cluster, domainUUID string, serviceType, resourceName *string) (*limes.DomainReport, *limes.DomainServiceReport, *limes.DomainResourceReport) {
	domain, exists := d[domainUUID]
	if !exists {
		domain = &limes.DomainReport{
			UUID:     domainUUID,
			Services: make(limes.DomainServiceReports),
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
		service = &limes.DomainServiceReport{
			ServiceInfo: cluster.InfoForService(*serviceType),
			Resources:   make(limes.DomainResourceReports),
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
		resource = &limes.DomainResourceReport{
			ResourceInfo: cluster.InfoForResource(*serviceType, *resourceName),
			Scaling:      cluster.BehaviorForResource(*serviceType, *resourceName).ToScalingBehavior(),
		}
		service.Resources[*resourceName] = resource
	}

	return domain, service, resource
}
