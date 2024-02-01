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
	"strings"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// NewCapacityPluginBackchannel builds a CapacityPluginBackchannel.
func NewCapacityPluginBackchannel(cluster *core.Cluster, dbi db.Interface, now time.Time) core.CapacityPluginBackchannel {
	return capacityPluginBackchannelImpl{cluster, dbi, now}
}

type capacityPluginBackchannelImpl struct {
	Cluster *core.Cluster
	DB      db.Interface
	Now     time.Time
}

var (
	getPendingCommitmentsInResourceQuery = sqlext.SimplifyWhitespace(`
		SELECT ps.id, par.az, SUM(pc.amount)
		  FROM project_services ps
		  JOIN project_resources pr ON pr.service_id = ps.id
		  JOIN project_az_resources par ON par.resource_id = pr.id
		  JOIN project_commitments pc ON pc.az_resource_id = par.id
		 WHERE ps.type = $1 AND pr.name = $2
		   AND pc.confirmed_at IS NULL AND pc.superseded_at IS NULL AND pc.confirm_by <= $3
		 GROUP BY ps.id, par.az
	`)
)

// GetOvercommitFactor implements the CapacityPluginBackchannel interface.
func (i capacityPluginBackchannelImpl) GetOvercommitFactor(serviceType, resourceName string) (core.OvercommitFactor, error) {
	return i.Cluster.BehaviorForResource(serviceType, resourceName, "").OvercommitFactor, nil
}

// GetGlobalResourceDemand implements the CapacityPluginBackchannel interface.
func (i capacityPluginBackchannelImpl) GetGlobalResourceDemand(serviceType, resourceName string) (map[limes.AvailabilityZone]core.ResourceDemand, error) {
	type projectData struct {
		Usage              uint64
		Committed          uint64
		PendingCommitments uint64
	}
	data := make(map[limes.AvailabilityZone]map[db.ProjectServiceID]projectData)
	addData := func(serviceID db.ProjectServiceID, az limes.AvailabilityZone, fill func(*projectData)) {
		azData := data[az]
		if azData == nil {
			azData = make(map[db.ProjectServiceID]projectData)
			data[az] = azData
		}
		pdata := azData[serviceID] //or zero-initialized on first use
		fill(&pdata)
		azData[serviceID] = pdata
	}

	var noFilter *string //== nil
	queryArgs := []any{serviceType, resourceName, noFilter, i.Now}
	query := strings.Replace(getUsageInResourceQuery, "par.historical_usage", "''", 1)
	err := sqlext.ForeachRow(i.DB, query, queryArgs, func(rows *sql.Rows) error {
		var (
			serviceID db.ProjectServiceID
			az        limes.AvailabilityZone
			usage     uint64
			unused    string
			committed uint64
		)
		err := rows.Scan(&serviceID, &az, &usage, &unused, &committed)
		if err != nil {
			return err
		}
		addData(serviceID, az, func(pdata *projectData) {
			pdata.Usage = usage
			pdata.Committed = committed
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("while getting resource usage for %s/%s through backchannel: %w", serviceType, resourceName, err)
	}

	queryArgs = []any{serviceType, resourceName, i.Now}
	err = sqlext.ForeachRow(i.DB, getPendingCommitmentsInResourceQuery, queryArgs, func(rows *sql.Rows) error {
		var (
			serviceID db.ProjectServiceID
			az        limes.AvailabilityZone
			pending   uint64
		)
		err := rows.Scan(&serviceID, &az, &pending)
		if err != nil {
			return err
		}
		addData(serviceID, az, func(pdata *projectData) { pdata.PendingCommitments = pending })
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("while getting pending commitments for %s/%s through backchannel: %w", serviceType, resourceName, err)
	}

	result := make(map[limes.AvailabilityZone]core.ResourceDemand, len(data))
	for az, azData := range data {
		var demand core.ResourceDemand
		for _, projectData := range azData {
			demand.Usage += projectData.Usage
			if projectData.Committed > projectData.Usage {
				demand.UnusedCommitments += projectData.Committed - projectData.Usage
			}
			demand.PendingCommitments += projectData.PendingCommitments
		}
		result[az] = demand
	}
	return result, nil
}
