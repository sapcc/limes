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

	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// ConfirmProjectCommitments will try to confirm as many additional commitments
// as can be covered at the current capacity and usage values.
func ConfirmProjectCommitments(serviceType, resourceName string) error {
	//TODO implement (this stub allows UI development on the commitment API to proceed)
	//TODO note to self: generate audit events upon confirmation
	return nil
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
	getCommitabilityStatsQuery = sqlext.SimplifyWhitespace(`
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

// CanConfirmNewCommitment returns whether the given commitment request can be
// confirmed immediately upon creation in the given project.
func CanConfirmNewCommitment(req limesresources.CommitmentRequest, project db.Project, cluster *core.Cluster, dbi db.Interface, now time.Time) (bool, error) {
	clusterBehavior := cluster.BehaviorForResource(req.ServiceType, req.ResourceName, "")

	//query DB for capacity
	var rawCapacity uint64
	queryArgs := []any{req.ServiceType, req.ResourceName, req.AvailabilityZone}
	err := dbi.QueryRow(getRawCapacityQuery, queryArgs...).Scan(&rawCapacity)
	if err != nil {
		return false, fmt.Errorf("while getting raw capacity for %s/%s: %w", req.ServiceType, req.ResourceName, err)
	}
	capacity := rawCapacity
	if clusterBehavior.OvercommitFactor != 0 {
		capacity = clusterBehavior.OvercommitFactor.ApplyTo(rawCapacity)
	}

	//query DB for commitment-relevant utilization
	queryArgs = []any{req.ServiceType, req.ResourceName, req.AvailabilityZone, now}
	usedCapacity := uint64(0)
	err = sqlext.ForeachRow(dbi, getCommitabilityStatsQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			projectID int64
			committed uint64
			usage     uint64
		)
		err := rows.Scan(&projectID, &committed, &usage)
		if err != nil {
			return err
		}

		// calculate `sum_over_projects(max(committed, usage))` including the requested commitment
		if projectID == project.ID {
			usedCapacity += max(committed+req.Amount, usage)
		} else {
			usedCapacity += max(committed, usage)
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("while getting allocation stats for %s/%s: %w", req.ServiceType, req.ResourceName, err)
	}

	//commitment can be confirmed if it and all other commitments and usage fit in the total capacity
	return usedCapacity <= capacity, nil
}
