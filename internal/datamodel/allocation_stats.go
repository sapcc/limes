// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"context"
	"fmt"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"
	"go.xyrillian.de/oblast"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"

	. "go.xyrillian.de/gg/option"
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

// CanAcceptCommitmentChanges determines whether the given commitment additions
// and subtractions can be accepted in this resources AZ capacity, which already
// considers the overcommit factor for the resource.
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

var getUsageInResourceQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		SELECT pazr.project_id, azr.az, pazr.usage, pazr.historical_usage, COALESCE(SUM(pc.amount), 0) AS committed
		  FROM services s
		  JOIN resources r ON r.service_id = s.id
		  JOIN az_resources azr ON azr.resource_id = r.id
		  JOIN project_az_resources pazr ON pazr.az_resource_id = azr.id
		  LEFT OUTER JOIN project_commitments pc ON pc.az_resource_id = azr.id AND pc.project_id = pazr.project_id AND pc.status = {{liquid.CommitmentStatusConfirmed}}
		 WHERE s.type = $1 AND r.name = $2 AND ($3::text IS NULL OR azr.az = $3) AND azr.az != {{liquid.AvailabilityZoneTotal}}
		 GROUP BY pazr.project_id, azr.az, pazr.usage, pazr.historical_usage
	`))

// Shared data collection phase for ApplyComputedProjectQuota,
// CanConfirmNewCommitment and ConfirmPendingCommitments.
func collectAZAllocationStats(ctx context.Context, sis core.ServiceInfoSnapshot, resourcePath db.ResourcePath, azFilter Option[limes.AvailabilityZone], cluster *core.Cluster, dbi db.Interface) (map[limes.AvailabilityZone]clusterAZAllocationStats, error) {
	scopeDesc := resourcePath.String()
	if azFilter.IsSome() {
		scopeDesc += fmt.Sprintf(" in %s", azFilter)
	}
	result := make(map[limes.AvailabilityZone]clusterAZAllocationStats)

	// get capacity
	overcommitFactor := cluster.BehaviorForResourcePath(resourcePath).OvercommitFactor
	for azRes := range sis.GetAZResourcesForPath(resourcePath).Values() {
		if az, exists := azFilter.Unpack(); exists && azRes.AvailabilityZone != az {
			continue
		}
		result[azRes.AvailabilityZone] = clusterAZAllocationStats{
			Capacity:                      overcommitFactor.ApplyTo(azRes.RawCapacity),
			ObservedNonzeroCapacityBefore: azRes.LastNonzeroRawCapacity.IsSome(),
		}
	}

	// get resource usage
	type usageInResourceRecord struct {
		ProjectID       db.ProjectID           `db:"project_id"`
		AZ              limes.AvailabilityZone `db:"az"`
		Usage           uint64                 `db:"usage"`
		HistoricalUsage string                 `db:"historical_usage"`
		Committed       uint64                 `db:"committed"`
	}
	queryArgs := []any{resourcePath.ServiceType, resourcePath.ResourceName, azFilter}
	err := oblast.MustNewStore[usageInResourceRecord](oblast.PostgresDialect()).Select(ctx, dbi, getUsageInResourceQuery, queryArgs...).Foreach(func(r usageInResourceRecord) error {
		ts, err := util.ParseTimeSeries[uint64](r.HistoricalUsage)
		if err != nil {
			return fmt.Errorf("could not parse historical usage of %s for project %d in %s: %w",
				resourcePath.String(), r.ProjectID, r.AZ, err)
		}
		stats := projectAZAllocationStats{
			Committed:          r.Committed,
			Usage:              r.Usage,
			MinHistoricalUsage: ts.MinOr(r.Usage),
			MaxHistoricalUsage: ts.MaxOr(r.Usage),
		}

		azStats := result[r.AZ].ProjectStats
		if azStats == nil {
			azEntry := result[r.AZ]
			azEntry.ProjectStats = map[db.ProjectID]projectAZAllocationStats{r.ProjectID: stats}
			result[r.AZ] = azEntry
		} else {
			azStats[r.ProjectID] = stats
		}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("while getting resource usage for %s: %w", scopeDesc, err)
	}

	return result, nil
}
