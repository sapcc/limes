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
	"github.com/sapcc/limes/internal/util"
)

// clusterAZAllocationStats bundles all data pertaining to a specific AZ
// resource that we need for various high-level algorithms in this package:
//
// - ApplyComputedProjectQuota
// - CanConfirmNewCommitment
// - ConfirmPendingCommitments
type clusterAZAllocationStats struct {
	Capacity     uint64
	ProjectStats map[db.ProjectServiceID]projectAZAllocationStats
}

func (c clusterAZAllocationStats) FitsAdditionalCommitment(serviceID db.ProjectServiceID, amount uint64) bool {
	// calculate `sum_over_projects(max(committed, usage))` including the requested commitment
	usedCapacity := uint64(0)
	for projectServiceID, stats := range c.ProjectStats {
		if projectServiceID == serviceID {
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
	Committed          uint64 //sum of confirmed commitments
	Usage              uint64
	MinHistoricalUsage uint64
	MaxHistoricalUsage uint64
}

var (
	getRawCapacityInResourceQuery = sqlext.SimplifyWhitespace(`
		SELECT car.az, car.raw_capacity
		  FROM cluster_services cs
		  JOIN cluster_resources cr ON cr.service_id = cs.id
		  JOIN cluster_az_resources car ON car.resource_id = cr.id
		  WHERE cs.type = $1 AND cr.name = $2 AND ($3::text IS NULL OR car.az = $3)
	`)

	getUsageInResourceQuery = sqlext.SimplifyWhitespace(`
		SELECT ps.id, par.az, par.usage, par.historical_usage,
		       (SELECT COALESCE(SUM(pc.amount), 0) FROM project_commitments pc
		         WHERE pc.az_resource_id = par.id AND pc.confirmed_at IS NOT NULL AND pc.superseded_at IS NULL AND pc.expires_at > $4)
		  FROM project_services ps
		  JOIN project_resources pr ON pr.service_id = ps.id
		  JOIN project_az_resources par ON par.resource_id = pr.id
		 WHERE ps.type = $1 AND pr.name = $2 AND ($3::text IS NULL OR par.az = $3)
	`)
)

// Shared data collection phase for ApplyComputedProjectQuota,
// CanConfirmNewCommitment and ConfirmPendingCommitments.
func collectAZAllocationStats(serviceType, resourceName string, azFilter *limes.AvailabilityZone, cluster *core.Cluster, dbi db.Interface, now time.Time) (map[limes.AvailabilityZone]clusterAZAllocationStats, error) {
	scopeDesc := fmt.Sprintf("%s/%s", serviceType, resourceName)
	if azFilter != nil {
		scopeDesc += fmt.Sprintf(" in %s", *azFilter)
	}
	result := make(map[limes.AvailabilityZone]clusterAZAllocationStats)

	//get capacity
	queryArgs := []any{serviceType, resourceName, azFilter}
	overcommitFactor := cluster.BehaviorForResource(serviceType, resourceName, "").OvercommitFactor
	err := sqlext.ForeachRow(dbi, getRawCapacityInResourceQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			az          limes.AvailabilityZone
			rawCapacity uint64
		)
		err := rows.Scan(&az, &rawCapacity)
		result[az] = clusterAZAllocationStats{
			Capacity: overcommitFactor.ApplyTo(rawCapacity),
		}
		return err
	})
	if err != nil {
		return result, fmt.Errorf("while getting raw capacity for %s: %w", scopeDesc, err)
	}

	//get resource usage
	queryArgs = append(queryArgs, now)
	err = sqlext.ForeachRow(dbi, getUsageInResourceQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			serviceID           db.ProjectServiceID
			az                  limes.AvailabilityZone
			stats               projectAZAllocationStats
			historicalUsageJSON string
		)
		err := rows.Scan(&serviceID, &az, &stats.Usage, &historicalUsageJSON, &stats.Committed)
		if err != nil {
			return err
		}
		ts, err := util.ParseTimeSeries[uint64](historicalUsageJSON)
		if err != nil {
			return fmt.Errorf("could not parse historical usage for project service %d in %s: %w",
				serviceID, az, err)
		}
		stats.MinHistoricalUsage = ts.MinOr(stats.Usage)
		stats.MaxHistoricalUsage = ts.MaxOr(stats.Usage)

		azStats := result[az].ProjectStats
		if azStats == nil {
			azEntry := result[az]
			azEntry.ProjectStats = map[db.ProjectServiceID]projectAZAllocationStats{serviceID: stats}
			result[az] = azEntry
		} else {
			azStats[serviceID] = stats
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("while getting resource usage for %s: %w", scopeDesc, err)
	}

	return result, nil
}
