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

	"github.com/go-gorp/gorp/v3"
	"github.com/prometheus/client_golang/prometheus"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/util"
)

var (
	scanInterval     = 15 * time.Minute
	scanInitialDelay = 1 * time.Minute
)

// ScanCapacity queries the cluster's capacity (across all enabled backend
// services) periodically.
//
// Errors are logged instead of returned. The function will not return unless
// startup fails.
func (c *Collector) ScanCapacity() {
	//don't start scanning capacity immediately to avoid too much load on the
	//backend services when the collector comes up
	time.Sleep(scanInitialDelay)

	for {
		logg.Debug("scanning capacity")
		c.scanCapacity()

		time.Sleep(scanInterval)
	}
}

func (c *Collector) scanCapacity() {
	values := make(map[string]map[string]core.CapacityData)
	scrapedAt := c.TimeNow()

	capacitorInfo := make(map[string]db.ClusterCapacitor)

	for capacitorID, plugin := range c.Cluster.CapacityPlugins {
		labels := prometheus.Labels{
			"os_cluster": c.Cluster.ID,
			"capacitor":  capacitorID,
		}
		//always report the counter
		clusterCapacitorSuccessCounter.With(labels).Add(0)
		clusterCapacitorFailedCounter.With(labels).Add(0)

		provider, eo := c.Cluster.ProviderClient()
		scrapeStart := c.TimeNow()
		capacities, serializedMetrics, err := plugin.Scrape(provider, eo)
		scrapeDuration := c.TimeNow().Sub(scrapeStart)
		if err != nil {
			c.LogError("scan capacity with capacitor %s failed: %s", capacitorID, util.UnpackError(err).Error())
			clusterCapacitorFailedCounter.With(labels).Inc()
			continue
		}

		//merge capacities from this plugin into the overall capacity values map
		for serviceType, resources := range capacities {
			if _, ok := values[serviceType]; !ok {
				values[serviceType] = make(map[string]core.CapacityData)
			}
			for resourceName, value := range resources {
				values[serviceType][resourceName] = value
			}
		}

		clusterCapacitorSuccessCounter.With(labels).Inc()
		capacitorInfo[capacitorID] = db.ClusterCapacitor{
			ClusterID:          c.Cluster.ID,
			CapacitorID:        capacitorID,
			ScrapedAt:          &scrapedAt,
			ScrapeDurationSecs: scrapeDuration.Seconds(),
			SerializedMetrics:  serializedMetrics,
		}
	}

	//skip values for services not enabled for this cluster
	for serviceType := range values {
		if !c.Cluster.HasService(serviceType) {
			logg.Info("discarding capacity values for unknown service type: %s", serviceType)
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
			logg.Info("discarding capacity value for unknown resource: %s/%s", serviceType, name)
			delete(subvalues, name)
		}
	}

	//do the following in a transaction to avoid inconsistent DB state
	tx, err := c.DB.Begin()
	if err != nil {
		c.LogError("write capacity failed: %s", err.Error())
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	err = c.writeCapacitorInfo(tx, capacitorInfo)
	if err != nil {
		c.LogError("write capacity failed: %s", err.Error())
	}
	err = c.writeCapacity(tx, values, scrapedAt)
	if err != nil {
		c.LogError("write capacity failed: %s", err.Error())
	}
	err = tx.Commit()
	if err != nil {
		c.LogError("write capacity failed: %s", err.Error())
	}
}

func (c *Collector) writeCapacitorInfo(tx *gorp.Transaction, capacitorInfo map[string]db.ClusterCapacitor) error {
	//remove superfluous cluster_capacitors
	var dbCapacitors []db.ClusterCapacitor
	_, err := tx.Select(&dbCapacitors, `SELECT * FROM cluster_capacitors WHERE cluster_id = $1`, c.Cluster.ID)
	if err != nil {
		return err
	}
	isExistingCapacitor := make(map[string]bool)
	for _, dbCapacitor := range dbCapacitors {
		isExistingCapacitor[dbCapacitor.CapacitorID] = true
		_, exists := c.Cluster.CapacityPlugins[dbCapacitor.CapacitorID]
		if !exists {
			_, err := tx.Delete(&dbCapacitor) //nolint:gosec // Delete is not holding onto the pointer after it returns
			if err != nil {
				return err
			}
		}
	}

	//insert or update cluster_capacitors where a scrape was successful
	for _, capacitor := range capacitorInfo {
		if isExistingCapacitor[capacitor.CapacitorID] {
			_, err := tx.Update(&capacitor) //nolint:gosec // Update is not holding onto the pointer after it returns
			if err != nil {
				return err
			}
		} else {
			err := tx.Insert(&capacitor) //nolint:gosec // Insert is not holding onto the pointer after it returns
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (c *Collector) writeCapacity(tx *gorp.Transaction, values map[string]map[string]core.CapacityData, scrapedAt time.Time) error {
	//create missing cluster_services entries (superfluous ones will be cleaned
	//up by the CheckConsistency())
	serviceIDForType := make(map[string]int64)
	var dbServices []*db.ClusterService
	_, err := tx.Select(&dbServices, `SELECT * FROM cluster_services WHERE cluster_id = $1`, c.Cluster.ID)
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
	sort.Strings(allServiceTypes) //for reproducibility in unit test

	for _, serviceType := range allServiceTypes {
		_, exists := serviceIDForType[serviceType]
		if exists {
			continue
		}

		dbService := &db.ClusterService{
			ClusterID: c.Cluster.ID,
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
	_, err = tx.Exec(`UPDATE cluster_services SET scraped_at = $1 WHERE cluster_id = $2`, scrapedAt, c.Cluster.ID)
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
				dbResource.RawCapacity = data.Capacity

				if len(data.Subcapacities) == 0 {
					dbResource.SubcapacitiesJSON = ""
				} else {
					bytes, err := json.Marshal(data.Subcapacities)
					if err != nil {
						return fmt.Errorf("failed to convert subcapacities to JSON: %s", err.Error())
					}
					dbResource.SubcapacitiesJSON = string(bytes)
				}

				if len(data.CapacityPerAZ) == 0 {
					dbResource.CapacityPerAZJSON = ""
				} else {
					bytes, err := json.Marshal(convertAZReport(data.CapacityPerAZ))
					if err != nil {
						return fmt.Errorf("failed to convert capacities per availability zone to JSON: %s", err.Error())
					}
					dbResource.CapacityPerAZJSON = string(bytes)
				}

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
		var missingResourceNames []string
		for name := range serviceValues {
			if !seen[name] {
				missingResourceNames = append(missingResourceNames, name)
			}
		}
		sort.Strings(missingResourceNames) //for reproducibility in unit test
		for _, name := range missingResourceNames {
			data := serviceValues[name]
			res := &db.ClusterResource{
				ServiceID:         serviceID,
				Name:              name,
				RawCapacity:       data.Capacity,
				CapacityPerAZJSON: "", //but see below
				SubcapacitiesJSON: "",
			}

			if len(data.Subcapacities) != 0 {
				bytes, err := json.Marshal(data.Subcapacities)
				if err != nil {
					return fmt.Errorf("failed to convert subcapacities to JSON: %s", err.Error())
				}
				res.SubcapacitiesJSON = string(bytes)
			}

			if len(data.CapacityPerAZ) != 0 {
				bytes, err := json.Marshal(convertAZReport(data.CapacityPerAZ))
				if err != nil {
					return fmt.Errorf("failed to convert capacities per availability zone to JSON: %s", err.Error())
				}
				res.CapacityPerAZJSON = string(bytes)
			}

			err := tx.Insert(res)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func convertAZReport(capacityPerAZ map[string]*core.CapacityDataForAZ) limesresources.ClusterAvailabilityZoneReports {
	//The initial implementation wrote limesresources.ClusterAvailabilityZoneReports into
	//the CapacityPerAZJSON database field, even though
	//map[string]*core.CapacityDataForAZ would have been more appropriate. Now we
	//stick with it for compatibility's sake.
	report := make(limesresources.ClusterAvailabilityZoneReports, len(capacityPerAZ))
	for azName, azData := range capacityPerAZ {
		report[azName] = &limesresources.ClusterAvailabilityZoneReport{
			Name:     azName,
			Capacity: azData.Capacity,
			Usage:    azData.Usage,
		}
	}
	return report
}
