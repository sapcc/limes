// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"database/sql"
	"fmt"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// NewCapacityScrapeBackchannel builds a CapacityScrapeBackchannel.
func NewCapacityScrapeBackchannel(cluster *core.Cluster, dbi db.Interface) core.CapacityScrapeBackchannel {
	return capacityScrapeBackchannelImpl{cluster, dbi}
}

type capacityScrapeBackchannelImpl struct {
	Cluster *core.Cluster
	DB      db.Interface
}

var (
	getResourceDemandQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		SELECT cazr.az, pazr.usage, COALESCE(pc_view.confirmed, 0), COALESCE(pc_view.pending, 0)
		  FROM services cs
		  JOIN resources cr ON cr.service_id = cs.id
		  JOIN az_resources cazr ON cazr.resource_id = cr.id
		  JOIN project_az_resources pazr ON pazr.az_resource_id = cazr.id
		  LEFT OUTER JOIN (
		    SELECT az_resource_id, project_id,
		           SUM(amount) FILTER (WHERE status = {{liquid.CommitmentStatusConfirmed}}) AS confirmed,
		           SUM(amount) FILTER (WHERE status = {{liquid.CommitmentStatusPending}}) AS pending
		      FROM project_commitments
		     GROUP BY az_resource_id, project_id
		  ) pc_view ON pc_view.az_resource_id = cazr.id AND pc_view.project_id = pazr.project_id
		 WHERE cs.type = $1 AND cr.name = $2
	`))
)

// GetResourceDemand implements the CapacityScrapeBackchannel interface.
func (i capacityScrapeBackchannelImpl) GetResourceDemand(serviceType db.ServiceType, resourceName liquid.ResourceName) (liquid.ResourceDemand, error) {
	result := liquid.ResourceDemand{
		OvercommitFactor: i.Cluster.BehaviorForResource(serviceType, resourceName).OvercommitFactor,
		PerAZ:            make(map[limes.AvailabilityZone]liquid.ResourceDemandInAZ),
	}
	err := sqlext.ForeachRow(i.DB, getResourceDemandQuery, []any{serviceType, resourceName}, func(rows *sql.Rows) error {
		var (
			az                 limes.AvailabilityZone
			usage              uint64
			activeCommitments  uint64
			pendingCommitments uint64
		)
		err := rows.Scan(&az, &usage, &activeCommitments, &pendingCommitments)
		if err != nil {
			return err
		}

		demand := result.PerAZ[az]
		demand.Usage += usage
		if activeCommitments > usage {
			demand.UnusedCommitments += activeCommitments - usage
		}
		demand.PendingCommitments += pendingCommitments
		result.PerAZ[az] = demand

		return nil
	})
	if err != nil {
		return liquid.ResourceDemand{}, fmt.Errorf("while getting resource demand for %s/%s through backchannel: %w", serviceType, resourceName, err)
	}
	return result, nil
}
