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
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

const (
	// how long to wait before scraping the same capacitor again
	capacityScrapeInterval = 15 * time.Minute
	// how long to wait after error before retrying the same capacitor
	capacityScrapeErrorInterval = 3 * time.Minute
)

// CapacityScrapeJob is a jobloop.Job. Each task scrapes one capacitor.
// Cluster resources managed by this capacitor are added, updated and deleted as necessary.
func (c *Collector) CapacityScrapeJob(registerer prometheus.Registerer) jobloop.Job {
	// used by discoverCapacityScrapeTask() to trigger a consistency check every
	// once in a while; starts out very far in the past to force a consistency
	// check on first run
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

	// This query updates `project_commitments.state` on all rows that have not
	// reached one of the final states ("superseded" and "expired").
	//
	// The result of this computation is used in all bulk queries on
	// project_commitments to replace lengthy and time-dependent conditions with
	// simple checks on the enum value in `state`.
	updateProjectCommitmentStatesForResourceQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_commitments
		   SET state = CASE WHEN superseded_at IS NOT NULL THEN 'superseded'
		                    WHEN expires_at <= $3          THEN 'expired'
		                    WHEN confirm_by > $3           THEN 'planned'
		                    WHEN confirmed_at IS NULL      THEN 'pending'
		                    ELSE 'active' END
		WHERE state NOT IN ('superseded', 'expired') AND az_resource_id IN (
			SELECT par.id
			  FROM project_services ps
			  JOIN project_resources pr ON pr.service_id = ps.id
			  JOIN project_az_resources par ON par.resource_id = pr.id
			 WHERE ps.type = $1 AND pr.name = $2
		)
	`)
)

func (c *Collector) discoverCapacityScrapeTask(_ context.Context, _ prometheus.Labels, lastConsistencyCheckAt *time.Time) (task capacityScrapeTask, err error) {
	task.Timing.StartedAt = c.MeasureTime()

	// consistency check: every once in a while (and also immediately on startup),
	// check that all required `cluster_capacitors` entries exist
	// (this is important because the query below will only find capacitors that have such an entry)
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

func (c *Collector) processCapacityScrapeTask(ctx context.Context, task capacityScrapeTask, labels prometheus.Labels) (returnedErr error) {
	capacitor := task.Capacitor
	labels["capacitor_id"] = capacitor.CapacitorID

	defer func() {
		if returnedErr != nil {
			returnedErr = fmt.Errorf("while scraping capacitor %s: %w", capacitor.CapacitorID, returnedErr)
		}
	}()

	// if capacitor was removed from the configuration, clean up its DB entry
	plugin := c.Cluster.CapacityPlugins[capacitor.CapacitorID]
	if plugin == nil {
		_, err := c.DB.Delete(&capacitor)
		return err
	}

	// collect mapping of cluster_services type names to IDs
	// (these DB entries are maintained for us by checkConsistencyCluster)
	serviceIDForType := make(map[db.ServiceType]db.ClusterServiceID)
	serviceTypeForID := make(map[db.ClusterServiceID]db.ServiceType)
	err := sqlext.ForeachRow(c.DB, getClusterServicesQuery, nil, func(rows *sql.Rows) error {
		var (
			serviceID   db.ClusterServiceID
			serviceType db.ServiceType
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

	// scrape capacity data
	capacityData, serializedMetrics, err := plugin.Scrape(ctx, datamodel.NewCapacityPluginBackchannel(c.Cluster, c.DB), c.Cluster.Config.AvailabilityZones)
	task.Timing.FinishedAt = c.MeasureTimeAtEnd()
	if err == nil {
		capacitor.ScrapedAt = &task.Timing.FinishedAt
		capacitor.ScrapeDurationSecs = task.Timing.Duration().Seconds()
		capacitor.SerializedMetrics = string(serializedMetrics)
		capacitor.NextScrapeAt = task.Timing.FinishedAt.Add(c.AddJitter(capacityScrapeInterval))
		capacitor.ScrapeErrorMessage = ""
		//NOTE: in this case, we continue below, with the cluster_resources update
		// the cluster_capacitors row will be updated at the end of the tx
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

	// do the following in a transaction to avoid inconsistent DB state
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// collect existing cluster_resources
	var dbResources []db.ClusterResource
	_, err = tx.Select(&dbResources, `SELECT * FROM cluster_resources`)
	if err != nil {
		return fmt.Errorf("cannot inspect existing cluster resources: %w", err)
	}

	// define the scope of the update
	var dbOwnedResources []db.ClusterResource
	for _, res := range dbResources {
		if res.CapacitorID == capacitor.CapacitorID {
			dbOwnedResources = append(dbOwnedResources, res)
		}
	}

	var wantedResources []db.ResourceRef[db.ClusterServiceID]
	for serviceType, serviceData := range capacityData {
		if !c.Cluster.HasService(serviceType) {
			logg.Info("discarding capacities reported by %s for unknown service type: %s", capacitor.CapacitorID, serviceType)
			continue
		}
		serviceID, ok := serviceIDForType[serviceType]
		if !ok {
			return fmt.Errorf("no cluster_services entry for service type %s (check if CheckConsistencyJob runs correctly)", serviceType)
		}

		for resourceName := range serviceData {
			if !c.Cluster.HasResource(serviceType, resourceName) {
				logg.Info("discarding capacity reported by %s for unknown resource name: %s/%s", capacitor.CapacitorID, serviceType, resourceName)
				continue
			}
			wantedResources = append(wantedResources, db.ResourceRef[db.ClusterServiceID]{
				ServiceID: serviceID,
				Name:      resourceName,
			})
		}
	}
	slices.SortFunc(wantedResources, db.CompareResourceRefs) // for deterministic test behavior

	// create and delete cluster_resources for this capacitor as needed
	setUpdate := db.SetUpdate[db.ClusterResource, db.ResourceRef[db.ClusterServiceID]]{
		ExistingRecords: dbOwnedResources,
		WantedKeys:      wantedResources,
		KeyForRecord:    db.ClusterResource.Ref,
		Create: func(ref db.ResourceRef[db.ClusterServiceID]) (db.ClusterResource, error) {
			return db.ClusterResource{
				ServiceID:   ref.ServiceID,
				Name:        ref.Name,
				CapacitorID: capacitor.CapacitorID,
			}, nil
		},
		Update: func(res *db.ClusterResource) (err error) { return nil },
	}
	dbOwnedResources, err = setUpdate.Execute(tx)
	if err != nil {
		return err
	}

	// collect cluster_az_resources for the cluster_resources owned by this capacitor
	dbOwnedResourceIDs := make([]any, len(dbOwnedResources))
	for idx, res := range dbOwnedResources {
		dbOwnedResourceIDs[idx] = res.ID
	}
	var dbAZResources []db.ClusterAZResource
	whereClause, queryArgs := db.BuildSimpleWhereClause(map[string]any{"resource_id": dbOwnedResourceIDs}, 0)
	_, err = tx.Select(&dbAZResources, `SELECT * FROM cluster_az_resources WHERE `+whereClause, queryArgs...)
	if err != nil {
		return fmt.Errorf("cannot inspect existing cluster AZ resources: %w", err)
	}
	dbAZResourcesByResourceID := make(map[db.ClusterResourceID][]db.ClusterAZResource)
	for _, azRes := range dbAZResources {
		dbAZResourcesByResourceID[azRes.ResourceID] = append(dbAZResourcesByResourceID[azRes.ResourceID], azRes)
	}

	// for each cluster_resources entry owned by this capacitor, maintain cluster_az_resources
	for _, res := range dbOwnedResources {
		serviceType := serviceTypeForID[res.ServiceID]
		resourceDataPerAZ := capacityData[serviceType][res.Name].Normalize(c.Cluster.Config.AvailabilityZones)

		setUpdate := db.SetUpdate[db.ClusterAZResource, liquid.AvailabilityZone]{
			ExistingRecords: dbAZResourcesByResourceID[res.ID],
			WantedKeys:      resourceDataPerAZ.Keys(),
			KeyForRecord: func(azRes db.ClusterAZResource) liquid.AvailabilityZone {
				return azRes.AvailabilityZone
			},
			Create: func(az liquid.AvailabilityZone) (db.ClusterAZResource, error) {
				return db.ClusterAZResource{
					ResourceID:       res.ID,
					AvailabilityZone: az,
				}, nil
			},
			Update: func(azRes *db.ClusterAZResource) (err error) {
				data := resourceDataPerAZ[azRes.AvailabilityZone]
				azRes.RawCapacity = data.Capacity
				azRes.Usage = data.Usage
				azRes.SubcapacitiesJSON, err = renderListToJSON("subcapacities", data.Subcapacities)
				return err
			},
		}
		_, err = setUpdate.Execute(tx)
		if err != nil {
			return err
		}
	}

	_, err = tx.Update(&capacitor)
	if err != nil {
		return err
	}
	err = tx.Commit()
	if err != nil {
		return err
	}

	// for all cluster resources thus updated, sync commitment states with reality
	for _, res := range dbOwnedResources {
		serviceType := serviceTypeForID[res.ServiceID]
		now := c.MeasureTime()
		_, err := c.DB.Exec(updateProjectCommitmentStatesForResourceQuery, serviceType, res.Name, now)
		if err != nil {
			return fmt.Errorf("while updating project_commitments.state for %s/%s: %w", serviceType, res.Name, err)
		}
	}

	// for all cluster resources thus updated, try to confirm pending commitments
	for _, res := range dbOwnedResources {
		err := c.confirmPendingCommitmentsIfNecessary(serviceTypeForID[res.ServiceID], res.Name)
		if err != nil {
			return err
		}
	}

	// for all cluster resources thus updated, recompute project quotas if necessary
	for _, res := range dbOwnedResources {
		serviceType := serviceTypeForID[res.ServiceID]
		now := c.MeasureTime()
		err := datamodel.ApplyComputedProjectQuota(serviceType, res.Name, c.DB, c.Cluster, now)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Collector) confirmPendingCommitmentsIfNecessary(serviceType db.ServiceType, resourceName liquid.ResourceName) error {
	behavior := c.Cluster.BehaviorForResource(serviceType, resourceName)
	now := c.MeasureTime()

	// do not run ConfirmPendingCommitments if commitments are not enabled (or not live yet) for this resource
	if len(behavior.CommitmentDurations) == 0 {
		return nil
	}
	if minConfirmBy := behavior.CommitmentMinConfirmDate; minConfirmBy != nil && minConfirmBy.After(now) {
		return nil
	}

	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	committableAZs := c.Cluster.Config.AvailabilityZones
	if !behavior.CommitmentIsAZAware {
		committableAZs = []liquid.AvailabilityZone{liquid.AvailabilityZoneAny}
	}
	for _, az := range committableAZs {
		loc := datamodel.AZResourceLocation{
			ServiceType:      serviceType,
			ResourceName:     resourceName,
			AvailabilityZone: az,
		}
		err = datamodel.ConfirmPendingCommitments(loc, c.Cluster, tx, now)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func renderListToJSON(attribute string, entries []any) (string, error) {
	if len(entries) == 0 {
		return "", nil
	}
	buf, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("could not convert %s to JSON: %w", attribute, err)
	}
	return string(buf), nil
}
