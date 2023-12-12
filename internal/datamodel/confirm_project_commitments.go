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
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// CanConfirmNewCommitment returns whether the given commitment request can be
// confirmed immediately upon creation in the given project.
func CanConfirmNewCommitment(req limesresources.CommitmentRequest, project db.Project, cluster *core.Cluster, dbi db.Interface, now time.Time) (bool, error) {
	stats, err := collectAllocationStats(req.ServiceType, req.ResourceName, req.AvailabilityZone, cluster, dbi, now)
	if err != nil {
		return false, err
	}
	return stats.FitsAdditionalCommitment(project.ID, req.Amount), nil
}

// Describes how much committable capacity for a certain AZ resource is
// available, and how much is already allocated by projects.
type clusterAllocationStats struct {
	Capacity         uint64
	StatsByProjectID map[int64]projectAllocationStats
}

// Describes how much committable capacity for a certain AZ resource is already
// allocated by a single project.
type projectAllocationStats struct {
	Committed uint64
	Usage     uint64
}

func (c clusterAllocationStats) FitsAdditionalCommitment(targetProjectID int64, amount uint64) bool {
	// calculate `sum_over_projects(max(committed, usage))` including the requested commitment
	usedCapacity := uint64(0)
	for projectID, stats := range c.StatsByProjectID {
		if projectID == targetProjectID {
			usedCapacity += max(stats.Committed+amount, stats.Usage)
		} else {
			usedCapacity += max(stats.Committed, stats.Usage)
		}
	}

	//commitment can be confirmed if it and all other commitments and usage fit in the total capacity
	return usedCapacity <= c.Capacity
}

var (
	// We need to ensure that `sum_over_projects(max(committed, usage)) <= capacity`.
	// For the target project, `committed` includes both existing confirmed commitments
	// as well as the given commitment.
	getRawCapacityQuery = sqlext.SimplifyWhitespace(`
		SELECT car.raw_capacity
			FROM cluster_services cs
			JOIN cluster_resources cr ON cr.service_id = cs.id
			JOIN cluster_az_resources car ON car.resource_id = cr.id
		 WHERE cs.type = $1 AND cr.name = $2 AND car.az = $3
	`)
	getAllocationStatsQuery = sqlext.SimplifyWhitespace(`
		WITH committed AS (
			SELECT ps.project_id AS project_id, SUM(pc.amount) AS amount
			  FROM project_services ps
			  JOIN project_commitments pc ON pc.service_id = ps.id
			 WHERE ps.type = $1 AND pc.resource_name = $2 AND pc.availability_zone = $3
			   AND pc.confirmed_at IS NOT NULL AND pc.superseded_at IS NULL AND pc.expires_at > $4
			 GROUP BY ps.project_id
		), used AS (
			SELECT ps.project_id AS project_id, par.usage AS amount
			  FROM project_services ps
			  JOIN project_resources pr ON pr.service_id = ps.id
			  JOIN project_az_resources par ON par.resource_id = pr.id
			 WHERE ps.type = $1 AND pr.name = $2 AND par.az = $3
		)
		SELECT COALESCE(c.project_id, u.project_id), COALESCE(c.amount, 0), COALESCE(u.amount, 0)
		  FROM committed c FULL OUTER JOIN used u ON u.project_id = c.project_id
	`)
)

// Shared data collection phase for CanConfirmNewCommitment and ConfirmPendingCommitments.
func collectAllocationStats(serviceType, resourceName string, az limes.AvailabilityZone, cluster *core.Cluster, dbi db.Interface, now time.Time) (clusterAllocationStats, error) {
	result := clusterAllocationStats{
		StatsByProjectID: make(map[int64]projectAllocationStats),
	}

	//get raw capacity
	var rawCapacity uint64
	queryArgs := []any{serviceType, resourceName, az}
	err := dbi.QueryRow(getRawCapacityQuery, queryArgs...).Scan(&rawCapacity)
	if err != nil {
		return clusterAllocationStats{}, fmt.Errorf("while getting raw capacity for %s/%s in %s: %w", serviceType, resourceName, az, err)
	}

	//get nominal capacity
	clusterBehavior := cluster.BehaviorForResource(serviceType, resourceName, "")
	if clusterBehavior.OvercommitFactor == 0 {
		result.Capacity = rawCapacity
	} else {
		result.Capacity = clusterBehavior.OvercommitFactor.ApplyTo(rawCapacity)
	}

	//collect project allocation stats
	queryArgs = []any{serviceType, resourceName, az, now}
	err = sqlext.ForeachRow(dbi, getAllocationStatsQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			projectID int64
			stats     projectAllocationStats
		)
		err := rows.Scan(&projectID, &stats.Committed, &stats.Usage)
		result.StatsByProjectID[projectID] = stats
		return err
	})
	if err != nil {
		return clusterAllocationStats{}, fmt.Errorf("while getting allocation stats for %s/%s in %s: %w", serviceType, resourceName, az, err)
	}

	return result, nil
}
