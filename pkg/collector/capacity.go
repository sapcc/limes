/*******************************************************************************
*
* Copyright 2017 SAP SE
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

package collector

import (
	"database/sql"
	"time"

	"github.com/sapcc/limes/pkg/drivers"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/models"
)

var scanInterval = 15 * time.Minute
var scanInitialDelay = 1 * time.Minute

//ScanCapacity queries the cluster's capacity (across all enabled backend services) periodically.
//
//Errors are logged instead of returned. The function will not return unless
//startup fails.
func ScanCapacity(driver drivers.Driver) {
	//don't start scanning capacity immediately to avoid too much load on the
	//backend services when the collector comes up
	time.Sleep(scanInitialDelay)

	for {
		for _, service := range driver.Cluster().EnabledServices() {
			plugin := GetPlugin(service.Type)
			if plugin == nil {
				//don't need to log an error here; if this failed, the scraper thread will already have reported the error
				continue
			}
			limes.Log(limes.LogDebug, "scanning %s capacity", service.Type)
			err := scanCapacity(driver, service.Type, plugin)
			if err != nil {
				limes.Log(limes.LogError, "scan %s capacity failed: %s", service.Type, err.Error())
			}
		}

		time.Sleep(scanInterval)
	}
}

func scanCapacity(driver drivers.Driver, serviceType string, plugin Plugin) error {
	capacities, err := plugin.Capacity(driver)
	if err != nil {
		return err
	}

	//do the following in a transaction to avoid inconsistent DB state
	tx, err := limes.DB.Begin()
	if err != nil {
		return err
	}
	defer limes.RollbackUnlessCommitted(tx)

	//find or create the cluster_services entry
	var serviceID uint64
	err = tx.QueryRow(
		`SELECT id FROM cluster_services WHERE cluster_id = $1 AND name = $2`,
		driver.Cluster().ID, serviceType,
	).Scan(&serviceID)
	switch err {
	case nil:
		//do nothing
	case sql.ErrNoRows:
		//need to create the cluster_services entry
		err := tx.QueryRow(
			`INSERT INTO cluster_services (cluster_id, name) VALUES ($1, $2) RETURNING id`,
			driver.Cluster().ID, serviceType,
		).Scan(&serviceID)
		if err != nil {
			return err
		}
	default:
		return err
	}

	//update existing cluster_resources entries
	seen := make(map[string]bool)
	err = models.ClusterResourcesTable.Where(`service_id = $1`, serviceID).
		Foreach(tx, func(record models.Record) error {
			res := record.(*models.ClusterResource)
			seen[res.Name] = true

			capacity, exists := capacities[res.Name]
			if exists {
				res.Capacity = capacity
				return res.Update(tx)
			}
			return res.Delete(tx)
		})
	if err != nil {
		return err
	}

	//insert missing cluster_resources entries
	for name, capacity := range capacities {
		if seen[name] {
			continue
		}
		res := &models.ClusterResource{
			ServiceID: serviceID,
			Name:      name,
			Capacity:  capacity,
		}
		err := res.Insert(tx)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}
