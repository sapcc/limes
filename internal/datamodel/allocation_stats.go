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

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"
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
// - CanMoveExistingCommitment
// - ConfirmPendingCommitments
type clusterAZAllocationStats struct {
	Capacity uint64
	// Using db.ProjectResourceID as a key here is only somewhat arbitrary:
	// ProjectServiceID and ProjectID could also be used, but then we would have to use more JOINs in some queries.
	ProjectStats map[db.ProjectResourceID]projectAZAllocationStats
}

func (c clusterAZAllocationStats) AllowsQuotaOvercommit(cfg core.AutogrowQuotaDistributionConfiguration) bool {
	if cfg.AllowQuotaOvercommitUntilAllocatedPercent == 0 {
		// optimization
		return false
	}

	usedCapacity := uint64(0)
	for _, stats := range c.ProjectStats {
		usedCapacity += max(stats.Committed, stats.Usage)
	}
	if c.Capacity == 0 {
		// explicit special case to avoid divide-by-zero below
		return usedCapacity == 0
	}

	usedPercent := 100 * float64(usedCapacity) / float64(c.Capacity)
	return usedPercent < cfg.AllowQuotaOvercommitUntilAllocatedPercent
}

func (c clusterAZAllocationStats) CanAcceptCommitmentChanges(additions, subtractions map[db.ProjectResourceID]uint64, ccr core.CommitmentCreationRule) bool {
	// calculate `sum_over_projects(max(committed, usage))` including the requested changes
	usedCapacity := uint64(0)
	for projectResourceID, stats := range c.ProjectStats {
		committed := saturatingSub(stats.Committed+additions[projectResourceID], subtractions[projectResourceID])
		usedCapacity += max(committed, stats.Usage)
	}

	// commitment can be confirmed if it and all other commitments and usage fit in the committable portion of the total capacity
	committableCapacity := c.Capacity
	if ccr.UntilPercent != nil {
		committableCapacity = uint64(float64(c.Capacity) * *ccr.UntilPercent / 100)
	}
	if usedCapacity <= committableCapacity {
		logg.Debug("CanAcceptCommitmentChanges: accepted")
		return true
	}

	// As an exception, even if capacity is exceeded:
	// - Commitments can always be reduced.
	// - Commitments can always be increased to cover existing usage.
	// This rule is designed to accommodate edits that do not change `usedCapacity` as computed above.
	for projectResourceID, stats := range c.ProjectStats {
		committed := saturatingSub(stats.Committed+additions[projectResourceID], subtractions[projectResourceID])

		if additions[projectResourceID] > 0 && committed > stats.Usage {
			logg.Debug("CanAcceptCommitmentChanges: forbidden by commitment target %d > usage %d in resourceID = %d",
				committed, stats.Usage, projectResourceID)
			return false
		}
	}

	logg.Debug("CanAcceptCommitmentChanges: accepted")
	return true
}

// Like `lhs - rhs`, but never underflows below 0.
func saturatingSub(lhs, rhs uint64) uint64 {
	if lhs < rhs {
		return 0
	}
	return lhs - rhs
}

// projectAZAllocationStats describes the resource allocation in a certain AZ
// resource by a single project.
type projectAZAllocationStats struct {
	Committed          uint64 // sum of confirmed commitments
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
		SELECT pr.id, par.az, par.usage, par.historical_usage, COALESCE(SUM(pc.amount), 0)
		  FROM project_services ps
		  JOIN project_resources pr ON pr.service_id = ps.id
		  JOIN project_az_resources par ON par.resource_id = pr.id
		  LEFT OUTER JOIN project_commitments pc ON pc.az_resource_id = par.id AND pc.state = 'active'
		 WHERE ps.type = $1 AND pr.name = $2 AND ($3::text IS NULL OR par.az = $3)
		 GROUP BY pr.id, par.az, par.usage, par.historical_usage
	`)
)

// Shared data collection phase for ApplyComputedProjectQuota,
// CanConfirmNewCommitment and ConfirmPendingCommitments.
func collectAZAllocationStats(serviceType db.ServiceType, resourceName liquid.ResourceName, azFilter *limes.AvailabilityZone, cluster *core.Cluster, dbi db.Interface) (map[limes.AvailabilityZone]clusterAZAllocationStats, error) {
	scopeDesc := fmt.Sprintf("%s/%s", serviceType, resourceName)
	if azFilter != nil {
		scopeDesc += fmt.Sprintf(" in %s", *azFilter)
	}
	result := make(map[limes.AvailabilityZone]clusterAZAllocationStats)

	// get capacity
	queryArgs := []any{serviceType, resourceName, azFilter}
	overcommitFactor := cluster.BehaviorForResource(serviceType, resourceName).OvercommitFactor
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

	// get resource usage
	err = sqlext.ForeachRow(dbi, getUsageInResourceQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			resourceID          db.ProjectResourceID
			az                  limes.AvailabilityZone
			stats               projectAZAllocationStats
			historicalUsageJSON string
		)
		err := rows.Scan(&resourceID, &az, &stats.Usage, &historicalUsageJSON, &stats.Committed)
		if err != nil {
			return err
		}
		ts, err := util.ParseTimeSeries[uint64](historicalUsageJSON)
		if err != nil {
			return fmt.Errorf("could not parse historical usage for project resource %d in %s: %w",
				resourceID, az, err)
		}
		stats.MinHistoricalUsage = ts.MinOr(stats.Usage)
		stats.MaxHistoricalUsage = ts.MaxOr(stats.Usage)

		azStats := result[az].ProjectStats
		if azStats == nil {
			azEntry := result[az]
			azEntry.ProjectStats = map[db.ProjectResourceID]projectAZAllocationStats{resourceID: stats}
			result[az] = azEntry
		} else {
			azStats[resourceID] = stats
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("while getting resource usage for %s: %w", scopeDesc, err)
	}

	return result, nil
}
