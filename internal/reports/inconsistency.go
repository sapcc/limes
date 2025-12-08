// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

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

var ospqReportQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	SELECT d.uuid, d.name, p.uuid, p.name, s.type, r.name, pazr.quota, pazr.usage
	  FROM projects p
	  JOIN domains d ON d.id = p.domain_id
	  JOIN project_az_resources pazr ON pazr.project_id = p.id
	  JOIN az_resources azr ON pazr.az_resource_id = azr.id AND azr.az = {{liquid.AvailabilityZoneTotal}}
	  JOIN resources r ON azr.resource_id = r.id {{AND r.name = $resource_name}}
	  JOIN services s ON r.service_id = s.id {{AND s.type = $service_type}}
	WHERE pazr.usage > pazr.quota
	 ORDER BY d.name, p.name, s.type, r.name
`))

var mmpqReportQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	SELECT d.uuid, d.name, p.uuid, p.name, s.type, r.name, pazr.quota, pazr.backend_quota
	  FROM projects p
	  JOIN domains d ON d.id = p.domain_id
	  JOIN project_az_resources pazr ON pazr.project_id = p.id
	  JOIN az_resources azr ON azr.id = pazr.az_resource_id AND azr.az = {{liquid.AvailabilityZoneTotal}}
	  JOIN resources r ON azr.resource_id = r.id {{AND r.name = $resource_name}}
	  JOIN services s ON r.service_id = s.id {{AND s.type = $service_type}}
	WHERE pazr.backend_quota != pazr.quota
	ORDER BY d.name, p.name, s.type, r.name
`))

// GetInconsistencies returns Inconsistency reports for all inconsistencies and their projects in the current cluster.
func GetInconsistencies(cluster *core.Cluster, dbi db.Interface, filter Filter, serviceInfos map[db.ServiceType]liquid.ServiceInfo) (*Inconsistencies, error) {
	// Initialize inconsistencies as Inconsistencies type.
	// The inconsistency data will be assigned in the respective SQL queries.
	inconsistencies := Inconsistencies{
		// ensure that empty lists get serialized as `[]` rather than as `null`
		OvercommittedQuotas: []struct{}{},
		OverspentQuotas:     []OverspentProjectQuota{},
		MismatchQuotas:      []MismatchProjectQuota{},
	}

	nm := core.BuildResourceNameMapping(cluster, serviceInfos)

	// ospqReportQuery: data for overspent project quota inconsistencies
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

		serviceInfo := core.InfoForService(serviceInfos, dbServiceType)
		ospq.Unit = core.InfoForResource(serviceInfo, dbResourceName).Unit
		inconsistencies.OverspentQuotas = append(inconsistencies.OverspentQuotas, ospq)

		return nil
	})
	if err != nil {
		return nil, err
	}

	// mmpqReportQuery: data for mismatch project quota inconsistencies
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

		serviceInfo := core.InfoForService(serviceInfos, dbServiceType)
		mmpq.Unit = core.InfoForResource(serviceInfo, dbResourceName).Unit
		inconsistencies.MismatchQuotas = append(inconsistencies.MismatchQuotas, mmpq)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &inconsistencies, nil
}
