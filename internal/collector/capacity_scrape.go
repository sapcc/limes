// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	. "github.com/majewsky/gg/option"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/audit"
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

// CapacityScrapeJob is a jobloop.Job. Each task scrapes one Liquid, equal to one Service entry.
// Resources and AZResources are managed indirectly by this job, because a bump of the InfoVersion
// on Liquid side causes a reconciliation against the DB. Extraneous Services are only deleted on startup
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
	Service db.Service
	Timing  TaskTiming
}

var (
	// find the next service that needs to have capacity scraped
	findServiceForScrapeQuery = sqlext.SimplifyWhitespace(`
		SELECT * FROM services
		-- filter by need to be updated
		WHERE next_scrape_at <= $1
		-- order by update priority (first schedule, then ID for deterministic test behavior)
		ORDER BY next_scrape_at ASC, id ASC
		-- find only one service to scrape per iteration
		LIMIT 1
	`)

	// This query updates `project_commitments.status` on all rows that have not
	// reached one of the final statuses ("superseded" and "expired").
	//
	// The result of this computation is used in all bulk queries on
	// project_commitments to replace lengthy and time-dependent conditions with
	// simple checks on the enum value in `status`.
	//
	// When moving to expired or superseded state, the transfer status, token and time are cleared.
	updateProjectCommitmentStatusForResourceQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		UPDATE project_commitments
		   SET status = CASE WHEN superseded_at IS NOT NULL THEN {{liquid.CommitmentStatusSuperseded}}
		                     WHEN expires_at <= $3          THEN {{liquid.CommitmentStatusExpired}}
		                     WHEN confirm_by > $3           THEN {{liquid.CommitmentStatusPlanned}}
		                     WHEN confirmed_at IS NULL      THEN {{liquid.CommitmentStatusPending}}
		                     ELSE {{liquid.CommitmentStatusConfirmed}} END,
		       transfer_status = CASE WHEN superseded_at IS NOT NULL OR expires_at <= $3 THEN {{limesresources.CommitmentTransferStatusNone}}
		                              ELSE transfer_status END,
		       transfer_token = CASE WHEN superseded_at IS NOT NULL OR expires_at <= $3 THEN NULL
		                             ELSE transfer_token END,
		       transfer_started_at = CASE WHEN superseded_at IS NOT NULL OR expires_at <= $3 THEN NULL
		                                  ELSE transfer_started_at END
		WHERE status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}) AND az_resource_id IN (
			SELECT azr.id
			  FROM services s
			  JOIN resources r ON r.service_id = s.id
			  JOIN az_resources azr ON azr.resource_id = r.id
			 WHERE s.type = $1 AND r.name = $2
		)
	`))
)

func (c *Collector) discoverCapacityScrapeTask(_ context.Context, _ prometheus.Labels) (task capacityScrapeTask, err error) {
	task.Timing.StartedAt = c.MeasureTime()
	// CheckConsistencyJob will ensure that all services are present in the DB. Before it runs,
	// we might have a service entry without a corresponding LiquidConnection or vise versa.

	err = c.DB.SelectOne(&task.Service, findServiceForScrapeQuery, task.Timing.StartedAt)
	return task, err
}

func (c *Collector) processCapacityScrapeTask(ctx context.Context, task capacityScrapeTask, labels prometheus.Labels) (returnedErr error) {
	service := task.Service
	labels["service_type"] = string(service.Type)

	defer func() {
		if returnedErr != nil {
			returnedErr = fmt.Errorf("while scraping service %s: %w", strconv.FormatInt(int64(service.ID), 10), returnedErr)
		}
	}()

	// if service is not in the LiquidConnections, do nothing
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
	capacityData, srv, serializedMetrics, err := c.scrapeLiquidCapacity(ctx, connection)
	if srv.LiquidVersion > service.LiquidVersion {
		service = srv
	}
	task.Timing.FinishedAt = c.MeasureTimeAtEnd()
	if err == nil {
		service.ScrapedAt = Some(task.Timing.FinishedAt)
		service.ScrapeDurationSecs = task.Timing.Duration().Seconds()
		service.SerializedMetrics = string(serializedMetrics)
		service.NextScrapeAt = task.Timing.FinishedAt.Add(c.AddJitter(capacityScrapeInterval))
		service.ScrapeErrorMessage = ""
		// NOTE: in this case, we continue below, with the resources update
		// the services row will be updated at the end of the tx
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

	// a resources update is not necessary, as it is done within c.scrapeLiquidCapacity if necessary
	// collect existing resources
	var dbResources []db.Resource
	_, err = tx.Select(&dbResources, `SELECT r.* FROM resources as r JOIN services as s ON r.service_id = s.id WHERE s.type = $1`, service.Type)
	if err != nil {
		return fmt.Errorf("cannot inspect existing resources: %w", err)
	}

	// collect az_resources for the resources owned by this capacitor
	dbResourceIDs := make([]any, len(dbResources))
	for idx, res := range dbResources {
		dbResourceIDs[idx] = res.ID
	}
	var dbAZResources []db.AZResource
	whereClause, queryArgs := db.BuildSimpleWhereClause(map[string]any{"resource_id": dbResourceIDs}, 0)
	_, err = tx.Select(&dbAZResources, `SELECT * FROM az_resources WHERE `+whereClause, queryArgs...)
	if err != nil {
		return fmt.Errorf("cannot inspect existing AZ resources: %w", err)
	}
	dbAZResourcesByResourceID := make(map[db.ResourceID][]db.AZResource)
	for _, azRes := range dbAZResources {
		dbAZResourcesByResourceID[azRes.ResourceID] = append(dbAZResourcesByResourceID[azRes.ResourceID], azRes)
	}

	serviceInfos, err := c.Cluster.AllServiceInfos()
	if err != nil {
		return err
	}
	serviceInfo := core.InfoForService(serviceInfos, service.Type)

	// az_resources should be there - enumerate the data an complain if they don't match (with exceptions)
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
			// az=unknown and az=any do not have to exist
			// specific AZs do not need capacity when az=any has capacity (sum should be correct)
			if !azResExists && !slices.Contains([]liquid.AvailabilityZone{liquid.AvailabilityZoneAny, liquid.AvailabilityZoneUnknown}, azRes.AvailabilityZone) && resourceTopology != liquid.FlatTopology && !anyAZexists {
				logg.Error("could not find AZ resource %s/%s in capacity data of %s, either version was not bumped correctly or capacity configuration is incomplete", azRes.AvailabilityZone, res.Name, service.Type)
			}
			// the unknown AZ is the only one which can vanish from the report, we treat this as capacity=0 and usage=NULL
			if !azResExists && azRes.AvailabilityZone == liquid.AvailabilityZoneUnknown {
				azResExists = true
				azResourceData = &liquid.AZResourceCapacityReport{}
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

	// for all resources thus updated, sync commitment status with reality
	for _, res := range dbResources {
		now := c.MeasureTime()
		_, err := c.DB.Exec(updateProjectCommitmentStatusForResourceQuery, service.Type, res.Name, now)
		if err != nil {
			return fmt.Errorf("while updating project_commitments.status for %s/%s: %w", service.Type, res.Name, err)
		}
	}

	// for all resources thus updated, try to confirm pending commitments
	for _, res := range dbResources {
		err := c.confirmPendingCommitmentsIfNecessary(service.Type, res.Name, serviceInfos)
		if err != nil {
			return err
		}
	}

	// for all resources thus updated, recompute project quotas if necessary
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

func (c *Collector) scrapeLiquidCapacity(ctx context.Context, connection *core.LiquidConnection) (capacityData liquid.ServiceCapacityReport, srv db.Service, serializedMetrics []byte, err error) {
	capacityData, srv, err = connection.ScrapeCapacity(ctx, datamodel.NewCapacityScrapeBackchannel(c.Cluster, c.DB), c.Cluster.Config.AvailabilityZones)
	if err != nil {
		return liquid.ServiceCapacityReport{}, srv, nil, err
	}
	serializedMetrics, err = liquidSerializeMetrics(connection.ServiceInfo().CapacityMetricFamilies, capacityData.Metrics)
	if err != nil {
		return liquid.ServiceCapacityReport{}, srv, nil, err
	}
	return capacityData, srv, serializedMetrics, nil
}

func (c *Collector) confirmPendingCommitmentsIfNecessary(serviceType db.ServiceType, resourceName liquid.ResourceName, serviceInfos map[db.ServiceType]liquid.ServiceInfo) error {
	behavior := c.Cluster.CommitmentBehaviorForResource(serviceType, resourceName).ForCluster()
	serviceInfo := core.InfoForService(serviceInfos, serviceType)
	resInfo := core.InfoForResource(serviceInfo, resourceName)
	now := c.MeasureTime()

	// do not run ConfirmPendingCommitments if commitments are not enabled (or not live yet) for this resource
	canConfirmErrMsg := behavior.CanConfirmCommitmentsAt(now)
	if len(behavior.Durations) == 0 || canConfirmErrMsg != "" {
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
	var auditevents []audittools.Event
	for _, az := range committableAZs {
		loc := core.AZResourceLocation{
			ServiceType:      serviceType,
			ResourceName:     resourceName,
			AvailabilityZone: az,
		}
		auditContext := audit.Context{
			UserIdentity: audit.CollectorUserInfo{
				TaskName: "capacity-scrape",
			},
			Request: audit.CollectorDummyRequest,
		}
		azAuditEvents, err := datamodel.ConfirmPendingCommitments(loc, resInfo.Unit, c.Cluster, tx, now, c.GenerateProjectCommitmentUUID, c.GenerateTransferToken, auditContext)
		if err != nil {
			return err
		}
		auditevents = append(auditevents, azAuditEvents...)
	}
	err = tx.Commit()
	if err != nil {
		return err
	}
	for _, ae := range auditevents {
		c.Auditor.Record(ae)
	}
	return nil
}
