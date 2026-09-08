// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"context"
	"fmt"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/sqlext"
	"go.xyrillian.de/oblast"

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
		SELECT azr.az, pazr.usage, COALESCE(pc_view.confirmed, 0) AS confirmed, COALESCE(pc_view.pending, 0) AS pending, r.topology
		  FROM services s
		  JOIN resources r ON r.service_id = s.id
		  JOIN az_resources azr ON azr.resource_id = r.id
		  JOIN project_az_resources pazr ON pazr.az_resource_id = azr.id
		  LEFT OUTER JOIN (
		    SELECT az_resource_id, project_id,
		           SUM(amount) FILTER (WHERE status = {{liquid.CommitmentStatusConfirmed}}) AS confirmed,
		           SUM(amount) FILTER (WHERE status = {{liquid.CommitmentStatusPending}}) AS pending
		      FROM project_commitments
		     GROUP BY az_resource_id, project_id
		  ) pc_view ON pc_view.az_resource_id = azr.id AND pc_view.project_id = pazr.project_id
		 WHERE s.type = $1 AND r.name = $2
		 -- no az filter, as az.isReal() is used later
	`))
)

type resourceDemandRecord struct {
	AZ                 limes.AvailabilityZone `db:"az"`
	Usage              uint64                 `db:"usage"`
	ActiveCommitments  uint64                 `db:"confirmed"`
	PendingCommitments uint64                 `db:"pending"`
	Topology           liquid.Topology        `db:"topology"`
}

// GetResourceDemand implements the CapacityScrapeBackchannel interface.
func (i capacityScrapeBackchannelImpl) GetResourceDemand(serviceType db.ServiceType, resourceName liquid.ResourceName) (liquid.ResourceDemand, error) {
	result := liquid.ResourceDemand{
		OvercommitFactor: i.Cluster.BehaviorForResource(serviceType, resourceName).OvercommitFactor,
		PerAZ:            make(map[limes.AvailabilityZone]liquid.ResourceDemandInAZ),
	}
	err := oblast.MustNewStore[resourceDemandRecord](oblast.PostgresDialect()).Select(context.TODO(), i.DB, getResourceDemandQuery, serviceType, resourceName).Foreach(func(r resourceDemandRecord) error {
		// ignore usage in pseudo-AZs (as an exception, topology "flat" has a single entry for AZ "any")
		switch r.Topology {
		case liquid.FlatTopology:
			if r.AZ != liquid.AvailabilityZoneAny {
				return nil
			}
		default:
			if !r.AZ.IsReal() {
				return nil
			}
		}

		demand := result.PerAZ[r.AZ]
		demand.Usage += r.Usage
		if r.ActiveCommitments > r.Usage {
			demand.UnusedCommitments += r.ActiveCommitments - r.Usage
		}
		demand.PendingCommitments += r.PendingCommitments
		result.PerAZ[r.AZ] = demand

		return nil
	})
	if err != nil {
		return liquid.ResourceDemand{}, fmt.Errorf("while getting resource demand for %s/%s through backchannel: %w", serviceType, resourceName, err)
	}
	return result, nil
}
