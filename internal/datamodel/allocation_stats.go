/******************************************************************************
*
*  Copyright 2023 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package datamodel

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// clusterAZAllocationStats bundles all data pertaining to a specific AZ
// resource that we need for various high-level algorithms in this package:
//
// - CanConfirmNewCommitment
// - ConfirmPendingCommitments
type clusterAZAllocationStats struct {
	Capacity                uint64
	StatsByProjectServiceID map[int64]projectAZAllocationStats
}

func (c clusterAZAllocationStats) FitsAdditionalCommitment(targetProjectServiceID int64, amount uint64) bool {
	// calculate `sum_over_projects(max(committed, usage))` including the requested commitment
	usedCapacity := uint64(0)
	for projectServiceID, stats := range c.StatsByProjectServiceID {
		if projectServiceID == targetProjectServiceID {
			usedCapacity += max(stats.Committed+amount, stats.Usage)
		} else {
			usedCapacity += max(stats.Committed, stats.Usage)
		}
	}

	//commitment can be confirmed if it and all other commitments and usage fit in the total capacity
	return usedCapacity <= c.Capacity
}

// projectAZAllocationStats describes the resource allocation in a certain AZ
// resource by a single project.
type projectAZAllocationStats struct {
	Committed uint64 //sum of confirmed commitments
	Usage     uint64
}

var (
	//queries for a specific AZ
	getRawCapacityInAZResourceQuery = sqlext.SimplifyWhitespace(`
		SELECT car.raw_capacity
		  FROM cluster_services cs
		  JOIN cluster_resources cr ON cr.service_id = cs.id
		  JOIN cluster_az_resources car ON car.resource_id = cr.id
		 WHERE cs.type = $1 AND cr.name = $2 AND car.az = $3
	`)
	getUsageInAZResourceQuery = sqlext.SimplifyWhitespace(`
		SELECT ps.id, par.usage
		  FROM project_services ps
		  JOIN project_resources pr ON pr.service_id = ps.id
		  JOIN project_az_resources par ON par.resource_id = pr.id
		 WHERE ps.type = $1 AND pr.name = $2 AND par.az = $3
	`)
	getCommittedInAZResourceQuery = sqlext.SimplifyWhitespace(`
		SELECT ps.id, SUM(pc.amount)
		  FROM project_services ps
		  JOIN project_commitments pc ON pc.service_id = ps.id
		 WHERE ps.type = $1 AND pc.resource_name = $2 AND pc.availability_zone = $3
		   AND pc.confirmed_at IS NOT NULL AND pc.superseded_at IS NULL AND pc.expires_at > $4
		 GROUP BY ps.id
	`)
)

// Shared data collection phase for CanConfirmNewCommitment and ConfirmPendingCommitments.
func collectAZAllocationStats(serviceType, resourceName string, az limes.AvailabilityZone, cluster *core.Cluster, dbi db.Interface, now time.Time) (clusterAZAllocationStats, error) {
	result := clusterAZAllocationStats{
		StatsByProjectServiceID: make(map[int64]projectAZAllocationStats),
	}

	//get raw capacity
	var rawCapacity uint64
	queryArgs := []any{serviceType, resourceName, az}
	err := dbi.QueryRow(getRawCapacityInAZResourceQuery, queryArgs...).Scan(&rawCapacity)
	if err != nil {
		return clusterAZAllocationStats{}, fmt.Errorf("while getting raw capacity for %s/%s in %s: %w", serviceType, resourceName, az, err)
	}

	//get nominal capacity
	overcommitFactor := cluster.BehaviorForResource(serviceType, resourceName, "").OvercommitFactor
	result.Capacity = overcommitFactor.ApplyTo(rawCapacity)

	//get resource usage
	err = sqlext.ForeachRow(dbi, getUsageInAZResourceQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			projectServiceID int64
			stats            projectAZAllocationStats
		)
		err := rows.Scan(&projectServiceID, &stats.Usage)
		result.StatsByProjectServiceID[projectServiceID] = stats
		return err
	})
	if err != nil {
		return clusterAZAllocationStats{}, fmt.Errorf("while getting resource usage for %s/%s in %s: %w", serviceType, resourceName, az, err)
	}

	//get commitment usage
	queryArgs = []any{serviceType, resourceName, az, now}
	err = sqlext.ForeachRow(dbi, getCommittedInAZResourceQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			projectServiceID int64
			committed        uint64
		)
		err := rows.Scan(&projectServiceID, &committed)
		stats := result.StatsByProjectServiceID[projectServiceID]
		stats.Committed = committed
		result.StatsByProjectServiceID[projectServiceID] = stats
		return err
	})
	if err != nil {
		return clusterAZAllocationStats{}, fmt.Errorf("while getting commitment usage for %s/%s in %s: %w", serviceType, resourceName, az, err)
	}

	return result, nil
}
