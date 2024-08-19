/******************************************************************************
*
*  Copyright 2024 SAP SE
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
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// NOTE: Most private functions in this file have the name prefix "acpq"
// since they represent individual steps of ApplyComputedProjectQuota.

var (
	acpqGetLocalQuotaConstraintsQuery = sqlext.SimplifyWhitespace(`
		SELECT pr.id, pr.min_quota_from_backend, pr.max_quota_from_backend, pr.max_quota_from_admin, pr.override_quota_from_config
		  FROM project_services ps
		  JOIN project_resources pr ON pr.service_id = ps.id
		 WHERE ps.type = $1 AND pr.name = $2 AND (pr.min_quota_from_backend IS NOT NULL
		                                       OR pr.max_quota_from_backend IS NOT NULL
		                                       OR pr.max_quota_from_admin IS NOT NULL
		                                       OR pr.override_quota_from_config IS NOT NULL)
	`)

	// This does not need to create any entries in project_az_resources, because
	// Scrape() already created them for us.
	acpqUpdateAZQuotaQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_az_resources SET quota = $1 WHERE quota IS DISTINCT FROM $1 AND az = $2 AND resource_id = $3
	`)
	acpqUpdateProjectQuotaQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_resources SET quota = $1 WHERE quota IS DISTINCT FROM $1 AND id = $2 RETURNING service_id
	`)
	acpqUpdateProjectServicesQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_services SET quota_desynced_at = $1 WHERE id = $2 AND quota_desynced_at IS NULL
	`)
)

type projectLocalQuotaConstraints struct {
	MinQuota *uint64
	MaxQuota *uint64
}

func (c *projectLocalQuotaConstraints) AddMinQuota(value *uint64) {
	if value == nil {
		return
	}
	if c.MinQuota == nil {
		c.MinQuota = value
	} else {
		*c.MinQuota = min(*c.MinQuota, *value)
	}
}

func (c *projectLocalQuotaConstraints) AddMaxQuota(value *uint64) {
	if value == nil {
		return
	}
	if c.MaxQuota == nil {
		c.MaxQuota = value
	} else {
		*c.MaxQuota = max(*c.MaxQuota, *value)
	}
}

// ApplyComputedProjectQuota reevaluates auto-computed project quotas for the
// given resource, if supported by its quota distribution model.
func ApplyComputedProjectQuota(serviceType db.ServiceType, resourceName liquid.ResourceName, dbm *gorp.DbMap, cluster *core.Cluster, now time.Time) error {
	// only run for resources with quota and autogrow QD model
	resInfo := cluster.InfoForResource(serviceType, resourceName)
	if !resInfo.HasQuota {
		return nil
	}
	qdCfg := cluster.QuotaDistributionConfigForResource(serviceType, resourceName)
	if qdCfg.Autogrow == nil {
		return nil
	}
	cfg := *qdCfg.Autogrow

	// run the quota computation in a transaction (this must be done inside this
	// function because we want to commit this wide-reaching transaction before
	// starting to talk to backend services for ApplyBackendQuota)
	tx, err := dbm.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// collect required data
	stats, err := collectAZAllocationStats(serviceType, resourceName, nil, cluster, tx)
	if err != nil {
		return err
	}

	constraints := make(map[db.ProjectResourceID]projectLocalQuotaConstraints)
	err = sqlext.ForeachRow(tx, acpqGetLocalQuotaConstraintsQuery, []any{serviceType, resourceName}, func(rows *sql.Rows) error {
		var (
			resourceID              db.ProjectResourceID
			minQuotaFromBackend     *uint64
			maxQuotaFromBackend     *uint64
			maxQuotaFromAdmin       *uint64
			overrideQuotaFromConfig *uint64
		)
		err := rows.Scan(&resourceID, &minQuotaFromBackend, &maxQuotaFromBackend, &maxQuotaFromAdmin, &overrideQuotaFromConfig)
		if err != nil {
			return err
		}

		var c projectLocalQuotaConstraints
		c.AddMinQuota(minQuotaFromBackend)
		c.AddMaxQuota(maxQuotaFromBackend)
		c.AddMaxQuota(maxQuotaFromAdmin)
		c.AddMinQuota(overrideQuotaFromConfig)
		c.AddMaxQuota(overrideQuotaFromConfig)

		constraints[resourceID] = c
		return nil
	})
	if err != nil {
		return err
	}

	// evaluate QD algorithm
	target, allowsQuotaOvercommit := acpqComputeQuotas(stats, cfg, constraints)
	if logg.ShowDebug {
		logg.Debug("ACPQ for %s/%s: stats = %#v", serviceType, resourceName, stats)
		logg.Debug("ACPQ for %s/%s: cfg = %#v", serviceType, resourceName, cfg)
		logg.Debug("ACPQ for %s/%s: constraints = %#v", serviceType, resourceName, constraints)
		logg.Debug("ACPQ for %s/%s: allowsQuotaOvercommit = %#v", serviceType, resourceName, allowsQuotaOvercommit)
		buf, _ := json.Marshal(target) //nolint:errcheck
		logg.Debug("ACPQ for %s/%s: target = %s", serviceType, resourceName, string(buf))
	}

	// write new AZ quotas to database
	err = sqlext.WithPreparedStatement(tx, acpqUpdateAZQuotaQuery, func(stmt *sql.Stmt) error {
		for az, azTarget := range target {
			for resourceID, projectTarget := range azTarget {
				_, err := stmt.Exec(projectTarget.Allocated, az, resourceID)
				if err != nil {
					return fmt.Errorf("in AZ %s in project resource %d: %w", az, resourceID, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("while writing updated %s/%s AZ quotas to DB: %w", serviceType, resourceName, err)
	}

	// write overall project quotas to database
	quotasByResourceID := make(map[db.ProjectResourceID]uint64)
	for _, azTarget := range target {
		for resourceID, projectTarget := range azTarget {
			quotasByResourceID[resourceID] += projectTarget.Allocated
		}
	}
	servicesWithUpdatedQuota := make(map[db.ProjectServiceID]struct{})
	err = sqlext.WithPreparedStatement(tx, acpqUpdateProjectQuotaQuery, func(stmt *sql.Stmt) error {
		for resourceID, quota := range quotasByResourceID {
			var serviceID db.ProjectServiceID
			err := stmt.QueryRow(quota, resourceID).Scan(&serviceID)
			if err == sql.ErrNoRows {
				// if quota was not actually changed, do not remember this project service as being stale
				continue
			}
			if err != nil {
				return fmt.Errorf("in project resource %d: %w", resourceID, err)
			}
			servicesWithUpdatedQuota[serviceID] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("while writing updated %s/%s project quotas to DB: %w", serviceType, resourceName, err)
	}

	// mark project services with changed quota for SyncQuotaToBackendJob
	err = sqlext.WithPreparedStatement(tx, acpqUpdateProjectServicesQuery, func(stmt *sql.Stmt) error {
		for serviceID := range servicesWithUpdatedQuota {
			_, err := stmt.Exec(now, serviceID)
			if err != nil {
				return fmt.Errorf("in project service %d: %w", serviceID, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("while marking updated %s/%s project quotas for sync in DB: %w", serviceType, resourceName, err)
	}

	return tx.Commit()

	//NOTE: Quotas are not applied to the backend here because OpenStack is way too inefficient in practice.
	// We wait for the next scrape cycle to come around and notice that `backend_quota != quota`.
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
	return subtractOrZero(t.Desired, t.Allocated)
}

// Like `lhs - rhs`, but never underflows below zero.
func subtractOrZero(lhs, rhs uint64) uint64 {
	if lhs <= rhs {
		return 0
	}
	return lhs - rhs
}

// Calculation space for all project AZ resources in a single AZ.
type acpqAZTarget map[db.ProjectResourceID]*acpqProjectAZTarget

// SumAllocated returns how much is allocated across all projects.
func (t acpqAZTarget) SumAllocated() (result uint64) {
	for _, pt := range t {
		result += pt.Allocated
	}
	return
}

// Requested returns how much is desired, but not allocated yet, for each project.
func (t acpqAZTarget) Requested() map[db.ProjectResourceID]uint64 {
	result := make(map[db.ProjectResourceID]uint64, len(t))
	for resourceID, pt := range t {
		result[resourceID] = pt.Requested()
	}
	return result
}

// AddGranted extends the allocations in this map by the granted values.
func (t acpqAZTarget) AddGranted(granted map[db.ProjectResourceID]uint64) {
	for resourceID, pt := range t {
		pt.Allocated += granted[resourceID]
	}
}

// Calculation space for the entire quota algorithm.
type acpqGlobalTarget map[limes.AvailabilityZone]acpqAZTarget

// This function comprises the entirety of the actual quota distribution algorithm.
// It is called by ApplyComputedProjectQuota() which contains all the side
// effects (reading the DB, writing the DB, setting quota in the backend).
// This function is separate because most test cases work on this level.
// The full ApplyComputedProjectQuota() function is tested during capacity scraping.
func acpqComputeQuotas(stats map[limes.AvailabilityZone]clusterAZAllocationStats, cfg core.AutogrowQuotaDistributionConfiguration, constraints map[db.ProjectResourceID]projectLocalQuotaConstraints) (target acpqGlobalTarget, allowsQuotaOvercommit map[limes.AvailabilityZone]bool) {
	// enumerate which project resource IDs and AZs are relevant
	// ("Relevant" AZs are all that have allocation stats available.)
	isProjectResourceID := make(map[db.ProjectResourceID]struct{})
	isRelevantAZ := make(map[limes.AvailabilityZone]struct{}, len(stats))
	var allAZsInOrder []limes.AvailabilityZone
	for az, azStats := range stats {
		isRelevantAZ[az] = struct{}{}
		allAZsInOrder = append(allAZsInOrder, az)
		for resourceID := range azStats.ProjectStats {
			isProjectResourceID[resourceID] = struct{}{}
		}
	}
	slices.Sort(allAZsInOrder)
	if cfg.ProjectBaseQuota > 0 {
		// base quota is given out in the pseudo-AZ "any", so we need to calculate quota for "any", too
		isRelevantAZ[limes.AvailabilityZoneAny] = struct{}{}
	}

	// enumerate which AZs allow quota overcommit
	allowsQuotaOvercommit = make(map[limes.AvailabilityZone]bool)
	isAZAware := false
	allRealAZsAllowQuotaOvercommit := true
	for az := range isRelevantAZ {
		allowsQuotaOvercommit[az] = stats[az].AllowsQuotaOvercommit(cfg)
		if az != limes.AvailabilityZoneAny && az != limes.AvailabilityZoneUnknown {
			isAZAware = true
			if !allowsQuotaOvercommit[az] {
				allRealAZsAllowQuotaOvercommit = false
			}
		}
	}

	// in AZ-aware resources, quota for the pseudo-AZ "any" is backed by capacity
	// in all the real AZs, so it can only allow quota overcommit if all AZs do
	if isAZAware {
		allowsQuotaOvercommit[limes.AvailabilityZoneAny] = allRealAZsAllowQuotaOvercommit
	}

	// initialize data structure where new quota will be computed (this uses eager
	// allocation of all required entries to simplify the following steps)
	//
	// Because we're looping through everything anyway, we're also doing the first
	// few steps of the algorithm itself that use the same looping pattern.
	target = make(acpqGlobalTarget, len(stats))
	for az := range isRelevantAZ {
		target[az] = make(acpqAZTarget, len(isProjectResourceID))
		for resourceID := range isProjectResourceID {
			projectAZStats := stats[az].ProjectStats[resourceID]
			target[az][resourceID] = &acpqProjectAZTarget{
				// phase 1: always grant hard minimum quota
				Allocated: max(projectAZStats.Committed, projectAZStats.Usage),
				// phase 2: try granting soft minimum quota
				Desired: projectAZStats.MaxHistoricalUsage,
			}
		}
	}
	target.EnforceConstraints(constraints, allAZsInOrder, isProjectResourceID, isAZAware)
	target.TryFulfillDesired(stats, cfg, allowsQuotaOvercommit)

	// phase 3: try granting desired_quota
	for az := range isRelevantAZ {
		for resourceID := range isProjectResourceID {
			projectAZStats := stats[az].ProjectStats[resourceID]
			growthBaseline := max(projectAZStats.Committed, projectAZStats.MinHistoricalUsage)
			desiredQuota := uint64(float64(growthBaseline) * cfg.GrowthMultiplier)
			if cfg.GrowthMultiplier > 1.0 && growthBaseline > 0 {
				// fix nonzero growth factor rounding to zero
				// e.g. growthBaseline = 5 and GrowthMultiplier = 1.1 -> desiredQuota = uint64(5.0 * 1.1) = 5
				growthMinimum := max(cfg.GrowthMinimum, 1)
				desiredQuota = max(desiredQuota, growthBaseline+growthMinimum)
			}
			target[az][resourceID].Desired = desiredQuota
		}
	}
	target.EnforceConstraints(constraints, allAZsInOrder, isProjectResourceID, isAZAware)
	target.TryFulfillDesired(stats, cfg, allowsQuotaOvercommit)

	// phase 4: try granting additional "any" quota until sum of all quotas is ProjectBaseQuota
	if cfg.ProjectBaseQuota > 0 {
		for resourceID := range isProjectResourceID {
			sumOfLocalizedQuotas := uint64(0)
			for az := range isRelevantAZ {
				if az != limes.AvailabilityZoneAny {
					sumOfLocalizedQuotas += target[az][resourceID].Allocated
				}
			}
			if sumOfLocalizedQuotas < cfg.ProjectBaseQuota {
				target[limes.AvailabilityZoneAny][resourceID].Desired = cfg.ProjectBaseQuota - sumOfLocalizedQuotas
			}
		}
		if !slices.Contains(allAZsInOrder, limes.AvailabilityZoneAny) {
			allAZsInOrder = append(allAZsInOrder, limes.AvailabilityZoneAny)
		}
		target.EnforceConstraints(constraints, allAZsInOrder, isProjectResourceID, isAZAware)
		target.TryFulfillDesired(stats, cfg, allowsQuotaOvercommit)
	}

	return target, allowsQuotaOvercommit
}

// After increasing Desired, but before increasing Allocated, this decreases
// Desired in order to fit into project-local quota constraints.
func (target acpqGlobalTarget) EnforceConstraints(constraints map[db.ProjectResourceID]projectLocalQuotaConstraints, allAZs []limes.AvailabilityZone, isProjectResourceID map[db.ProjectResourceID]struct{}, isAZAware bool) {
	for resourceID, c := range constraints {
		// raise Allocated as necessary to fulfil minimum quota
		if c.MinQuota != nil && *c.MinQuota > 0 {
			totalAllocated := uint64(0)
			desireScalePerAZ := make(map[limes.AvailabilityZone]uint64)
			for _, az := range allAZs {
				// Quota should not be assgined to ANY AZ on AZ aware resources. This causes unusable quota distribution on manual quota overrides.
				if az == limes.AvailabilityZoneAny && isAZAware {
					continue
				}
				t := target[az][resourceID]
				totalAllocated += t.Allocated
				desireScalePerAZ[az] = *c.MinQuota * max(1, subtractOrZero(t.Desired, t.Allocated))
			}
			extraAllocatedPerAZ := util.DistributeFairly(subtractOrZero(*c.MinQuota, totalAllocated), desireScalePerAZ)
			for _, az := range allAZs {
				if az == limes.AvailabilityZoneAny && isAZAware {
					continue
				}
				target[az][resourceID].Allocated += extraAllocatedPerAZ[az]
			}
		}

		// lower Desired as necessary to fulfil maximum quota
		if c.MaxQuota != nil {
			totalAllocated := uint64(0)
			totalDesired := uint64(0)
			extraDesiredPerAZ := make(map[limes.AvailabilityZone]uint64)
			for _, az := range allAZs {
				t := target[az][resourceID]
				totalAllocated += t.Allocated
				totalDesired += max(t.Allocated, t.Desired)
				extraDesiredPerAZ[az] = subtractOrZero(t.Desired, t.Allocated)
			}
			if totalDesired > *c.MaxQuota {
				extraDesiredPerAZ = util.DistributeFairly(subtractOrZero(*c.MaxQuota, totalAllocated), extraDesiredPerAZ)
				for _, az := range allAZs {
					t := target[az][resourceID]
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
		availableCapacity := subtractOrZero(stats[az].Capacity, azTarget.SumAllocated())
		if availableCapacity > 0 {
			granted := util.DistributeFairly(availableCapacity, azTarget.Requested())
			azTarget.AddGranted(granted)
		}
	}

	// the pseudo-AZ "any" can fulfil demand by absorbing all unused capacity
	var totalAvailable uint64
	for az, azTarget := range target {
		totalAvailable += subtractOrZero(stats[az].Capacity, azTarget.SumAllocated())
	}
	if totalAvailable > 0 {
		anyTarget := target[limes.AvailabilityZoneAny]
		granted := util.DistributeFairly(totalAvailable, anyTarget.Requested())
		anyTarget.AddGranted(granted)
	}
}
