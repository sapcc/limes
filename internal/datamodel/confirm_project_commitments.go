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

var (
	// Commitments are confirmed in a chronological order, wherein `created_at`
	// has a higher priority than `confirm_by` to ensure that commitments created
	// at a later date cannot skip the queue when existing customers are already
	// waiting for commitments.
	//
	// The final `BY pc.id` ordering ensures deterministic behavior in tests.
	getConfirmableCommitmentsQuery = sqlext.SimplifyWhitespace(`
		SELECT ps.id, pc.id, pc.amount
		  FROM project_services ps
		  JOIN project_resources pr ON pr.service_id = ps.id
		  JOIN project_az_resources par ON par.resource_id = pr.id
		  JOIN project_commitments pc ON pc.az_resource_id = par.id
		 WHERE ps.type = $1 AND pr.name = $2 AND par.az = $3 AND pc.state = 'pending'
		 ORDER BY pc.created_at ASC, pc.confirm_by ASC, pc.id ASC
	`)
)

// CanConfirmNewCommitment returns whether the given commitment request can be
// confirmed immediately upon creation in the given project.
func CanConfirmNewCommitment(req limesresources.CommitmentRequest, project db.Project, cluster *core.Cluster, dbi db.Interface) (bool, error) {
	statsByAZ, err := collectAZAllocationStats(req.ServiceType, req.ResourceName, &req.AvailabilityZone, cluster, dbi)
	if err != nil {
		return false, err
	}
	stats := statsByAZ[req.AvailabilityZone]

	var serviceID db.ProjectServiceID
	err = dbi.QueryRow(`SELECT id FROM project_services WHERE project_id = $1 AND type = $2`, project.ID, req.ServiceType).Scan(&serviceID)
	if err != nil {
		return false, err
	}
	return stats.FitsAdditionalCommitment(serviceID, req.Amount), nil
}

// ConfirmPendingCommitments goes through all unconfirmed commitments that
// could be confirmed, in chronological creation order, and confirms as many of
// them as possible given the currently available capacity.
func ConfirmPendingCommitments(serviceType, resourceName string, az limes.AvailabilityZone, cluster *core.Cluster, dbi db.Interface, now time.Time) error {
	statsByAZ, err := collectAZAllocationStats(serviceType, resourceName, &az, cluster, dbi)
	if err != nil {
		return err
	}
	stats := statsByAZ[az]

	// load confirmable commitments (we need to load them into a buffer first, since
	// lib/pq cannot do UPDATE while a SELECT targeting the same rows is still going)
	type confirmableCommitment struct {
		ProjectServiceID db.ProjectServiceID
		CommitmentID     db.ProjectCommitmentID
		Amount           uint64
	}
	var confirmableCommitments []confirmableCommitment
	queryArgs := []any{serviceType, resourceName, az}
	err = sqlext.ForeachRow(dbi, getConfirmableCommitmentsQuery, queryArgs, func(rows *sql.Rows) error {
		var c confirmableCommitment
		err := rows.Scan(&c.ProjectServiceID, &c.CommitmentID, &c.Amount)
		confirmableCommitments = append(confirmableCommitments, c)
		return err
	})
	if err != nil {
		return fmt.Errorf("while enumerating confirmable commitments for %s/%s in %s: %w", serviceType, resourceName, az, err)
	}

	// foreach confirmable commitment...
	for _, c := range confirmableCommitments {
		// ignore commitments that do not fit
		if !stats.FitsAdditionalCommitment(c.ProjectServiceID, c.Amount) {
			continue
		}

		// confirm the commitment
		_, err = dbi.Exec(`UPDATE project_commitments SET confirmed_at = $1, state = $2 WHERE id = $3`,
			now, db.CommitmentStateActive, c.CommitmentID)
		if err != nil {
			return fmt.Errorf("while confirming commitment ID=%d for %s/%s in %s: %w", c.CommitmentID, serviceType, resourceName, az, err)
		}

		// block its allocation from being committed again in this loop
		oldStats := stats.ProjectStats[c.ProjectServiceID]
		stats.ProjectStats[c.ProjectServiceID] = projectAZAllocationStats{
			Committed: oldStats.Committed + c.Amount,
			Usage:     oldStats.Usage,
		}
	}

	return nil
}
