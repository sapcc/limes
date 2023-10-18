/*******************************************************************************
*
* Copyright 2023 SAP SE
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
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

const (
	//how long to wait before scraping the same capacitor again
	capacityScrapeInterval = 15 * time.Minute
	//how long to wait after error before retrying the same capacitor
	capacityScrapeErrorInterval = 3 * time.Minute
)

// CapacityScrapeJob is a jobloop.Job. Each task scrapes one capacitor.
// Cluster resources managed by this capacitor are added, updated and deleted as necessary.
func (c *Collector) CapacityScrapeJob(registerer prometheus.Registerer) jobloop.Job {
	//used by discoverCapacityScrapeTask() to trigger a consistency check every
	//once in a while; starts out very far in the past to force a consistency
	//check on first run
	lastConsistencyCheckAt := time.Unix(-1000000, 0).UTC()

	return (&jobloop.ProducerConsumerJob[capacityScrapeTask]{
		Metadata: jobloop.JobMetadata{
			ReadableName: "scrape capacity",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_capacity_scrapes",
				Help: "Counter for capacity scrape operations per capacitor.",
			},
			CounterLabels: []string{"capacitor_id"},
		},
		DiscoverTask: func(ctx context.Context, labels prometheus.Labels) (capacityScrapeTask, error) {
			return c.discoverCapacityScrapeTask(ctx, labels, &lastConsistencyCheckAt)
		},
		ProcessTask: c.processCapacityScrapeTask,
	}).Setup(registerer)
}

type capacityScrapeTask struct {
	Capacitor db.ClusterCapacitor
	Timing    TaskTiming
}

var (
	// upsert a cluster_capacitors entry
	initCapacitorQuery = sqlext.SimplifyWhitespace(`
		INSERT INTO cluster_capacitors (capacitor_id, next_scrape_at)
		VALUES ($1, $2)
		ON CONFLICT DO NOTHING
	`)

	// find the next capacitor that needs to have capacity scraped
	findCapacitorForScrapeQuery = sqlext.SimplifyWhitespace(`
		SELECT * FROM cluster_capacitors
		-- filter by need to be updated
		WHERE next_scrape_at <= $1
		-- order by update priority (first schedule, then ID for deterministic test behavior)
		ORDER BY next_scrape_at ASC, capacitor_id ASC
		-- find only one capacitor to scrape per iteration
		LIMIT 1
	`)

	// queries to collect context data within processCapacityScrapeTask()
	getClusterServicesQuery = sqlext.SimplifyWhitespace(`
		SELECT id, type FROM cluster_services
	`)
)

func (c *Collector) discoverCapacityScrapeTask(_ context.Context, _ prometheus.Labels, lastConsistencyCheckAt *time.Time) (task capacityScrapeTask, err error) {
	task.Timing.StartedAt = c.MeasureTime()

	//consistency check: every once in a while (and also immediately on startup),
	//check that all required `cluster_capacitors` entries exist
	//(this is important because the query below will only find capacitors that have such an entry)
	if lastConsistencyCheckAt.Before(task.Timing.StartedAt.Add(-5 * time.Minute)) {
		err = sqlext.WithPreparedStatement(c.DB, initCapacitorQuery, func(stmt *sql.Stmt) error {
			for capacitorID := range c.Cluster.CapacityPlugins {
				_, err := stmt.Exec(capacitorID, task.Timing.StartedAt)
				if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return task, fmt.Errorf("while creating cluster_capacitors entries: %w", err)
		}
		*lastConsistencyCheckAt = task.Timing.StartedAt
	}

	err = c.DB.SelectOne(&task.Capacitor, findCapacitorForScrapeQuery, task.Timing.StartedAt)
	return task, err
}

func (c *Collector) processCapacityScrapeTask(_ context.Context, task capacityScrapeTask, labels prometheus.Labels) (returnedErr error) {
	capacitor := task.Capacitor
	labels["capacitor_id"] = capacitor.CapacitorID

	defer func() {
		if returnedErr != nil {
			returnedErr = fmt.Errorf("while scraping capacitor %s: %w", capacitor.CapacitorID, returnedErr)
		}
	}()

	//if capacitor was removed from the configuration, clean up its DB entry
	plugin := c.Cluster.CapacityPlugins[capacitor.CapacitorID]
	if plugin == nil {
		_, err := c.DB.Delete(&capacitor)
		return err
	}

	//collect mapping of cluster_services type names to IDs
	//(these DB entries are maintained for us by checkConsistencyCluster)
	serviceIDForType := make(map[string]int64)
	serviceTypeForID := make(map[int64]string)
	err := sqlext.ForeachRow(c.DB, getClusterServicesQuery, nil, func(rows *sql.Rows) error {
		var (
			serviceID   int64
			serviceType string
		)
		err := rows.Scan(&serviceID, &serviceType)
		if err == nil {
			serviceTypeForID[serviceID] = serviceType
			serviceIDForType[serviceType] = serviceID
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("cannot collect cluster service mapping: %w", err)
	}

	//collect ownership information for existing cluster_resources
	var dbResources []db.ClusterResource
	_, err = c.DB.Select(&dbResources, `SELECT * FROM cluster_resources`)
	if err != nil {
		return fmt.Errorf("cannot inspect existing cluster resources: %w", err)
	}
	dbResourceLookup := make(map[string]map[string]db.ClusterResource)
	for _, res := range dbResources {
		serviceType := serviceTypeForID[res.ServiceID]
		if dbResourceLookup[serviceType] == nil {
			dbResourceLookup[serviceType] = make(map[string]db.ClusterResource)
		}
		dbResourceLookup[serviceType][res.Name] = res
	}

	//scrape capacity data
	capacityData, serializedMetrics, err := plugin.Scrape()
	task.Timing.FinishedAt = c.MeasureTimeAtEnd()
	if err == nil {
		capacitor.ScrapedAt = &task.Timing.FinishedAt
		capacitor.ScrapeDurationSecs = task.Timing.Duration().Seconds()
		capacitor.SerializedMetrics = serializedMetrics
		capacitor.NextScrapeAt = task.Timing.FinishedAt.Add(c.AddJitter(capacityScrapeInterval))
		capacitor.ScrapeErrorMessage = ""
		//NOTE: in this case, we continue below, with the cluster_resources update
		//the cluster_capacitors row will be updated at the end of the tx
	} else {
		err = util.UnpackError(err)
		capacitor.NextScrapeAt = task.Timing.FinishedAt.Add(c.AddJitter(capacityScrapeErrorInterval))
		capacitor.ScrapeErrorMessage = err.Error()

		_, updateErr := c.DB.Update(&capacitor)
		if updateErr != nil {
			err = fmt.Errorf("%w (additional error while updating DB: %s", err, updateErr.Error())
		}
		return err
	}

	//do the following in a transaction to avoid inconsistent DB state
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	//create or update cluster_resources for this capacitor
	for serviceType, serviceData := range capacityData {
		if !c.Cluster.HasService(serviceType) {
			logg.Info("discarding capacities reported by %s for unknown service type: %s", capacitor.CapacitorID, serviceType)
			continue
		}

		for resourceName, resourceDataPerAZ := range serviceData {
			if !c.Cluster.HasResource(serviceType, resourceName) {
				logg.Info("discarding capacity reported by %s for unknown resource name: %s/%s", capacitor.CapacitorID, serviceType, resourceName)
				continue
			}
			serviceID, ok := serviceIDForType[serviceType]
			if !ok {
				return fmt.Errorf("no cluster_services entry for service type %s (check if CheckConsistencyJob runs correctly)", serviceType)
			}

			summedResourceData := resourceDataPerAZ.Sum()
			res := db.ClusterResource{
				ServiceID:         serviceID,
				Name:              resourceName,
				RawCapacity:       summedResourceData.Capacity,
				CapacityPerAZJSON: "", //see below
				SubcapacitiesJSON: "", //see below
				CapacitorID:       capacitor.CapacitorID,
			}

			if shouldStoreAZReport(resourceDataPerAZ) {
				buf, err := json.Marshal(convertAZReport(resourceDataPerAZ))
				if err != nil {
					return fmt.Errorf("could not convert capacities per AZ to JSON: %w", err)
				}
				res.CapacityPerAZJSON = string(buf)
			}

			if len(summedResourceData.Subcapacities) > 0 {
				buf, err := json.Marshal(summedResourceData.Subcapacities)
				if err != nil {
					return fmt.Errorf("could not convert subcapacities to JSON: %w", err)
				}
				res.SubcapacitiesJSON = string(buf)
			}

			if _, exists := dbResourceLookup[serviceType][resourceName]; exists {
				res.ID = dbResourceLookup[serviceType][resourceName].ID
				_, err = tx.Update(&res)
			} else {
				err = tx.Insert(&res)
			}
			if err != nil {
				return fmt.Errorf("could not write cluster resource %s/%s: %w", serviceType, resourceName, err)
			}
		}
	}

	//delete cluster_resources owned by this capacitor for which we do not have capacityData anymore
	for serviceType, serviceMapping := range dbResourceLookup {
		for resourceName, res := range serviceMapping {
			//do not delete if owned by a different capacitor
			if res.CapacitorID != capacitor.CapacitorID {
				continue
			}

			//do not delete if we still have capacity data
			_, ok := capacityData[serviceType][resourceName]
			if ok {
				continue
			}

			_, err = tx.Exec(`DELETE FROM cluster_resources WHERE service_id = $1 AND name = $2`,
				serviceIDForType[serviceType], resourceName,
			)
			if err != nil {
				return fmt.Errorf("could not cleanup cluster resource %s/%s: %w", serviceType, resourceName, err)
			}
		}
	}

	_, err = tx.Update(&capacitor)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func shouldStoreAZReport(capacityPerAZ core.PerAZ[core.CapacityData]) bool {
	for az := range capacityPerAZ {
		if az != limes.AvailabilityZoneAny {
			return true
		}
	}
	return false
}

func convertAZReport(capacityPerAZ core.PerAZ[core.CapacityData]) limesresources.ClusterAvailabilityZoneReports {
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
