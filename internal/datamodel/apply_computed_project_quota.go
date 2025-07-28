// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"time"

	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// NOTE: Most private functions in this file have the name prefix "acpq"
// since they represent individual steps of ApplyComputedProjectQuota.

var (
	acpqGetClusterResourceIDQuery = sqlext.SimplifyWhitespace(`
		SELECT cr.id
		  FROM cluster_services cs
		  JOIN cluster_resources cr ON cr.service_id = cs.id
		 WHERE cs.type = $1 AND cr.name = $2
	`)

	acpqGetLocalQuotaConstraintsQuery = sqlext.SimplifyWhitespace(`
		SELECT project_id, forbidden, max_quota_from_outside_admin, max_quota_from_local_admin, override_quota_from_config
		  FROM project_resources_v2
		 WHERE resource_id = $1 AND (forbidden IS NOT NULL
		                          OR max_quota_from_outside_admin IS NOT NULL
		                          OR max_quota_from_local_admin IS NOT NULL
		                          OR override_quota_from_config IS NOT NULL)
	`)

	// This does not need to create any entries in project_az_resources, because
	// Scrape() already created them for us.
	acpqUpdateAZQuotaQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_az_resources_v2 pazr
		SET quota = $1
		FROM cluster_az_resources cazr
		WHERE pazr.az_resource_id = cazr.id AND cazr.az = $2 AND pazr.project_id = $3 AND cazr.resource_id = $4
		AND pazr.quota IS DISTINCT FROM $1
	`)
	acpqUpdateProjectQuotaQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_resources_v2 SET quota = $1 WHERE quota IS DISTINCT FROM $1 AND project_id = $2 AND resource_id = $3
	`)
	acpqUpdateProjectServicesQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_services_v2 ps
		SET quota_desynced_at = $1
		FROM cluster_services cs
		WHERE ps.service_id = cs.id AND ps.project_id = $2 AND cs.type = $3
		AND quota_desynced_at IS NULL
	`)
)

type projectLocalQuotaConstraints struct {
	MinQuota Option[uint64]
	MaxQuota Option[uint64]
}

func (c *projectLocalQuotaConstraints) AddMinQuota(value Option[uint64]) {
	rhs, ok := value.Unpack()
	if !ok {
		return
	}
	lhs, ok := c.MinQuota.Unpack()
	if ok {
		c.MinQuota = Some(max(lhs, rhs))
	} else {
		c.MinQuota = Some(rhs)
	}
}

func (c *projectLocalQuotaConstraints) AddMaxQuota(value Option[uint64]) {
	rhs, ok := value.Unpack()
	if !ok {
		return
	}
	lhs, ok := c.MaxQuota.Unpack()
	if ok {
		c.MaxQuota = Some(min(lhs, rhs))
	} else {
		c.MaxQuota = Some(rhs)
	}
}

// ApplyComputedProjectQuota reevaluates auto-computed project quotas for the
// given resource, if supported by its quota distribution model.
func ApplyComputedProjectQuota(serviceType db.ServiceType, resourceName liquid.ResourceName, resourceInfo liquid.ResourceInfo, cluster *core.Cluster, now time.Time) error {
	// only run for resources with quota and autogrow QD model
	if !resourceInfo.HasQuota {
		return nil
	}
	cfg, ok := cluster.QuotaDistributionConfigForResource(serviceType, resourceName).Autogrow.Unpack()
	if !ok {
		return nil
	}

	// run the quota computation in a transaction (this must be done inside this
	// function because we want to commit this wide-reaching transaction before
	// starting to talk to backend services for ApplyBackendQuota)
	tx, err := cluster.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// to avoid excessive joins, convert serviceType+resourceName into a ClusterResourceID only once
	var clusterResourceID db.ClusterResourceID
	err = tx.QueryRow(acpqGetClusterResourceIDQuery, serviceType, resourceName).Scan(&clusterResourceID)
	if err != nil {
		return fmt.Errorf("could not find cluster_resources.id: %w", err)
	}

	// collect required data (TODO: pass `clusterResourceID` into here to simplify queries over there too?)
	stats, err := collectAZAllocationStats(serviceType, resourceName, nil, cluster, tx)
	if err != nil {
		return err
	}

	constraints := make(map[db.ProjectID]projectLocalQuotaConstraints)
	err = sqlext.ForeachRow(tx, acpqGetLocalQuotaConstraintsQuery, []any{clusterResourceID}, func(rows *sql.Rows) error {
		var (
			projectID                db.ProjectID
			forbidden                bool
			maxQuotaFromOutsideAdmin Option[uint64]
			maxQuotaFromLocalAdmin   Option[uint64]
			overrideQuotaFromConfig  Option[uint64]
		)
		err := rows.Scan(&projectID, &forbidden, &maxQuotaFromOutsideAdmin, &maxQuotaFromLocalAdmin, &overrideQuotaFromConfig)
		if err != nil {
			return err
		}

		var c projectLocalQuotaConstraints
		if forbidden {
			c.AddMaxQuota(Some(uint64(0)))
		}
		c.AddMaxQuota(maxQuotaFromOutsideAdmin)
		c.AddMaxQuota(maxQuotaFromLocalAdmin)
		c.AddMinQuota(overrideQuotaFromConfig)
		c.AddMaxQuota(overrideQuotaFromConfig)

		constraints[projectID] = c
		return nil
	})
	if err != nil {
		return err
	}

	// evaluate QD algorithm
	// AZ separated basequota will be assigned to all available AZs
	if logg.ShowDebug {
		// NOTE: The structs that contain pointers must be printed as JSON to actually show all values.
		logg.Debug("ACPQ for %s/%s: stats = %#v", serviceType, resourceName, stats)
		logg.Debug("ACPQ for %s/%s: cfg = %#v", serviceType, resourceName, cfg)
		buf, _ := json.Marshal(constraints) //nolint:errcheck
		logg.Debug("ACPQ for %s/%s: constraints = %s", serviceType, resourceName, string(buf))
	}
	target, allowsQuotaOvercommit := acpqComputeQuotas(stats, cfg, constraints, resourceInfo)
	if logg.ShowDebug {
		logg.Debug("ACPQ for %s/%s: allowsQuotaOvercommit = %#v", serviceType, resourceName, allowsQuotaOvercommit)
		buf, _ := json.Marshal(target) //nolint:errcheck
		logg.Debug("ACPQ for %s/%s: target = %s", serviceType, resourceName, string(buf))
	}

	// write new AZ quotas to database
	projectsWithUpdatedQuota := make(map[db.ProjectID]struct{})
	err = sqlext.WithPreparedStatement(tx, acpqUpdateAZQuotaQuery, func(stmt *sql.Stmt) error {
		for az, azTarget := range target {
			for projectID, projectTarget := range azTarget {
				result, err := stmt.Exec(projectTarget.Allocated, az, projectID, clusterResourceID)
				if err != nil {
					return fmt.Errorf("in AZ %s in project %d: %w", az, projectID, err)
				}
				rowsAffected, err := result.RowsAffected()
				if err != nil {
					return fmt.Errorf("in AZ %s in project %d: %w", az, projectID, err)
				}

				// AZSeparatedTopology does not update resource quota. Therefore the desync needs to be queued right here.
				if rowsAffected > 0 && resourceInfo.Topology == liquid.AZSeparatedTopology {
					projectsWithUpdatedQuota[projectID] = struct{}{}
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("while writing updated %s/%s AZ quotas to DB: %w", serviceType, resourceName, err)
	}

	// write overall project quotas to database
	quotasByProjectID := make(map[db.ProjectID]uint64)
	for _, azTarget := range target {
		for projectID, projectTarget := range azTarget {
			quotasByProjectID[projectID] += projectTarget.Allocated
		}
	}

	err = sqlext.WithPreparedStatement(tx, acpqUpdateProjectQuotaQuery, func(stmt *sql.Stmt) error {
		for projectID, quota := range quotasByProjectID {
			// Resources with AZSeparatedTopology will report `backendQuota == nil` during scrape.
			// If we set anything other than nil here, this would lead to unnecessary quota syncs with the backend,
			// because backendQuota != quota.
			quotaToWrite := &quota
			if resourceInfo.Topology == liquid.AZSeparatedTopology {
				quotaToWrite = nil
			}

			result, err := stmt.Exec(quotaToWrite, projectID, clusterResourceID)
			if err != nil {
				return fmt.Errorf("in project %d: %w", projectID, err)
			}
			rowsAffected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("in project %d: %w", projectID, err)
			}
			if rowsAffected > 0 {
				projectsWithUpdatedQuota[projectID] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("while writing updated %s/%s project quotas to DB: %w", serviceType, resourceName, err)
	}

	// mark project services with changed quota for SyncQuotaToBackendJob
	err = sqlext.WithPreparedStatement(tx, acpqUpdateProjectServicesQuery, func(stmt *sql.Stmt) error {
		for projectID := range projectsWithUpdatedQuota {
			_, err := stmt.Exec(now, projectID, serviceType)
			if err != nil {
				return fmt.Errorf("in project %d: %w", projectID, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("while marking updated %s/%s project quotas for sync in DB: %w", serviceType, resourceName, err)
	}
	return tx.Commit()
}

// Calculation space for a single project AZ resource.
type acpqProjectAZTarget struct {
	Allocated uint64
	Desired   uint64
}

// MarshalJSON implements the json.Marshaler interface.
// This is only used in tests to make error output more compact.
func (t acpqProjectAZTarget) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.Allocated)
}

// Requested returns how much is desired, but not allocated yet.
func (t acpqProjectAZTarget) Requested() uint64 {
	return liquidapi.SaturatingSub(t.Desired, t.Allocated)
}

// Calculation space for all project AZ resources in a single AZ.
type acpqAZTarget map[db.ProjectID]*acpqProjectAZTarget

// SumAllocated returns how much is allocated across all projects.
func (t acpqAZTarget) SumAllocated() (result uint64) {
	for _, pt := range t {
		result += pt.Allocated
	}
	return
}

// Requested returns how much is desired, but not allocated yet, for each project.
func (t acpqAZTarget) Requested() map[db.ProjectID]uint64 {
	result := make(map[db.ProjectID]uint64, len(t))
	for projectID, pt := range t {
		result[projectID] = pt.Requested()
	}
	return result
}

// AddGranted extends the allocations in this map by the granted values.
func (t acpqAZTarget) AddGranted(granted map[db.ProjectID]uint64) {
	for projectID, pt := range t {
		pt.Allocated += granted[projectID]
	}
}

// Calculation space for the entire quota algorithm.
type acpqGlobalTarget map[limes.AvailabilityZone]acpqAZTarget

// This function comprises the entirety of the actual quota distribution algorithm.
// It is called by ApplyComputedProjectQuota() which contains all the side
// effects (reading the DB, writing the DB, setting quota in the backend).
// This function is separate because most test cases work on this level.
// The full ApplyComputedProjectQuota() function is tested during capacity scraping.
func acpqComputeQuotas(stats map[limes.AvailabilityZone]clusterAZAllocationStats, cfg core.AutogrowQuotaDistributionConfiguration, constraints map[db.ProjectID]projectLocalQuotaConstraints, resInfo liquid.ResourceInfo) (target acpqGlobalTarget, allowsQuotaOvercommit map[limes.AvailabilityZone]bool) {
	// enumerate which project IDs and AZs are relevant
	// ("Relevant" AZs are all that have allocation stats available.)
	isProjectID := make(map[db.ProjectID]struct{})
	isRelevantAZ := make(map[limes.AvailabilityZone]struct{}, len(stats))
	var allAZsInOrder []limes.AvailabilityZone
	for az, azStats := range stats {
		isRelevantAZ[az] = struct{}{}
		allAZsInOrder = append(allAZsInOrder, az)
		for projectID := range azStats.ProjectStats {
			isProjectID[projectID] = struct{}{}
		}
	}
	slices.Sort(allAZsInOrder)
	if cfg.ProjectBaseQuota > 0 && resInfo.Topology != liquid.AZSeparatedTopology {
		// base quota is given out in the pseudo-AZ "any", so we need to calculate quota for "any", too
		isRelevantAZ[limes.AvailabilityZoneAny] = struct{}{}
	}

	// enumerate which AZs allow quota overcommit
	allowsQuotaOvercommit = make(map[limes.AvailabilityZone]bool)
	isAZAware := false
	allowsQuotaOvercommitInAny := true
	for az := range isRelevantAZ {
		allowsGrowthQuotaOvercommit, allowsBaseQuotaOvercommit := stats[az].allowsQuotaOvercommit(cfg)
		allowsQuotaOvercommit[az] = allowsGrowthQuotaOvercommit
		if az != limes.AvailabilityZoneAny && az != limes.AvailabilityZoneUnknown {
			isAZAware = true
			if !allowsBaseQuotaOvercommit {
				allowsQuotaOvercommitInAny = false
			}
		}
	}

	// in AZ-aware resources, quota for the pseudo-AZ "any" is backed by capacity
	// in all the real AZs, so it can only allow quota overcommit if all AZs do
	if isAZAware && resInfo.Topology != liquid.AZSeparatedTopology {
		allowsQuotaOvercommit[limes.AvailabilityZoneAny] = allowsQuotaOvercommitInAny
	}

	// initialize data structure where new quota will be computed (this uses eager
	// allocation of all required entries to simplify the following steps)
	//
	// Because we're looping through everything anyway, we're also doing the first
	// few steps of the algorithm itself that use the same looping pattern.
	target = make(acpqGlobalTarget, len(stats))
	for az := range isRelevantAZ {
		target[az] = make(acpqAZTarget, len(isProjectID))
		for projectID := range isProjectID {
			projectAZStats := stats[az].ProjectStats[projectID]
			target[az][projectID] = &acpqProjectAZTarget{
				// phase 1: always grant hard minimum quota
				Allocated: max(projectAZStats.Committed, projectAZStats.Usage),
				// phase 2: try granting soft minimum quota
				Desired: projectAZStats.MaxHistoricalUsage,
			}
		}
	}
	target.EnforceConstraints(stats, constraints, allAZsInOrder, isProjectID, isAZAware)
	target.TryFulfillDesired(stats, cfg, allowsQuotaOvercommit)

	// phase 3: try granting desired_quota
	for az := range isRelevantAZ {
		for projectID := range isProjectID {
			projectAZStats := stats[az].ProjectStats[projectID]
			growthBaseline := max(projectAZStats.Committed, projectAZStats.MinHistoricalUsage)
			desiredQuota := uint64(float64(growthBaseline) * cfg.GrowthMultiplier)
			if cfg.GrowthMultiplier > 1.0 && growthBaseline > 0 {
				// fix nonzero growth factor rounding to zero
				// e.g. growthBaseline = 5 and GrowthMultiplier = 1.1 -> desiredQuota = uint64(5.0 * 1.1) = 5
				growthMinimum := max(cfg.GrowthMinimum, 1)
				desiredQuota = max(desiredQuota, growthBaseline+growthMinimum)
			}
			target[az][projectID].Desired = desiredQuota
		}
	}
	target.EnforceConstraints(stats, constraints, allAZsInOrder, isProjectID, isAZAware)
	target.TryFulfillDesired(stats, cfg, allowsQuotaOvercommit)

	// phase 4: try granting additional "any" quota until sum of all quotas is ProjectBaseQuota
	if cfg.ProjectBaseQuota > 0 {
		for projectID := range isProjectID {
			sumOfLocalizedQuotas := uint64(0)
			for az := range isRelevantAZ {
				if az != limes.AvailabilityZoneAny {
					sumOfLocalizedQuotas += target[az][projectID].Allocated
				}
			}
			if sumOfLocalizedQuotas < cfg.ProjectBaseQuota {
				// AZ separated topology receives the basequota to all available AZs
				if resInfo.Topology == liquid.AZSeparatedTopology {
					for az := range isRelevantAZ {
						target[az][projectID].Desired = cfg.ProjectBaseQuota
					}
				} else {
					target[limes.AvailabilityZoneAny][projectID].Desired = cfg.ProjectBaseQuota - sumOfLocalizedQuotas
				}
			}
		}
		if resInfo.Topology != liquid.AZSeparatedTopology && !slices.Contains(allAZsInOrder, limes.AvailabilityZoneAny) {
			allAZsInOrder = append(allAZsInOrder, limes.AvailabilityZoneAny)
		}
		target.EnforceConstraints(stats, constraints, allAZsInOrder, isProjectID, isAZAware)
		target.TryFulfillDesired(stats, cfg, allowsQuotaOvercommit)
	}

	return target, allowsQuotaOvercommit
}

// After increasing Desired, but before increasing Allocated, this decreases
// Desired in order to fit into project-local quota constraints.
func (target acpqGlobalTarget) EnforceConstraints(stats map[limes.AvailabilityZone]clusterAZAllocationStats, constraints map[db.ProjectID]projectLocalQuotaConstraints, allAZs []limes.AvailabilityZone, isProjectID map[db.ProjectID]struct{}, isAZAware bool) {
	// Quota should not be assgined to ANY AZ on AZ aware resources. This causes unusable quota distribution on manual quota overrides.
	resourceAZs := allAZs
	if isAZAware {
		resourceAZs = slices.Clone(allAZs)
		resourceAZs = slices.DeleteFunc(resourceAZs, func(az limes.AvailabilityZone) bool { return az == limes.AvailabilityZoneAny })
	}
	for projectID, c := range constraints {
		// raise Allocated as necessary to fulfil minimum quota
		if minQuota, ok := c.MinQuota.Unpack(); ok && minQuota > 0 {
			// phase 1: distribute quota proportionally to desire in AZs that have capacity
			// if there is sufficient capacity in each AZ, all quota required additionally will be assigned in this phase
			totalAllocated := uint64(0)
			totalCapacity := uint64(0)
			totalDesire := uint64(0)
			for _, az := range resourceAZs {
				t := target[az][projectID]
				totalAllocated += t.Allocated
				totalCapacity += stats[az].Capacity
				totalDesire += t.Requested()
			}
			desireScalePerAZ := make(map[limes.AvailabilityZone]uint64)
			for _, az := range resourceAZs {
				if stats[az].Capacity > 0 {
					if totalDesire > 0 {
						// Desire is normalized to avoid uint overflows when dealing with large desire values
						desireProportion := float64(target[az][projectID].Requested()) / float64(totalDesire)
						desireScalePerAZ[az] = uint64(math.Ceil(float64(minQuota) * desireProportion))
					} else {
						desireScalePerAZ[az] = minQuota
					}
				}
			}
			missingQuota := liquidapi.SaturatingSub(minQuota, totalAllocated)
			extraAllocatedPerAZ := liquidapi.DistributeFairly(missingQuota, desireScalePerAZ)
			for _, az := range resourceAZs {
				extraAllocated := min(extraAllocatedPerAZ[az], stats[az].Capacity)
				target[az][projectID].Allocated += extraAllocated
				missingQuota -= extraAllocated
			}

			// phase 2: if not enough quota could be assigned due to capacity constraints,
			// iniate second distribution round with distribution proportionally to the available capacity.
			// Since min quota should be enforced, more quota than available capacity may be distributed
			if missingQuota > 0 {
				capacityScalePerAZ := make(map[limes.AvailabilityZone]uint64)
				for _, az := range resourceAZs {
					// Capacity is normalized to avoid uint overflows when dealing with large capacities
					capacityProportion := (float64(stats[az].Capacity) / float64(totalCapacity))
					capacityScalePerAZ[az] = uint64(math.Ceil(float64(minQuota) * capacityProportion))
				}
				extraAllocatedPerAZ := liquidapi.DistributeFairly(missingQuota, capacityScalePerAZ)
				for _, az := range resourceAZs {
					target[az][projectID].Allocated += extraAllocatedPerAZ[az]
				}
			}
		}

		// lower Desired as necessary to fulfil maximum quota
		if maxQuota, ok := c.MaxQuota.Unpack(); ok {
			totalAllocated := uint64(0)
			totalDesired := uint64(0)
			extraDesiredPerAZ := make(map[limes.AvailabilityZone]uint64)
			for _, az := range allAZs {
				t := target[az][projectID]
				totalAllocated += t.Allocated
				totalDesired += max(t.Allocated, t.Desired)
				extraDesiredPerAZ[az] = t.Requested()
			}
			if totalDesired > maxQuota {
				extraDesiredPerAZ = liquidapi.DistributeFairly(liquidapi.SaturatingSub(maxQuota, totalAllocated), extraDesiredPerAZ)
				for _, az := range allAZs {
					t := target[az][projectID]
					t.Desired = t.Allocated + extraDesiredPerAZ[az]
				}
			}
		}
	}
}

func (target acpqGlobalTarget) TryFulfillDesired(stats map[limes.AvailabilityZone]clusterAZAllocationStats, cfg core.AutogrowQuotaDistributionConfiguration, allowsQuotaOvercommit map[limes.AvailabilityZone]bool) {
	// in AZs where quota overcommit is allowed, we do not have to be careful
	for az, azTarget := range target {
		if allowsQuotaOvercommit[az] {
			for _, projectTarget := range azTarget {
				projectTarget.Allocated = max(projectTarget.Desired, projectTarget.Allocated)
			}
		}
	}

	// real AZs (i.e. not "any") can only have their demand fulfilled locally,
	// using capacity in that specific AZ
	for az, azTarget := range target {
		if az == limes.AvailabilityZoneAny {
			continue
		}
		availableCapacity := liquidapi.SaturatingSub(stats[az].Capacity, azTarget.SumAllocated())
		if availableCapacity > 0 {
			granted := liquidapi.DistributeFairly(availableCapacity, azTarget.Requested())
			azTarget.AddGranted(granted)
		}
	}

	// the pseudo-AZ "any" can fulfil demand by absorbing all unused capacity
	var totalAvailable uint64
	for az, azTarget := range target {
		totalAvailable += liquidapi.SaturatingSub(stats[az].Capacity, azTarget.SumAllocated())
	}
	if totalAvailable > 0 {
		anyTarget := target[limes.AvailabilityZoneAny]
		granted := liquidapi.DistributeFairly(totalAvailable, anyTarget.Requested())
		anyTarget.AddGranted(granted)
	}
}
