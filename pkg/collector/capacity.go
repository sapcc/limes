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

	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

var scanInterval = 15 * time.Minute
var scanInitialDelay = 1 * time.Minute

//ScanCapacity queries the cluster's capacity (across all enabled backend
//services) periodically.
//
//Errors are logged instead of returned. The function will not return unless
//startup fails.
func ScanCapacity(driver limes.Driver) {
	//don't start scanning capacity immediately to avoid too much load on the
	//backend services when the collector comes up
	time.Sleep(scanInitialDelay)

	for {
		for _, service := range driver.Cluster().Services {
			plugin := limes.GetPlugin(service.Type)
			if plugin == nil {
				//don't need to log an error here; if this failed, the scraper thread
				//will already have reported the error
				continue
			}
			util.LogDebug("scanning %s capacity", service.Type)
			err := scanCapacity(driver, service.Type, plugin)
			if err != nil {
				util.LogError("scan %s capacity failed: %s", service.Type, err.Error())
			}
		}

		time.Sleep(scanInterval)
	}
}

func scanCapacity(driver limes.Driver, serviceType string, plugin limes.Plugin) error {
	capacities, err := plugin.Capacity(driver)
	if err != nil {
		return err
	}
	scrapedAt := time.Now()

	//do the following in a transaction to avoid inconsistent DB state
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer db.RollbackUnlessCommitted(tx)

	//find or create the cluster_services entry
	var serviceID int64
	err = tx.QueryRow(
		`UPDATE cluster_services SET scraped_at = $1 WHERE cluster_id = $2 AND name = $3 RETURNING id`,
		scrapedAt, driver.Cluster().ID, serviceType,
	).Scan(&serviceID)
	switch err {
	case nil:
		//entry found - nothing to do here
	case sql.ErrNoRows:
		//need to create the cluster_services entry
		err := tx.QueryRow(
			`INSERT INTO cluster_services (cluster_id, name, scraped_at) VALUES ($1, $2, $3) RETURNING id`,
			driver.Cluster().ID, serviceType, scrapedAt,
		).Scan(&serviceID)
		if err != nil {
			return err
		}
	default:
		return err
	}

	//update existing cluster_resources entries
	seen := make(map[string]bool)
	records, err := tx.Select(&db.ClusterResource{}, `SELECT * FROM cluster_resources WHERE service_id = $1`, serviceID)
	if err != nil {
		return err
	}
	for _, record := range records {
		res := record.(*db.ClusterResource)
		seen[res.Name] = true

		capacity, exists := capacities[res.Name]
		if exists {
			res.Capacity = capacity
			_, err := tx.Update(res)
			if err != nil {
				return err
			}
		} else {
			_, err := tx.Delete(res)
			if err != nil {
				return err
			}
		}
	}

	//insert missing cluster_resources entries
	for name, capacity := range capacities {
		if seen[name] {
			continue
		}
		res := &db.ClusterResource{
			ServiceID: serviceID,
			Name:      name,
			Capacity:  capacity,
		}
		err := tx.Insert(res)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}
