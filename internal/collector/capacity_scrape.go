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
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	. "github.com/majewsky/gg/option"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

const (
	// how long to wait before scraping the same service again
	capacityScrapeInterval = 15 * time.Minute
	// how long to wait after error before retrying the same service
	capacityScrapeErrorInterval = 3 * time.Minute
)

// CapacityScrapeJob is a jobloop.Job. Each task scrapes one Liquid, equal to one ClusterService entry.
// ClusterResources and ClusterAZResources are managed by this job. ClusterServices are managed by the CheckConsistencyJob.
func (c *Collector) CapacityScrapeJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.ProducerConsumerJob[capacityScrapeTask]{
		Metadata: jobloop.JobMetadata{
			ReadableName: "scrape capacity",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_capacity_scrapes",
				Help: "Counter for capacity scrape operations per service_type.",
			},
			CounterLabels: []string{"service_type"},
		},
		DiscoverTask: func(ctx context.Context, labels prometheus.Labels) (capacityScrapeTask, error) {
			return c.discoverCapacityScrapeTask(ctx, labels)
		},
		ProcessTask: c.processCapacityScrapeTask,
	}).Setup(registerer)
}

type capacityScrapeTask struct {
	Service db.ClusterService
	Timing  TaskTiming
}

var (
	// find the next cluster_service that needs to have capacity scraped
	findServiceForScrapeQuery = sqlext.SimplifyWhitespace(`
		SELECT * FROM cluster_services
		-- filter by need to be updated
		WHERE next_scrape_at <= $1
		-- order by update priority (first schedule, then ID for deterministic test behavior)
		ORDER BY next_scrape_at ASC, id ASC
		-- find only one service to scrape per iteration
		LIMIT 1
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

func (c *Collector) discoverCapacityScrapeTask(_ context.Context, _ prometheus.Labels) (task capacityScrapeTask, err error) {
	task.Timing.StartedAt = c.MeasureTime()
	// CheckConsistencyJob will ensure that all cluster_services are present in the DB. Before it runs,
	// we might have a cluster_service entry without a corresponding LiquidConnection or vise versa.

	err = c.DB.SelectOne(&task.Service, findServiceForScrapeQuery, task.Timing.StartedAt)
	return task, err
}

func (c *Collector) processCapacityScrapeTask(ctx context.Context, task capacityScrapeTask, labels prometheus.Labels) (returnedErr error) {
	service := task.Service
	labels["service_type"] = string(service.Type)

	defer func() {
		if returnedErr != nil {
			returnedErr = fmt.Errorf("while scraping clusterService %s: %w", strconv.FormatInt(int64(service.ID), 10), returnedErr)
		}
	}()

	// if cluster_service is not in the LiquidConnections, do nothing
	connection := c.Cluster.LiquidConnections[service.Type]
	if connection == nil {
		task.Timing.FinishedAt = c.MeasureTimeAtEnd()
		service.NextScrapeAt = task.Timing.FinishedAt.Add(c.AddJitter(capacityScrapeInterval))
		_, err := c.DB.Update(&service)
		if err != nil {
			err = fmt.Errorf("error while skipping scrape for %s: %w", service.Type, err)
			return err
		}
		return nil
	}

	// scrape capacity data
	capacityData, serializedMetrics, err := c.scrapeLiquidCapacity(ctx, connection)
	task.Timing.FinishedAt = c.MeasureTimeAtEnd()
	if err == nil {
		service.ScrapedAt = Some(task.Timing.FinishedAt)
		service.ScrapeDurationSecs = task.Timing.Duration().Seconds()
		service.SerializedMetrics = string(serializedMetrics)
		service.NextScrapeAt = task.Timing.FinishedAt.Add(c.AddJitter(capacityScrapeInterval))
		service.ScrapeErrorMessage = ""
		//NOTE: in this case, we continue below, with the cluster_resources update
		// the cluster_services row will be updated at the end of the tx
	} else {
		err = util.UnpackError(err)
		service.NextScrapeAt = task.Timing.FinishedAt.Add(c.AddJitter(capacityScrapeErrorInterval))
		service.ScrapeErrorMessage = err.Error()

		_, updateErr := c.DB.Update(&service)
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
		if res.ServiceID == service.ID {
			dbOwnedResources = append(dbOwnedResources, res)
		}
	}

	var wantedResources []liquid.ResourceName

	for resourceName := range capacityData.Resources {
		if !c.Cluster.HasResource(connection.ServiceType, resourceName) {
			logg.Info("discarding capacity reported by %s for unknown resource name: %s/%s", service.ID, connection.ServiceType, resourceName)
			continue
		}
		wantedResources = append(wantedResources, resourceName)
	}
	slices.Sort(wantedResources)

	// create and delete cluster_resources for this cluster_service as needed
	setUpdate := db.SetUpdate[db.ClusterResource, liquid.ResourceName]{
		ExistingRecords: dbOwnedResources,
		WantedKeys:      wantedResources,
		KeyForRecord: func(resource db.ClusterResource) liquid.ResourceName {
			return resource.Name
		},
		Create: func(resourceName liquid.ResourceName) (db.ClusterResource, error) {
			return db.ClusterResource{
				ServiceID: service.ID,
				Name:      resourceName,
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
		resourceData := capacityData.Resources[res.Name]

		setUpdate := db.SetUpdate[db.ClusterAZResource, liquid.AvailabilityZone]{
			ExistingRecords: dbAZResourcesByResourceID[res.ID],
			WantedKeys:      slices.Sorted(maps.Keys(resourceData.PerAZ)),
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
				data := resourceData.PerAZ[azRes.AvailabilityZone]
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

	_, err = tx.Update(&service)
	if err != nil {
		return err
	}
	err = tx.Commit()
	if err != nil {
		return err
	}

	// for all cluster resources thus updated, sync commitment states with reality
	for _, res := range dbOwnedResources {
		now := c.MeasureTime()
		_, err := c.DB.Exec(updateProjectCommitmentStatesForResourceQuery, service.Type, res.Name, now)
		if err != nil {
			return fmt.Errorf("while updating project_commitments.state for %s/%s: %w", service.Type, res.Name, err)
		}
	}

	// for all cluster resources thus updated, try to confirm pending commitments
	for _, res := range dbOwnedResources {
		err := c.confirmPendingCommitmentsIfNecessary(service.Type, res.Name)
		if err != nil {
			return err
		}
	}

	// for all cluster resources thus updated, recompute project quotas if necessary
	for _, res := range dbOwnedResources {
		now := c.MeasureTime()
		err := datamodel.ApplyComputedProjectQuota(service.Type, res.Name, c.DB, c.Cluster, now)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Collector) scrapeLiquidCapacity(ctx context.Context, connection *core.LiquidConnection) (liquid.ServiceCapacityReport, []byte, error) {
	capacityData, err := connection.ScrapeCapacity(ctx, datamodel.NewCapacityScrapeBackchannel(c.Cluster, c.DB), c.Cluster.Config.AvailabilityZones)
	if err != nil {
		return liquid.ServiceCapacityReport{}, nil, err
	}
	serializedMetrics, err := core.LiquidSerializeMetrics(connection.ServiceInfo().CapacityMetricFamilies, capacityData.Metrics)
	if err != nil {
		return liquid.ServiceCapacityReport{}, nil, err
	}
	return capacityData, serializedMetrics, nil
}

func (c *Collector) confirmPendingCommitmentsIfNecessary(serviceType db.ServiceType, resourceName liquid.ResourceName) error {
	behavior := c.Cluster.CommitmentBehaviorForResource(serviceType, resourceName).ForCluster()
	resInfo := c.Cluster.InfoForResource(serviceType, resourceName)
	now := c.MeasureTime()

	// do not run ConfirmPendingCommitments if commitments are not enabled (or not live yet) for this resource
	if len(behavior.Durations) == 0 || !behavior.CanConfirmCommitmentsAt(now) {
		return nil
	}

	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	committableAZs := c.Cluster.Config.AvailabilityZones
	if resInfo.Topology == liquid.FlatTopology {
		committableAZs = []liquid.AvailabilityZone{liquid.AvailabilityZoneAny}
	}
	for _, az := range committableAZs {
		loc := core.AZResourceLocation{
			ServiceType:      serviceType,
			ResourceName:     resourceName,
			AvailabilityZone: az,
		}
		mails, err := datamodel.ConfirmPendingCommitments(loc, c.Cluster, tx, now)
		if err != nil {
			return err
		}

		for _, mail := range mails {
			err := tx.Insert(&mail)
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func renderListToJSON[T any](attribute string, entries []T) (string, error) {
	if len(entries) == 0 {
		return "", nil
	}
	buf, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("could not convert %s to JSON: %w", attribute, err)
	}
	return string(buf), nil
}
