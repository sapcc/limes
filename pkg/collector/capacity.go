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
	"time"

	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/util"
)

var scanInterval = 15 * time.Minute
var scanInitialDelay = 1 * time.Minute

//ScanCapacity queries the cluster's capacity (across all enabled backend
//services) periodically.
//
//Errors are logged instead of returned. The function will not return unless
//startup fails.
func (c *Collector) ScanCapacity() {
	//don't start scanning capacity immediately to avoid too much load on the
	//backend services when the collector comes up
	time.Sleep(scanInitialDelay)

	for {
		util.LogDebug("scanning capacity")
		c.scanCapacity()

		time.Sleep(scanInterval)
	}
}

func (c *Collector) scanCapacity() {
	values := make(map[string]map[string]uint64)
	scrapedAt := c.TimeNow()
	cluster := c.Driver.Cluster()

	for capacitorID, plugin := range cluster.CapacityPlugins {
		capacities, err := plugin.Scrape(c.Driver)
		if err != nil {
			c.LogError("scan capacity with capacitor %s failed: %s", capacitorID, err.Error())
			continue
		}

		//merge capacities from this plugin into the overall capacity values map
		for serviceType, resources := range capacities {
			if _, ok := values[serviceType]; !ok {
				values[serviceType] = make(map[string]uint64)
			}
			for resourceName, value := range resources {
				values[serviceType][resourceName] = value
			}
		}
	}

	//skip values for services not enabled for this cluster
	for serviceType := range values {
		if !cluster.HasService(serviceType) {
			delete(values, serviceType)
		}
	}

	//skip values for resources not announced by the respective QuotaPlugin
	for serviceType, plugin := range cluster.QuotaPlugins {
		subvalues, exists := values[serviceType]
		if !exists {
			continue
		}
		names := make(map[string]bool)
		for name := range values {
			names[name] = true
		}
		for _, res := range plugin.Resources() {
			delete(names, res.Name)
		}
		for name := range names {
			delete(subvalues, name)
		}
	}

	err := c.writeCapacity(values, scrapedAt)
	if err != nil {
		c.LogError("write capacity failed: %s", err.Error())
	}
}

func (c *Collector) writeCapacity(values map[string]map[string]uint64, scrapedAt time.Time) error {
	clusterID := c.Driver.Cluster().ID

	//do the following in a transaction to avoid inconsistent DB state
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer db.RollbackUnlessCommitted(tx)

	//enumerate cluster_services entries: create missing ones, delete superfluous ones
	serviceIDForType := make(map[string]int64)
	serviceTypeForID := make(map[int64]string)
	var dbServices []*db.ClusterService
	_, err = tx.Select(&dbServices, `SELECT * FROM cluster_services WHERE cluster_id = $1`, clusterID)
	if err != nil {
		return err
	}
	for _, dbService := range dbServices {
		if _, ok := values[dbService.Type]; ok {
			serviceIDForType[dbService.Type] = dbService.ID
			serviceTypeForID[dbService.ID] = dbService.Type
		} else {
			_, err := tx.Delete(dbService)
			if err != nil {
				return err
			}
		}
	}
	for serviceType := range values {
		if _, ok := serviceIDForType[serviceType]; ok {
			continue
		}
		dbService := &db.ClusterService{
			ClusterID: clusterID,
			Type:      serviceType,
			ScrapedAt: &scrapedAt,
		}
		err := tx.Insert(dbService)
		if err != nil {
			return err
		}
		serviceIDForType[dbService.Type] = dbService.ID
		serviceTypeForID[dbService.ID] = dbService.Type
	}

	//update scraped_at timestamp on all cluster services in one step
	_, err = tx.Exec(`UPDATE cluster_services SET scraped_at = $1 WHERE cluster_id = $2`, scrapedAt, clusterID)
	if err != nil {
		return err
	}

	//same for resources as for services: create missing ones, update existing ones, delete superfluous ones
	for serviceType, serviceValues := range values {
		serviceID := serviceIDForType[serviceType]
		var dbResources []*db.ClusterResource
		_, err := tx.Select(&dbResources, `SELECT * FROM cluster_resources WHERE service_id = $1`, serviceID)
		if err != nil {
			return err
		}

		seen := make(map[string]bool)
		for _, dbResource := range dbResources {
			seen[dbResource.Name] = true

			capacity, exists := serviceValues[dbResource.Name]
			if exists {
				dbResource.Capacity = capacity
				_, err := tx.Update(dbResource)
				if err != nil {
					return err
				}
			} else {
				_, err := tx.Delete(dbResource)
				if err != nil {
					return err
				}
			}
		}

		//insert missing cluster_resources entries
		for name, capacity := range serviceValues {
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
	}

	return tx.Commit()
}
