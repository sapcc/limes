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
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"
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
		SELECT pr.id, pc.id, pc.amount
		  FROM project_services ps
		  JOIN project_resources pr ON pr.service_id = ps.id
		  JOIN project_az_resources par ON par.resource_id = pr.id
		  JOIN project_commitments pc ON pc.az_resource_id = par.id
		 WHERE ps.type = $1 AND pr.name = $2 AND par.az = $3 AND pc.state = 'pending'
		 ORDER BY pc.created_at ASC, pc.confirm_by ASC, pc.id ASC
	`)
)

// AZResourceLocation is a tuple identifying an AZ resource within a project.
type AZResourceLocation struct {
	ServiceType      db.ServiceType
	ResourceName     liquid.ResourceName
	AvailabilityZone limes.AvailabilityZone
}

// CanConfirmNewCommitment returns whether the given commitment request can be
// confirmed immediately upon creation in the given project.
func CanConfirmNewCommitment(loc AZResourceLocation, resourceID db.ProjectResourceID, amount uint64, cluster *core.Cluster, dbi db.Interface) (bool, error) {
	statsByAZ, err := collectAZAllocationStats(loc.ServiceType, loc.ResourceName, &loc.AvailabilityZone, cluster, dbi)
	if err != nil {
		return false, err
	}
	stats := statsByAZ[loc.AvailabilityZone]

	additions := map[db.ProjectResourceID]uint64{resourceID: amount}
	ccr := cluster.CommitmentCreationRuleForResource(loc.ServiceType, loc.ResourceName)
	logg.Debug("checking CanConfirmNewCommitment in %s/%s/%s: resourceID = %d, amount = %d",
		loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, resourceID, amount)
	return stats.CanAcceptCommitmentChanges(additions, nil, ccr), nil
}

// CanMoveExistingCommitment returns whether a commitment of the given amount
// at the given AZ resource location can be moved from one project to another.
// The projects are identified by their resource IDs.
func CanMoveExistingCommitment(amount uint64, loc AZResourceLocation, sourceResourceID, targetResourceID db.ProjectResourceID, cluster *core.Cluster, dbi db.Interface) (bool, error) {
	statsByAZ, err := collectAZAllocationStats(loc.ServiceType, loc.ResourceName, &loc.AvailabilityZone, cluster, dbi)
	if err != nil {
		return false, err
	}
	stats := statsByAZ[loc.AvailabilityZone]

	additions := map[db.ProjectResourceID]uint64{targetResourceID: amount}
	subtractions := map[db.ProjectResourceID]uint64{sourceResourceID: amount}
	ccr := cluster.CommitmentCreationRuleForResource(loc.ServiceType, loc.ResourceName)
	logg.Debug("checking CanMoveExistingCommitment in %s/%s/%s: resourceID = %d -> %d, amount = %d",
		loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, sourceResourceID, targetResourceID, amount)
	return stats.CanAcceptCommitmentChanges(additions, subtractions, ccr), nil
}

// ConfirmPendingCommitments goes through all unconfirmed commitments that
// could be confirmed, in chronological creation order, and confirms as many of
// them as possible given the currently available capacity.
func ConfirmPendingCommitments(loc AZResourceLocation, cluster *core.Cluster, dbi db.Interface, now time.Time) error {
	ccr := cluster.CommitmentCreationRuleForResource(loc.ServiceType, loc.ResourceName)

	statsByAZ, err := collectAZAllocationStats(loc.ServiceType, loc.ResourceName, &loc.AvailabilityZone, cluster, dbi)
	if err != nil {
		return err
	}
	stats := statsByAZ[loc.AvailabilityZone]

	// load confirmable commitments (we need to load them into a buffer first, since
	// lib/pq cannot do UPDATE while a SELECT targeting the same rows is still going)
	type confirmableCommitment struct {
		ProjectResourceID db.ProjectResourceID
		CommitmentID      db.ProjectCommitmentID
		Amount            uint64
	}
	var confirmableCommitments []confirmableCommitment
	queryArgs := []any{loc.ServiceType, loc.ResourceName, loc.AvailabilityZone}
	err = sqlext.ForeachRow(dbi, getConfirmableCommitmentsQuery, queryArgs, func(rows *sql.Rows) error {
		var c confirmableCommitment
		err := rows.Scan(&c.ProjectResourceID, &c.CommitmentID, &c.Amount)
		confirmableCommitments = append(confirmableCommitments, c)
		return err
	})
	if err != nil {
		return fmt.Errorf("while enumerating confirmable commitments for %s/%s in %s: %w", loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
	}

	// foreach confirmable commitment...
	for _, c := range confirmableCommitments {
		// ignore commitments that do not fit
		additions := map[db.ProjectResourceID]uint64{c.ProjectResourceID: c.Amount}
		logg.Debug("checking ConfirmPendingCommitments in %s/%s/%s: resourceID = %d, amount = %d",
			loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, c.ProjectResourceID, c.Amount)
		if !stats.CanAcceptCommitmentChanges(additions, nil, ccr) {
			continue
		}

		// confirm the commitment
		_, err = dbi.Exec(`UPDATE project_commitments SET confirmed_at = $1, state = $2 WHERE id = $3`,
			now, db.CommitmentStateActive, c.CommitmentID)
		if err != nil {
			return fmt.Errorf("while confirming commitment ID=%d for %s/%s in %s: %w", c.CommitmentID, loc.ServiceType, loc.ResourceName, loc.AvailabilityZone, err)
		}

		// block its allocation from being committed again in this loop
		oldStats := stats.ProjectStats[c.ProjectResourceID]
		stats.ProjectStats[c.ProjectResourceID] = projectAZAllocationStats{
			Committed: oldStats.Committed + c.Amount,
			Usage:     oldStats.Usage,
		}
	}

	return nil
}
