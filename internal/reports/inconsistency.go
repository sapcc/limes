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

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// Inconsistencies contains aggregated data about inconsistent quota setups for
// domains and projects in the current cluster.
type Inconsistencies struct {
	OvercommittedQuotas []struct{}              `json:"domain_quota_overcommitted"` // legacy, cannot occur anymore
	OverspentQuotas     []OverspentProjectQuota `json:"project_quota_overspent"`
	MismatchQuotas      []MismatchProjectQuota  `json:"project_quota_mismatch"`
}

// OverspentProjectQuota is a substructure of Inconsistency containing data for
// the inconsistency type where for some project the 'usage > quota' for a
// single resource.
type OverspentProjectQuota struct {
	Project  core.KeystoneProject        `json:"project"`
	Service  limes.ServiceType           `json:"service"`
	Resource limesresources.ResourceName `json:"resource"`
	Unit     limes.Unit                  `json:"unit,omitempty"`
	Quota    uint64                      `json:"quota"`
	Usage    uint64                      `json:"usage"`
}

// MismatchProjectQuota is a substructure of Inconsistency containing data for
// the inconsistency type where for some project the 'backend_quota != quota'
// for a single resource.
type MismatchProjectQuota struct {
	Project      core.KeystoneProject        `json:"project"`
	Service      limes.ServiceType           `json:"service"`
	Resource     limesresources.ResourceName `json:"resource"`
	Unit         limes.Unit                  `json:"unit,omitempty"`
	Quota        uint64                      `json:"quota"`
	BackendQuota int64                       `json:"backend_quota"`
}

var ospqReportQuery = sqlext.SimplifyWhitespace(`
	SELECT d.uuid, d.name, p.uuid, p.name, ps.type, pr.name, pr.quota, SUM(par.usage)
	  FROM projects p
	  JOIN domains d ON d.id = p.domain_id
	  JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	  JOIN project_az_resources par ON pr.id = par.resource_id
	 GROUP BY d.uuid, d.name, p.uuid, p.name, ps.type, pr.name, pr.quota
	HAVING SUM(par.usage) > pr.quota
	 ORDER BY d.name, p.name, ps.type, pr.name
`)

var mmpqReportQuery = sqlext.SimplifyWhitespace(`
	SELECT d.uuid, d.name, p.uuid, p.name, ps.type, pr.name, pr.quota, pr.backend_quota
	  FROM projects p
	  JOIN domains d ON d.id = p.domain_id
	  JOIN project_services ps ON ps.project_id = p.id {{AND ps.type = $service_type}}
	  JOIN project_resources pr ON pr.service_id = ps.id {{AND pr.name = $resource_name}}
	WHERE pr.backend_quota != pr.quota
	ORDER BY d.name, p.name, ps.type, pr.name
`)

// GetInconsistencies returns Inconsistency reports for all inconsistencies and their projects in the current cluster.
func GetInconsistencies(cluster *core.Cluster, dbi db.Interface, filter Filter) (*Inconsistencies, error) {
	// Initialize inconsistencies as Inconsistencies type.
	// The inconsistency data will be assigned in the respective SQL queries.
	inconsistencies := Inconsistencies{
		// ensure that empty lists get serialized as `[]` rather than as `null`
		OvercommittedQuotas: []struct{}{},
		OverspentQuotas:     []OverspentProjectQuota{},
		MismatchQuotas:      []MismatchProjectQuota{},
	}
	nm := core.BuildResourceNameMapping(cluster)

	//ospqReportQuery: data for overspent project quota inconsistencies
	queryStr, joinArgs := filter.PrepareQuery(ospqReportQuery)
	//nolint:dupl
	err := sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			ospq           OverspentProjectQuota
			dbServiceType  db.ServiceType
			dbResourceName liquid.ResourceName
		)
		err := rows.Scan(
			&ospq.Project.Domain.UUID, &ospq.Project.Domain.Name,
			&ospq.Project.UUID, &ospq.Project.Name, &dbServiceType,
			&dbResourceName, &ospq.Quota, &ospq.Usage,
		)
		if err != nil {
			return err
		}

		var exists bool
		ospq.Service, ospq.Resource, exists = nm.MapToV1API(dbServiceType, dbResourceName)
		if !exists {
			return nil
		}

		ospq.Unit = cluster.InfoForResource(dbServiceType, dbResourceName).Unit
		inconsistencies.OverspentQuotas = append(inconsistencies.OverspentQuotas, ospq)

		return nil
	})
	if err != nil {
		return nil, err
	}

	//mmpqReportQuery: data for mismatch project quota inconsistencies
	queryStr, joinArgs = filter.PrepareQuery(mmpqReportQuery)
	//nolint:dupl
	err = sqlext.ForeachRow(dbi, queryStr, joinArgs, func(rows *sql.Rows) error {
		var (
			mmpq           MismatchProjectQuota
			dbServiceType  db.ServiceType
			dbResourceName liquid.ResourceName
		)
		err := rows.Scan(
			&mmpq.Project.Domain.UUID, &mmpq.Project.Domain.Name,
			&mmpq.Project.UUID, &mmpq.Project.Name, &dbServiceType,
			&dbResourceName, &mmpq.Quota, &mmpq.BackendQuota,
		)
		if err != nil {
			return err
		}

		var exists bool
		mmpq.Service, mmpq.Resource, exists = nm.MapToV1API(dbServiceType, dbResourceName)
		if !exists {
			return nil
		}

		mmpq.Unit = cluster.InfoForResource(dbServiceType, dbResourceName).Unit
		inconsistencies.MismatchQuotas = append(inconsistencies.MismatchQuotas, mmpq)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &inconsistencies, nil
}
