// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

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

	// Whether last_nonzero_raw_capacity is not NULL.
	ObservedNonzeroCapacityBefore bool

	ProjectStats map[db.ProjectID]projectAZAllocationStats
}

// Returns two separate opinions:
//   - Whether growth quota overcommit is allowed in this AZ, and
//   - whether this AZ is fine with allowing base quota overcommit in the `any` AZ.
func (c clusterAZAllocationStats) allowsQuotaOvercommit(cfg core.AutogrowQuotaDistributionConfiguration) (allowsGrowth, allowsBase bool) {
	usedCapacity := uint64(0)
	for _, stats := range c.ProjectStats {
		usedCapacity += max(stats.Committed, stats.Usage)
	}

	if c.Capacity == 0 {
		// If we have no capacity, we will definitely forbid growth quota overcommit in this AZ.
		// But we do not block base quota overcommit in some specific scenarios:
		// - when the AZ never had any capacity (either because this resource is just not available here, or because it is still in buildup)
		// - when there is no usage either (e.g. during decommissioning)
		return false, !c.ObservedNonzeroCapacityBefore || usedCapacity == 0
	} else {
		// If there is a reliable capacity measurement, we can voice a strong opinion.
		usedPercent := 100 * float64(usedCapacity) / float64(c.Capacity)
		result := usedPercent < cfg.AllowQuotaOvercommitUntilAllocatedPercent
		return result, result
	}
}

func (c clusterAZAllocationStats) CanAcceptCommitmentChanges(additions, subtractions map[db.ProjectID]uint64, behavior core.CommitmentBehavior) bool {
	// calculate `sum_over_projects(max(committed, usage))` before and after the requested changes
	var (
		usedCapacityBefore = uint64(0)
		usedCapacityAfter  = uint64(0)
	)
	for projectID, stats := range c.ProjectStats {
		usedCapacityBefore += max(stats.Committed, stats.Usage)
		committedAfter := saturatingSub(stats.Committed+additions[projectID], subtractions[projectID])
		usedCapacityAfter += max(committedAfter, stats.Usage)
	}

	// all changes that do not increase `usedCapacity` are safe to allow
	if usedCapacityAfter <= usedCapacityBefore {
		logg.Debug("CanAcceptCommitmentChanges: accepted because usedCapacity does not increase (%d -> %d)",
			usedCapacityBefore, usedCapacityAfter)
		return true
	}

	// commitment increases can be confirmed if all commitments and usage fit in the committable portion of the total capacity
	committableCapacity := c.Capacity
	if thresholdPercent, ok := behavior.UntilPercent.Unpack(); ok {
		committableCapacity = uint64(float64(c.Capacity) * thresholdPercent / 100)
	}
	if usedCapacityAfter <= committableCapacity {
		logg.Debug("CanAcceptCommitmentChanges: accepted because usedCapacity increases within committableCapacity (%d -> %d <= %d)",
			usedCapacityBefore, usedCapacityAfter, committableCapacity)
		return true
	}

	logg.Debug("CanAcceptCommitmentChanges: rejected because usedCapacity grows to exceed committableCapacity (%d -> %d > %d)",
		usedCapacityBefore, usedCapacityAfter, committableCapacity)
	return false
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
		SELECT car.az, car.raw_capacity, car.last_nonzero_raw_capacity IS NOT NULL
		  FROM services cs
		  JOIN resources cr ON cr.service_id = cs.id
		  JOIN az_resources car ON car.resource_id = cr.id
		  WHERE cs.type = $1 AND cr.name = $2 AND ($3::text IS NULL OR car.az = $3)
	`)

	getUsageInResourceQuery = sqlext.SimplifyWhitespace(db.FillEnumValues(`
		SELECT pazr.project_id, cazr.az, pazr.usage, pazr.historical_usage, COALESCE(SUM(pc.amount), 0)
		  FROM services cs
		  JOIN resources cr ON cr.service_id = cs.id
		  JOIN az_resources cazr ON cazr.resource_id = cr.id
		  JOIN project_az_resources pazr ON pazr.az_resource_id = cazr.id
		  LEFT OUTER JOIN project_commitments pc ON pc.az_resource_id = cazr.id AND pc.project_id = pazr.project_id AND pc.status = {{liquid.CommitmentStatusConfirmed}}
		 WHERE cs.type = $1 AND cr.name = $2 AND ($3::text IS NULL OR cazr.az = $3)
		 GROUP BY pazr.project_id, cazr.az, pazr.usage, pazr.historical_usage
	`))
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
			az                            limes.AvailabilityZone
			rawCapacity                   uint64
			observedNonzeroCapacityBefore bool
		)
		err := rows.Scan(&az, &rawCapacity, &observedNonzeroCapacityBefore)
		result[az] = clusterAZAllocationStats{
			Capacity:                      overcommitFactor.ApplyTo(rawCapacity),
			ObservedNonzeroCapacityBefore: observedNonzeroCapacityBefore,
		}
		return err
	})
	if err != nil {
		return result, fmt.Errorf("while getting raw capacity for %s: %w", scopeDesc, err)
	}

	// get resource usage
	err = sqlext.ForeachRow(dbi, getUsageInResourceQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			projectID           db.ProjectID
			az                  limes.AvailabilityZone
			stats               projectAZAllocationStats
			historicalUsageJSON string
		)
		err := rows.Scan(&projectID, &az, &stats.Usage, &historicalUsageJSON, &stats.Committed)
		if err != nil {
			return err
		}
		ts, err := util.ParseTimeSeries[uint64](historicalUsageJSON)
		if err != nil {
			return fmt.Errorf("could not parse historical usage of %s/%s for project %d in %s: %w",
				serviceType, resourceName, projectID, az, err)
		}
		stats.MinHistoricalUsage = ts.MinOr(stats.Usage)
		stats.MaxHistoricalUsage = ts.MaxOr(stats.Usage)

		azStats := result[az].ProjectStats
		if azStats == nil {
			azEntry := result[az]
			azEntry.ProjectStats = map[db.ProjectID]projectAZAllocationStats{projectID: stats}
			result[az] = azEntry
		} else {
			azStats[projectID] = stats
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("while getting resource usage for %s: %w", scopeDesc, err)
	}

	return result, nil
}
