/*******************************************************************************
*
* Copyright 2018 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package reports

import (
	"database/sql"
	"fmt"

	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
)

//Inconsistencies contains aggregated data about inconsistent quota setups for domains and projects
//in the current cluster.
type Inconsistencies struct {
	ClusterID           string                     `json:"cluster_id"`
	OvercommittedQuotas []OvercommittedDomainQuota `json:"domain_quota_overcommitted,keepempty"`
	OverspentQuotas     []OverspentProjectQuota    `json:"project_quota_overspent,keepempty"`
	MismatchQuotas      []MismatchProjectQuota     `json:"project_quota_mismatch,keepempty"`
}

//OvercommittedDomainQuota is a substructure of Inconsistency containing data for the inconsistency type
//where for a domain the sum(projects_quota) > domain_quota for a single resource.
type OvercommittedDomainQuota struct {
	Domain        DomainData `json:"domain,keepempty"`
	Service       string     `json:"service,keepempty"`
	Resource      string     `json:"resource,keepempty"`
	Unit          limes.Unit `json:"unit,omitempty"`
	DomainQuota   uint64     `json:"domain_quota,keepempty"`
	ProjectsQuota uint64     `json:"projects_quota,keepempty"`
}

//OverspentProjectQuota is a substructure of Inconsistency containing data for the inconsistency type
//where for some project the usage > quota for a single resource.
type OverspentProjectQuota struct {
	Project  ProjectData `json:"project,keepempty"`
	Service  string      `json:"service,keepempty"`
	Resource string      `json:"resource,keepempty"`
	Unit     limes.Unit  `json:"unit,omitempty"`
	Quota    uint64      `json:"quota,keepempty"`
	Usage    uint64      `json:"usage,keepempty"`
}

//MismatchProjectQuota is a substructure of Inconsistency containing data for the inconsistency type
//where for some project the quota != backend_quota for a single resource.
type MismatchProjectQuota struct {
	Project      ProjectData `json:"project,keepempty"`
	Service      string      `json:"service,keepempty"`
	Resource     string      `json:"resource,keepempty"`
	Unit         limes.Unit  `json:"unit,omitempty"`
	Quota        uint64      `json:"quota,keepempty"`
	BackendQuota int64       `json:"backend_quota,keepempty"`
}

//DomainData is a substructure containing domain data for a single inconsistency
type DomainData struct {
	UUID string `json:"id"`
	Name string `json:"name"`
}

//ProjectData is a substructure containing project data for a single inconsistency
type ProjectData struct {
	UUID   string     `json:"id"`
	Name   string     `json:"name"`
	Domain DomainData `json:"domain,keepempty"`
}

var ocdqReportQuery = `
	SELECT d.uuid, d.name, ps.type, pr.name, MAX(COALESCE(dr.quota, 0)), SUM(pr.quota)
	  FROM domains d
	  JOIN projects p ON p.domain_id = d.id
	  JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	  LEFT OUTER JOIN domain_services ds ON ds.domain_id = d.id AND ds.type = ps.type
	  LEFT OUTER JOIN domain_resources dr ON dr.service_id = ds.id AND dr.name = pr.name
	WHERE %s GROUP BY d.uuid, d.name, ps.type, pr.name
	HAVING MAX(COALESCE(dr.quota, 0)) < SUM(pr.quota)
	ORDER BY d.uuid ASC;
`

var ospqReportQuery = `
	SELECT d.uuid, d.name, p.uuid, p.name, ps.type, pr.name, SUM(pr.quota), SUM(pr.usage)
	  FROM projects p
	  LEFT OUTER JOIN domains d ON d.id=p.domain_id
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	WHERE %s GROUP BY d.uuid, d.name, p.uuid, p.name, ps.type, pr.name
	HAVING SUM(pr.usage) > SUM(pr.quota)
	ORDER BY p.uuid ASC
`

var mmpqReportQuery = `
	SELECT d.uuid, d.name, p.uuid, p.name, ps.type, pr.name, SUM(pr.quota), SUM(pr.backend_quota)
	  FROM projects p
	  LEFT OUTER JOIN domains d ON d.id=p.domain_id
	  LEFT OUTER JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  LEFT OUTER JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	WHERE %s GROUP BY d.uuid, d.name, p.uuid, p.name, ps.type, pr.name
	HAVING SUM(pr.quota) != SUM(pr.backend_quota)
	ORDER BY p.uuid ASC
`

//GetInconsistencies returns Inconsistency reports for all inconsistencies and their projects in the current cluster.
func GetInconsistencies(cluster *limes.Cluster, dbi db.Interface, filter Filter) (*Inconsistencies, error) {
	fields := map[string]interface{}{"d.cluster_id": cluster.ID}

	//Initialize inconsistencies as Inconsistencies type and assign ClusterID.
	//The inconsistency data will be assigned in the respective SQL queries.
	inconsistencies := Inconsistencies{
		ClusterID: cluster.ID,
		//ensure that empty lists get serialized as `[]` rather than as `null`
		OvercommittedQuotas: []OvercommittedDomainQuota{},
		OverspentQuotas:     []OverspentProjectQuota{},
		MismatchQuotas:      []MismatchProjectQuota{},
	}

	//ocdqReportQuery: data for OvercommittedDomainQuota inconsistencies.
	queryStr, joinArgs := filter.PrepareQuery(ocdqReportQuery)
	whereStr, whereArgs := db.BuildSimpleWhereClause(fields, len(joinArgs))
	err := db.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		ocdq := OvercommittedDomainQuota{}
		err := rows.Scan(
			&ocdq.Domain.UUID, &ocdq.Domain.Name, &ocdq.Service,
			&ocdq.Resource, &ocdq.DomainQuota, &ocdq.ProjectsQuota,
		)
		if err != nil {
			return err
		}

		ocdq.Unit = cluster.InfoForResource(ocdq.Service, ocdq.Resource).Unit
		inconsistencies.OvercommittedQuotas = append(inconsistencies.OvercommittedQuotas, ocdq)

		return err
	})

	//ospqReportQuery: data for OverspentProjectQuota inconsistencies.
	queryStr, joinArgs = filter.PrepareQuery(ospqReportQuery)
	whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))
	err = db.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		ospq := OverspentProjectQuota{}
		err := rows.Scan(
			&ospq.Project.Domain.UUID, &ospq.Project.Domain.Name, &ospq.Project.UUID,
			&ospq.Project.Name, &ospq.Service, &ospq.Resource, &ospq.Quota, &ospq.Usage,
		)
		if err != nil {
			return err
		}

		ospq.Unit = cluster.InfoForResource(ospq.Service, ospq.Resource).Unit
		inconsistencies.OverspentQuotas = append(inconsistencies.OverspentQuotas, ospq)

		return err
	})

	//mmpqReportQuery: data for MismatchProjectQuota inconsistencies.
	queryStr, joinArgs = filter.PrepareQuery(mmpqReportQuery)
	whereStr, whereArgs = db.BuildSimpleWhereClause(fields, len(joinArgs))
	err = db.ForeachRow(db.DB, fmt.Sprintf(queryStr, whereStr), append(joinArgs, whereArgs...), func(rows *sql.Rows) error {
		mmpq := MismatchProjectQuota{}
		err := rows.Scan(
			&mmpq.Project.Domain.UUID, &mmpq.Project.Domain.Name, &mmpq.Project.UUID,
			&mmpq.Project.Name, &mmpq.Service, &mmpq.Resource, &mmpq.Quota, &mmpq.BackendQuota,
		)
		if err != nil {
			return err
		}

		mmpq.Unit = cluster.InfoForResource(mmpq.Service, mmpq.Resource).Unit
		inconsistencies.MismatchQuotas = append(inconsistencies.MismatchQuotas, mmpq)

		return err
	})

	if err != nil {
		return nil, err
	}

	return &inconsistencies, nil
}
