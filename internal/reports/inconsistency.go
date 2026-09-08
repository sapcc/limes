// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports

import (
	"context"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/sqlext"
	"go.xyrillian.de/oblast"

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
	Project      core.KeystoneProject        `json:"project"`
	Service      limes.ServiceType           `json:"service" db:"-"`
	Resource     limesresources.ResourceName `json:"resource" db:"-"`
	Unit         limes.Unit                  `json:"unit,omitzero" db:"-"`
	ServiceType  db.ServiceType              `json:"-" db:"type"`
	ResourceName liquid.ResourceName         `json:"-" db:"name"`
	Quota        uint64                      `json:"quota" db:"quota"`
	Usage        uint64                      `json:"usage" db:"usage"`
}

// MismatchProjectQuota is a substructure of Inconsistency containing data for
// the inconsistency type where for some project the 'backend_quota != quota'
// for a single resource.
type MismatchProjectQuota struct {
	Project        core.KeystoneProject        `json:"project"`
	Service        limes.ServiceType           `json:"service" db:"-"`
	Resource       limesresources.ResourceName `json:"resource" db:"-"`
	Unit           limes.Unit                  `json:"unit,omitzero" db:"-"`
	DBServiceType  db.ServiceType              `json:"-" db:"type"`
	DBResourceName liquid.ResourceName         `json:"-" db:"name"`
	Quota          uint64                      `json:"quota" db:"quota"`
	BackendQuota   int64                       `json:"backend_quota" db:"backend_quota"`
}

var ospqReportQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	SELECT d.uuid AS domain_uuid, d.name AS domain_name, p.uuid AS project_uuid, p.name AS project_name, s.type, r.name, pazr.quota, pazr.usage
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
	SELECT d.uuid AS domain_uuid, d.name AS domain_name, p.uuid AS project_uuid, p.name AS project_name, s.type, r.name, pazr.quota, pazr.backend_quota
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
func GetInconsistencies(ctx context.Context, cluster *core.Cluster, dbi db.Interface, filter Filter, sis core.ServiceInfoSnapshot) (*Inconsistencies, error) {
	// Initialize inconsistencies as Inconsistencies type.
	// The inconsistency data will be assigned in the respective SQL queries.
	inconsistencies := Inconsistencies{
		// ensure that empty lists get serialized as `[]` rather than as `null`
		OvercommittedQuotas: []struct{}{},
		OverspentQuotas:     []OverspentProjectQuota{},
		MismatchQuotas:      []MismatchProjectQuota{},
	}

	nm := core.BuildResourceNameMapping(cluster, sis)

	// ospqReportQuery: data for overspent project quota inconsistencies
	queryStr, joinArgs := filter.PrepareQuery(ospqReportQuery)
	err := oblast.MustNewStore[OverspentProjectQuota](oblast.PostgresDialect()).Select(ctx, dbi, queryStr, joinArgs...).Foreach(func(r OverspentProjectQuota) error {
		path := db.ResourcePath{ServiceType: r.ServiceType, ResourceName: r.ResourceName}

		var exists bool
		r.Service, r.Resource, exists = nm.MapToV1API(r.ServiceType, r.ResourceName)
		if !exists {
			return nil
		}

		// we ignore when a resource can't be found in the app layer yet, it will appear with default value
		resource, _ := sis.GetResourceForPath(path)
		r.Unit = core.ConvertUnitToV1(resource.Unit)
		inconsistencies.OverspentQuotas = append(inconsistencies.OverspentQuotas, r)

		return nil
	})
	if err != nil {
		return nil, err
	}

	// mmpqReportQuery: data for mismatch project quota inconsistencies
	queryStr, joinArgs = filter.PrepareQuery(mmpqReportQuery)
	err = oblast.MustNewStore[MismatchProjectQuota](oblast.PostgresDialect()).Select(ctx, dbi, queryStr, joinArgs...).Foreach(func(r MismatchProjectQuota) error {
		var exists bool
		r.Service, r.Resource, exists = nm.MapToV1API(r.DBServiceType, r.DBResourceName)
		if !exists {
			return nil
		}

		// we ignore when a resource can't be found in the app layer yet, it will appear with default value
		resource, _ := sis.GetResourceForPath(db.ResourcePath{ServiceType: r.DBServiceType, ResourceName: r.DBResourceName})
		r.Unit = core.ConvertUnitToV1(resource.Unit)
		inconsistencies.MismatchQuotas = append(inconsistencies.MismatchQuotas, r)

		return nil
	})
	if err != nil {
		return nil, err
	}

	return &inconsistencies, nil
}
