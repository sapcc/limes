// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"fmt"
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
// ClusterResources and ClusterAZResources are managed indirectly by this job, because a bump of the InfoVersion
// on Liquid side causes a reconciliation against the DB. Extraneous ClusterServices are only deleted on startup
// of the Collector, by Cluster.Connect.
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
		DiscoverTask: c.discoverCapacityScrapeTask,
		ProcessTask:  c.processCapacityScrapeTask,
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
		// NOTE: in this case, we continue below, with the cluster_resources update
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

	// a cluster_resources update is not necessary, as it is done within c.scrapeLiquidCapacity if necessary
	// collect existing cluster_resources
	var dbResources []db.ClusterResource
	_, err = tx.Select(&dbResources, `SELECT cr.* FROM cluster_resources as cr JOIN cluster_services as cs ON cr.service_id = cs.id WHERE cs.type = $1`, service.Type)
	if err != nil {
		return fmt.Errorf("cannot inspect existing cluster resources: %w", err)
	}

	// collect cluster_az_resources for the cluster_resources owned by this capacitor
	dbResourceIDs := make([]any, len(dbResources))
	for idx, res := range dbResources {
		dbResourceIDs[idx] = res.ID
	}
	var dbAZResources []db.ClusterAZResource
	whereClause, queryArgs := db.BuildSimpleWhereClause(map[string]any{"resource_id": dbResourceIDs}, 0)
	_, err = tx.Select(&dbAZResources, `SELECT * FROM cluster_az_resources WHERE `+whereClause, queryArgs...)
	if err != nil {
		return fmt.Errorf("cannot inspect existing cluster AZ resources: %w", err)
	}
	dbAZResourcesByResourceID := make(map[db.ClusterResourceID][]db.ClusterAZResource)
	for _, azRes := range dbAZResources {
		dbAZResourcesByResourceID[azRes.ResourceID] = append(dbAZResourcesByResourceID[azRes.ResourceID], azRes)
	}

	serviceInfos, err := c.Cluster.AllServiceInfos()
	if err != nil {
		return err
	}
	serviceInfo := core.InfoForService(serviceInfos, service.Type)

	// cluster_az_resources should be there - enumerate the data an complain if they don't match (with exceptions)
	for _, res := range dbResources {
		resourceData, resExists := capacityData.Resources[res.Name]
		if !resExists {
			logg.Error("could not find resource %s in capacity data of %s, either version was not bumped correctly or capacity configuration is incomplete", res.Name, service.Type)
			continue
		}

		resourceTopology := core.InfoForResource(serviceInfo, res.Name).Topology
		_, anyAZexists := resourceData.PerAZ[liquid.AvailabilityZoneAny]
		for _, azRes := range dbAZResourcesByResourceID[res.ID] {
			azResourceData, azResExists := resourceData.PerAZ[azRes.AvailabilityZone]
			// az=unknown does not have to exist
			// specific AZs do not need capacity when az=any has capacity (sum should be correct)
			if !azResExists && azRes.AvailabilityZone != liquid.AvailabilityZoneUnknown && resourceTopology != liquid.FlatTopology && !anyAZexists {
				logg.Error("could not find AZ resource %s/%s in capacity data of %s, either version was not bumped correctly or capacity configuration is incomplete", azRes.AvailabilityZone, res.Name, service.Type)
			}
			if !azResExists {
				continue
			}
			azRes.RawCapacity = azResourceData.Capacity
			if azResourceData.Capacity > 0 {
				azRes.LastNonzeroRawCapacity = Some(azResourceData.Capacity)
			}
			azRes.Usage = azResourceData.Usage
			azRes.SubcapacitiesJSON, err = util.RenderListToJSON("subcapacities", azResourceData.Subcapacities)
			if err != nil {
				return err
			}
			_, err := tx.Update(&azRes)
			if err != nil {
				return err
			}
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
	for _, res := range dbResources {
		now := c.MeasureTime()
		_, err := c.DB.Exec(updateProjectCommitmentStatesForResourceQuery, service.Type, res.Name, now)
		if err != nil {
			return fmt.Errorf("while updating project_commitments.state for %s/%s: %w", service.Type, res.Name, err)
		}
	}

	// for all cluster resources thus updated, try to confirm pending commitments
	for _, res := range dbResources {
		err := c.confirmPendingCommitmentsIfNecessary(service.Type, res.Name, serviceInfos)
		if err != nil {
			return err
		}
	}

	// for all cluster resources thus updated, recompute project quotas if necessary
	for _, res := range dbResources {
		now := c.MeasureTime()
		serviceInfo := core.InfoForService(serviceInfos, service.Type)
		resInfo := core.InfoForResource(serviceInfo, res.Name)
		err := datamodel.ApplyComputedProjectQuota(service.Type, res.Name, resInfo, c.Cluster, now)
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
	serializedMetrics, err := liquidSerializeMetrics(connection.ServiceInfo().CapacityMetricFamilies, capacityData.Metrics)
	if err != nil {
		return liquid.ServiceCapacityReport{}, nil, err
	}
	return capacityData, serializedMetrics, nil
}

func (c *Collector) confirmPendingCommitmentsIfNecessary(serviceType db.ServiceType, resourceName liquid.ResourceName, serviceInfos map[db.ServiceType]liquid.ServiceInfo) error {
	behavior := c.Cluster.CommitmentBehaviorForResource(serviceType, resourceName).ForCluster()
	serviceInfo := core.InfoForService(serviceInfos, serviceType)
	resInfo := core.InfoForResource(serviceInfo, resourceName)
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
