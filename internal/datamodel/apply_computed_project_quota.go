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
	"math"
	"slices"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// NOTE: Most private functions in this file have the name prefix "acpq"
// since they represent individual steps of ApplyComputedProjectQuota.

var (
	// This does not need to create any entries in project_az_resources, because
	// Scrape() already created them for us.
	acpqUpdateAZQuotaQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_az_resources SET quota = $1 WHERE quota IS DISTINCT FROM $1 AND az = $2 AND resource_id = (
			SELECT id FROM project_resources WHERE service_id = $3 AND name = $4
		)
	`)
	acpqComputeProjectQuotaQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_resources pr SET quota = (
			SELECT SUM(par.quota) FROM project_az_resources par WHERE par.resource_id = pr.id
		), desired_backend_quota = (
			SELECT SUM(par.quota) FROM project_az_resources par WHERE par.resource_id = pr.id
		) WHERE service_id IN (
			SELECT id FROM project_services WHERE type = $1
		)
	`)
	acpqListUpdatedProjectServicesQuery = sqlext.SimplifyWhitespace(`
		SELECT DISTINCT ps.id
		FROM project_services ps JOIN project_resources pr ON pr.service_id = ps.id
		WHERE ps.type = $1 AND pr.desired_backend_quota IS DISTINCT FROM pr.backend_quota
	`)
)

// ApplyComputedProjectQuota reevaluates auto-computed project quotas for the
// given resource, if supported by its quota distribution model.
func ApplyComputedProjectQuota(serviceType, resourceName string, dbm *gorp.DbMap, cluster *core.Cluster, now time.Time) error {
	//only run for resources with autogrow QD model
	qdCfg := cluster.QuotaDistributionConfigForResource(serviceType, resourceName)
	if qdCfg.Autogrow == nil {
		return nil
	}
	cfg := *qdCfg.Autogrow

	//run the quota computation in a transaction (this must be done inside this
	//function because we want to commit this wide-reaching transaction before
	//starting to talk to backend services for ApplyBackendQuota)
	tx, err := dbm.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	//collect required data
	stats, err := collectAZAllocationStats(serviceType, resourceName, nil, cluster, tx, now)
	if err != nil {
		return err
	}

	//evaluate QD algorithm
	target := acpqComputeQuotas(stats, cfg)

	//write new quotas to database
	err = sqlext.WithPreparedStatement(tx, acpqUpdateAZQuotaQuery, func(stmt *sql.Stmt) error {
		for az, azTarget := range target {
			for serviceID, projectTarget := range azTarget {
				_, err := stmt.Exec(projectTarget.Allocated, az, serviceID, resourceName)
				if err != nil {
					return fmt.Errorf("while updating quota for AZ %s in project service %d: %w", az, serviceID, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("while writing updated %s/%s quotas to DB: %w", serviceType, resourceName, err)
	}
	_, err = tx.Exec(acpqComputeProjectQuotaQuery, serviceType)
	if err != nil {
		return fmt.Errorf("while computing updated %s/%s backend quotas: %w", serviceType, resourceName, err)
	}

	err = tx.Commit()
	if err != nil {
		return err
	}

	//apply updated quotas to backend as needed
	err = sqlext.ForeachRow(dbm, acpqListUpdatedProjectServicesQuery, []any{serviceType}, func(rows *sql.Rows) error {
		srv := db.ServiceRef[db.ProjectServiceID]{Type: serviceType}
		err := rows.Scan(&srv.ID)
		if err != nil {
			return err
		}

		var dbProject db.Project
		err = dbm.SelectOne(&dbProject, `SELECT * FROM projects WHERE id IN (SELECT project_id FROM project_services WHERE id = $1)`, srv.ID)
		if err != nil {
			return fmt.Errorf("while loading project for project service %d: %w", srv.ID, err)
		}

		var dbDomain db.Domain
		err = dbm.SelectOne(&dbDomain, `SELECT * FROM domains WHERE id = $1`, dbProject.DomainID)
		if err != nil {
			return fmt.Errorf("while loading domain %d: %w", dbProject.DomainID, err)
		}

		err = ApplyBackendQuota(dbm, cluster, core.KeystoneDomainFromDB(dbDomain), dbProject, srv)
		if err != nil {
			return fmt.Errorf("while applying quotas for project %s: %w", dbProject.UUID, err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("while finding %s quotas to apply to the backend: %w", serviceType, err)
	}
	return nil
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
type acpqAZTarget map[db.ProjectServiceID]*acpqProjectAZTarget

// SumAllocated returns how much is allocated across all projects.
func (t acpqAZTarget) SumAllocated() (result uint64) {
	for _, pt := range t {
		result += pt.Allocated
	}
	return
}

// Requested returns how much is desired, but not allocated yet, for each project.
func (t acpqAZTarget) Requested() map[db.ProjectServiceID]uint64 {
	result := make(map[db.ProjectServiceID]uint64, len(t))
	for serviceID, pt := range t {
		result[serviceID] = pt.Requested()
	}
	return result
}

// AddGranted extends the allocations in this map by the granted values.
func (t acpqAZTarget) AddGranted(granted map[db.ProjectServiceID]uint64) {
	for serviceID, pt := range t {
		pt.Allocated += granted[serviceID]
	}
}

// Calculation space for the entire quota algorithm.
type acpqGlobalTarget map[limes.AvailabilityZone]acpqAZTarget

// This function comprises the entirety of the actual quota distribution algorithm.
// It is called by ApplyComputedProjectQuota() which contains all the side
// effects (reading the DB, writing the DB, setting quota in the backend).
// This function is separate because most test cases work on this level.
// The full ApplyComputedProjectQuota() function is tested during capacity scraping.
func acpqComputeQuotas(stats map[limes.AvailabilityZone]clusterAZAllocationStats, cfg core.AutogrowQuotaDistributionConfiguration) acpqGlobalTarget {
	isProjectServiceID := make(map[db.ProjectServiceID]struct{})
	isRelevantAZ := make(map[limes.AvailabilityZone]struct{}, len(stats))
	for az, azStats := range stats {
		isRelevantAZ[az] = struct{}{}
		for serviceID := range azStats.ProjectStats {
			isProjectServiceID[serviceID] = struct{}{}
		}
	}
	if cfg.ProjectBaseQuota > 0 {
		// base quota is given out in the pseudo-AZ "any", so we need to calculate quota for "any", too
		isRelevantAZ[limes.AvailabilityZoneAny] = struct{}{}
	}

	//initialize data structure where new quota will be computed (this uses eager
	//allocation of all required entries to simplify the following steps)
	//
	//Because we're looping through everything anyway, we're also doing the first
	//few steps of the algorithm itself that use the same looping pattern.
	target := make(acpqGlobalTarget, len(stats))
	for az := range isRelevantAZ {
		target[az] = make(acpqAZTarget, len(isProjectServiceID))
		for serviceID := range isProjectServiceID {
			projectAZStats := stats[az].ProjectStats[serviceID]
			target[az][serviceID] = &acpqProjectAZTarget{
				//phase 1: always grant hard minimum quota
				Allocated: max(projectAZStats.Committed, projectAZStats.Usage),
				//phase 2: try granting soft minimum quota
				Desired: projectAZStats.MaxHistoricalUsage,
			}
		}
	}
	target.TryFulfillDesired(stats, cfg)

	//phase 3: try granting desired_quota
	for az := range isRelevantAZ {
		for serviceID := range isProjectServiceID {
			projectAZStats := stats[az].ProjectStats[serviceID]
			growthBaseline := max(projectAZStats.Committed, projectAZStats.MinHistoricalUsage)
			desiredQuota := uint64(float64(growthBaseline) * cfg.GrowthMultiplier)
			if cfg.GrowthMultiplier > 1.0 && growthBaseline > 0 {
				// fix nonzero growth factor rounding to zero
				// e.g. growthBaseline = 5 and GrowthMultiplier = 1.1 -> desiredQuota = uint64(5.0 * 1.1) = 5
				growthMinimum := max(cfg.GrowthMinimum, 1)
				desiredQuota = max(desiredQuota, growthBaseline+growthMinimum)
			}
			target[az][serviceID].Desired = desiredQuota
		}
	}
	target.TryFulfillDesired(stats, cfg)

	//phase 4: try granting additional "any" quota until sum of all quotas is ProjectBaseQuota
	if cfg.ProjectBaseQuota > 0 {
		for serviceID := range isProjectServiceID {
			sumOfLocalizedQuotas := uint64(0)
			for az := range isRelevantAZ {
				if az != limes.AvailabilityZoneAny {
					sumOfLocalizedQuotas += target[az][serviceID].Allocated
				}
			}
			if sumOfLocalizedQuotas < cfg.ProjectBaseQuota {
				target[limes.AvailabilityZoneAny][serviceID].Desired = cfg.ProjectBaseQuota - sumOfLocalizedQuotas
			}
		}
		target.TryFulfillDesired(stats, cfg)
	}

	return target
}

func (target acpqGlobalTarget) TryFulfillDesired(stats map[limes.AvailabilityZone]clusterAZAllocationStats, cfg core.AutogrowQuotaDistributionConfiguration) {
	//if quota overcommit is allowed, we do not have to be careful
	if cfg.AllowQuotaOvercommit {
		for _, azTarget := range target {
			for _, projectTarget := range azTarget {
				projectTarget.Allocated = max(projectTarget.Desired, projectTarget.Allocated)
			}
		}
		return
	}

	//real AZs (i.e. not "any") can only have their demand fulfilled locally,
	//using capacity in that specific AZ
	for az, azTarget := range target {
		if az == limes.AvailabilityZoneAny {
			continue
		}
		availableCapacity := subtractOrZero(stats[az].Capacity, azTarget.SumAllocated())
		if availableCapacity > 0 {
			granted := acpqDistributeFairly(availableCapacity, azTarget.Requested())
			azTarget.AddGranted(granted)
		}
	}

	//the pseudo-AZ "any" can fulfil demand by absorbing all unused capacity
	var totalAvailable uint64
	for az, azTarget := range target {
		totalAvailable += subtractOrZero(stats[az].Capacity, azTarget.SumAllocated())
	}
	if totalAvailable > 0 {
		anyTarget := target[limes.AvailabilityZoneAny]
		granted := acpqDistributeFairly(totalAvailable, anyTarget.Requested())
		anyTarget.AddGranted(granted)
	}
}

// Give each requester as much as they requested if possible. If the total is
// smaller than the sum of all requests, distribute the total fairly according
// to how much was requested.
func acpqDistributeFairly[K comparable](total uint64, requested map[K]uint64) map[K]uint64 {
	//easy case: all requests can be granted
	sumOfRequests := uint64(0)
	for _, request := range requested {
		sumOfRequests += request
	}
	if sumOfRequests <= total {
		return requested
	}

	//a completely fair distribution would require using these floating-point values...
	exact := make(map[K]float64, len(requested))
	for key, request := range requested {
		exact[key] = float64(total*request) / float64(sumOfRequests)
	}

	//...but we have to round to uint64
	fair := make(map[K]uint64, len(requested))
	keys := make([]K, 0, len(requested))
	totalOfFair := uint64(0)
	for key := range requested {
		floor := uint64(math.Floor(exact[key]))
		fair[key] = floor
		totalOfFair += floor
		keys = append(keys, key)
	}

	//now we have `sum(fair) <= total` because the fractional parts were ignored;
	//to fix this, we distribute one more to the highest fractional parts, e.g.
	//
	//    total = 15
	//    requested = [ 4, 6, 7 ]
	//    exact = [ 3.529..., 5.294..., 6.176... ]
	//    fair before adjustment = [ 3, 5, 6 ]
	//    missing = 1
	//    fair after adjustment = [ 4, 5, 6 ] -> because exact[0] had the largest fractional part
	//
	missing := total - totalOfFair
	slices.SortFunc(keys, func(lhs, rhs K) int {
		leftRemainder := exact[lhs] - math.Floor(exact[lhs])
		rightRemainder := exact[rhs] - math.Floor(exact[rhs])
		if leftRemainder < rightRemainder {
			return -1
		} else if leftRemainder > rightRemainder {
			return +1
		} else {
			return 0
		}
	})
	for _, key := range keys[len(keys)-int(missing):] {
		fair[key] += 1
	}
	return fair
}