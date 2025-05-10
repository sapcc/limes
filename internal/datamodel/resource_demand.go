/*******************************************************************************
*
* Copyright 2024 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package datamodel

import (
	"database/sql"
	"fmt"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// NewCapacityScrapeBackchannel builds a CapacityScrapeBackchannel.
func NewCapacityScrapeBackchannel(cluster *core.Cluster, dbi db.Interface) core.CapacityScrapeBackchannel {
	return capacityScrapeBackchannelImpl{cluster, dbi}
}

type capacityScrapeBackchannelImpl struct {
	Cluster *core.Cluster
	DB      db.Interface
}

var (
	getResourceDemandQuery = sqlext.SimplifyWhitespace(`
		SELECT par.az, par.usage, COALESCE(pc_view.active, 0), COALESCE(pc_view.pending, 0)
		  FROM project_services ps
		  JOIN project_resources pr ON pr.service_id = ps.id
		  JOIN project_az_resources par ON par.resource_id = pr.id
		  LEFT OUTER JOIN (
		    SELECT az_resource_id AS az_resource_id,
		           SUM(amount) FILTER (WHERE state = 'active') AS active,
		           SUM(amount) FILTER (WHERE state = 'pending') AS pending
		      FROM project_commitments
		     GROUP BY az_resource_id
		  ) pc_view ON pc_view.az_resource_id = par.id
		 WHERE ps.type = $1 AND pr.name = $2
	`)
)

// GetResourceDemand implements the CapacityScrapeBackchannel interface.
func (i capacityScrapeBackchannelImpl) GetResourceDemand(serviceType db.ServiceType, resourceName liquid.ResourceName) (liquid.ResourceDemand, error) {
	result := liquid.ResourceDemand{
		OvercommitFactor: i.Cluster.BehaviorForResource(serviceType, resourceName).OvercommitFactor,
		PerAZ:            make(map[limes.AvailabilityZone]liquid.ResourceDemandInAZ),
	}
	err := sqlext.ForeachRow(i.DB, getResourceDemandQuery, []any{serviceType, resourceName}, func(rows *sql.Rows) error {
		var (
			az                 limes.AvailabilityZone
			usage              uint64
			activeCommitments  uint64
			pendingCommitments uint64
		)
		err := rows.Scan(&az, &usage, &activeCommitments, &pendingCommitments)
		if err != nil {
			return err
		}

		demand := result.PerAZ[az]
		demand.Usage += usage
		if activeCommitments > usage {
			demand.UnusedCommitments += activeCommitments - usage
		}
		demand.PendingCommitments += pendingCommitments
		result.PerAZ[az] = demand

		return nil
	})
	if err != nil {
		return liquid.ResourceDemand{}, fmt.Errorf("while getting resource demand for %s/%s through backchannel: %w", serviceType, resourceName, err)
	}
	return result, nil
}
