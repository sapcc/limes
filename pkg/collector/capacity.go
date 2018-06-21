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
	"encoding/json"
	"fmt"
	"sort"
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
	values := make(map[string]map[string]limes.CapacityData)
	scrapedAt := c.TimeNow()

	for capacitorID, plugin := range c.Cluster.CapacityPlugins {
		capacities, err := plugin.Scrape(c.Cluster.ProviderClient(), c.Cluster.ID)
		if err != nil {
			c.LogError("scan capacity with capacitor %s failed: %s", capacitorID, err.Error())
			continue
		}

		//merge capacities from this plugin into the overall capacity values map
		for serviceType, resources := range capacities {
			if _, ok := values[serviceType]; !ok {
				values[serviceType] = make(map[string]limes.CapacityData)
			}
			for resourceName, value := range resources {
				values[serviceType][resourceName] = value
			}
		}
	}

	//skip values for services not enabled for this cluster
	for serviceType := range values {
		if !c.Cluster.HasService(serviceType) {
			util.LogInfo("discarding capacity values for unknown service type: %s", serviceType)
			delete(values, serviceType)
		}
	}

	//skip values for resources not announced by the respective QuotaPlugin
	for serviceType, plugin := range c.Cluster.QuotaPlugins {
		subvalues, exists := values[serviceType]
		if !exists {
			continue
		}
		names := make(map[string]bool)
		for name := range subvalues {
			names[name] = true
		}
		for _, res := range plugin.Resources() {
			delete(names, res.Name)
		}
		for name := range names {
			util.LogInfo("discarding capacity value for unknown resource: %s/%s", serviceType, name)
			delete(subvalues, name)
		}
	}

	//split values into sharedValues and unsharedValues
	sharedValues := make(map[string]map[string]limes.CapacityData)
	unsharedValues := make(map[string]map[string]limes.CapacityData)
	for serviceType, subvalues := range values {
		if c.Cluster.IsServiceShared[serviceType] {
			sharedValues[serviceType] = subvalues
		} else {
			unsharedValues[serviceType] = subvalues
		}
	}

	err := c.writeCapacity("shared", sharedValues, scrapedAt)
	if err != nil {
		c.LogError("write capacity failed: %s", err.Error())
	}
	err = c.writeCapacity(c.Cluster.ID, unsharedValues, scrapedAt)
	if err != nil {
		c.LogError("write capacity failed: %s", err.Error())
	}
}

var listProtectedServicesQueryStr = `
	SELECT DISTINCT cs.id FROM cluster_services cs
		JOIN cluster_resources cr ON cr.service_id = cs.id
	 WHERE cs.cluster_id = $1 AND cr.comment != ''
`

func (c *Collector) writeCapacity(clusterID string, values map[string]map[string]limes.CapacityData, scrapedAt time.Time) error {
	//NOTE: clusterID is not taken from c.Cluster because it can also be "shared".

	//do the following in a transaction to avoid inconsistent DB state
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer db.RollbackUnlessCommitted(tx)

	//create missing cluster_services entries (superfluous ones will be cleaned
	//up by the CheckConsistency())
	serviceIDForType := make(map[string]int64)
	var dbServices []*db.ClusterService
	_, err = tx.Select(&dbServices, `SELECT * FROM cluster_services WHERE cluster_id = $1`, clusterID)
	if err != nil {
		return err
	}
	for _, dbService := range dbServices {
		serviceIDForType[dbService.Type] = dbService.ID
	}

	var allServiceTypes []string
	for serviceType := range values {
		allServiceTypes = append(allServiceTypes, serviceType)
	}
	sort.Strings(allServiceTypes) //for reproducability in unit test

	for _, serviceType := range allServiceTypes {
		_, exists := serviceIDForType[serviceType]
		if exists {
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
	}

	//update scraped_at timestamp on all cluster services in one step
	_, err = tx.Exec(`UPDATE cluster_services SET scraped_at = $1 WHERE cluster_id = $2`, scrapedAt, clusterID)
	if err != nil {
		return err
	}

	//enumerate cluster_resources: create missing ones, update existing ones, delete superfluous ones
	for _, serviceType := range allServiceTypes {
		serviceValues := values[serviceType]
		serviceID := serviceIDForType[serviceType]
		var dbResources []*db.ClusterResource
		_, err := tx.Select(&dbResources, `SELECT * FROM cluster_resources WHERE service_id = $1`, serviceID)
		if err != nil {
			return err
		}

		seen := make(map[string]bool)
		for _, dbResource := range dbResources {
			seen[dbResource.Name] = true

			data, exists := serviceValues[dbResource.Name]
			if exists {
				dbResource.Capacity = data.Capacity

				if len(data.Subcapacities) == 0 {
					dbResource.SubcapacitiesJSON = ""
				} else {
					bytes, err := json.Marshal(data.Subcapacities)
					if err != nil {
						return fmt.Errorf("failed to convert subcapacities to JSON: %s", err.Error())
					}
					dbResource.SubcapacitiesJSON = string(bytes)
				}

				//if this is a manually maintained record, upgrade it to automatically maintained
				dbResource.Comment = ""
				_, err := tx.Update(dbResource)
				if err != nil {
					return err
				}
			} else {
				//never delete capacity records for shared services (because the
				//current cluster might not have all relevant capacity plugins enabled,
				//thus serviceValues may not have the whole picture)
				if clusterID != "shared" && dbResource.Comment == "" {
					_, err := tx.Delete(dbResource)
					if err != nil {
						return err
					}
				}
			}
		}

		//insert missing cluster_resources entries
		var missingResourceNames []string
		for name := range serviceValues {
			if !seen[name] {
				missingResourceNames = append(missingResourceNames, name)
			}
		}
		sort.Strings(missingResourceNames) //for reproducability in unit test
		for _, name := range missingResourceNames {
			data := serviceValues[name]
			res := &db.ClusterResource{
				ServiceID:         serviceID,
				Name:              name,
				Capacity:          data.Capacity,
				SubcapacitiesJSON: "", //but see below
			}

			if len(data.Subcapacities) != 0 {
				bytes, err := json.Marshal(data.Subcapacities)
				if err != nil {
					return fmt.Errorf("failed to convert subcapacities to JSON: %s", err.Error())
				}
				res.SubcapacitiesJSON = string(bytes)
			}

			err := tx.Insert(res)
			if err != nil {
				return err
			}
		}
	}

	return tx.Commit()
}
