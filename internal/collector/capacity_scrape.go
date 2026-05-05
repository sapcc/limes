// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gophercloudext"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"
	. "go.xyrillian.de/gg/option"

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
	// do not use the db.Service directly, as it might get updated during the scrape operation
	ServiceType db.ServiceType
	Timing      TaskTiming
}

var (
	// find the next service that needs to have capacity scraped
	findServiceForScrapeQuery = sqlext.SimplifyWhitespace(`
		SELECT type FROM services
		-- filter by need to be updated
		WHERE next_scrape_at <= $1
		-- order by update priority (first schedule, then ID for deterministic test behavior)
		ORDER BY next_scrape_at ASC, id ASC
		-- find only one service to scrape per iteration
		LIMIT 1
	`)

	// This query updates `project_commitments.status` on all rows that have not
	// reached one of the final statuses ("superseded", "expired" or "deleted").
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
		WHERE status NOT IN ({{liquid.CommitmentStatusSuperseded}}, {{liquid.CommitmentStatusExpired}}, {{util.CommitmentStatusDeleted}}) AND az_resource_id IN (
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

	str, err := c.DB.SelectStr(findServiceForScrapeQuery, task.Timing.StartedAt)
	if err != nil {
		return task, err
	}
	task.ServiceType = db.ServiceType(str)

	// Defense in depth: Verify that we have a LiquidConnection for the serviceType of this task.
	_, ok := c.Cluster.LiquidConnections[task.ServiceType]
	if !ok {
		return task, fmt.Errorf("no such service type: %q", task.ServiceType)
	}
	// if the above check succeeded, this should never fail because the SIC is updated after the
	// LiquidConnection is initialized.
	_, ok = c.Cluster.SIC.GetServiceForType(task.ServiceType)
	if !ok {
		return task, fmt.Errorf("no data found in ServiceInfoCache for type %s", task.ServiceType)
	}
	return task, err
}

func (c *Collector) processCapacityScrapeTask(ctx context.Context, task capacityScrapeTask, labels prometheus.Labels) (returnedErr error) {
	serviceType := task.ServiceType
	labels["service_type"] = string(task.ServiceType)

	defer func() {
		if returnedErr != nil {
			returnedErr = fmt.Errorf("while scraping service %s: %w", task.ServiceType, returnedErr)
		}
	}()

	// if service is not in the LiquidConnections, do nothing
	connection := c.Cluster.LiquidConnections[serviceType]
	if connection == nil {
		task.Timing.FinishedAt = c.MeasureTimeAtEnd()
		service, _ := c.Cluster.SIC.GetServiceForType(serviceType)
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

	// IMPORTANT: service may have been updated, only load it here
	service, sExists := c.Cluster.SIC.GetServiceForType(serviceType)
	resources, _ := c.Cluster.SIC.GetResourcesForType(serviceType)
	azResources, _ := c.Cluster.SIC.GetAZResourcesForType(serviceType)
	if !sExists { // defense in depth: when we get here, the scrape was successful, so the service should be up to date
		return fmt.Errorf("no data found in ServiceInfoCache for type %s", connection.ServiceType)
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
		err = gophercloudext.UnpackError(err)
		service.NextScrapeAt = task.Timing.FinishedAt.Add(c.AddJitter(capacityScrapeErrorInterval))
		service.ScrapeErrorMessage = err.Error()

		_, updateErr := c.DB.Update(&service)
		if updateErr != nil {
			err = fmt.Errorf("%w (additional error while updating DB: %s", err, updateErr.Error())
		}
		return err
	}

	enrichCapacityReportTotals(&capacityData)

	// do the following in a transaction to avoid inconsistent DB state
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// az_resources should be there - enumerate the data and complain if they don't match (with exceptions)
	for _, res := range resources {
		resourceData, resExists := capacityData.Resources[res.Name]
		if !resExists {
			logg.Error("could not find resource %s in capacity data of %s, either version was not bumped correctly or capacity configuration is incomplete", res.Name, service.Type)
			continue
		}

		_, anyAZexists := resourceData.PerAZ[liquid.AvailabilityZoneAny]
		for _, azRes := range azResources[res.Name] {
			azResourceData, azResExists := resourceData.PerAZ[azRes.AvailabilityZone]
			// az=unknown and az=any do not have to exist
			// specific AZs do not need capacity when az=any has capacity (sum should be correct)
			if !azResExists && !slices.Contains([]liquid.AvailabilityZone{liquid.AvailabilityZoneAny, liquid.AvailabilityZoneUnknown}, azRes.AvailabilityZone) && res.Topology != liquid.FlatTopology && !anyAZexists {
				logg.Error("could not find AZ resource %s/%s in capacity data of %s, either version was not bumped correctly or capacity configuration is incomplete", res.Name, azRes.AvailabilityZone, service.Type)
			}
			// the unknown AZ is the only one which can vanish from the report, we treat this as capacity=0 and usage=NULL
			if !azResExists && azRes.AvailabilityZone == liquid.AvailabilityZoneUnknown {
				azResExists = true
				azResourceData = &liquid.AZResourceCapacityReport{}
			}
			// exit if no data
			if !azResExists {
				continue
			}

			azRes.RawCapacity = azResourceData.Capacity
			if azResourceData.Capacity > 0 && azRes.AvailabilityZone != liquid.AvailabilityZoneTotal {
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
	// the cluster level entities have been updated, we need to refresh the cache now
	err = c.Cluster.SIC.InvalidateService(Some(serviceType))
	if err != nil {
		return err
	}

	// for all resources thus updated, sync commitment status with reality
	for _, res := range resources {
		now := c.MeasureTime()
		_, err = c.DB.Exec(updateProjectCommitmentStatusForResourceQuery, service.Type, res.Name, now)
		if err != nil {
			return fmt.Errorf("while updating project_commitments.status for %s/%s: %w", service.Type, res.Name, err)
		}
	}

	// for all resources thus updated, try to confirm pending commitments
	for _, resource := range resources {
		err := c.confirmPendingCommitmentsIfNecessary(ctx, service, resource)
		if err != nil {
			return err
		}
	}

	// for all resources thus updated, recompute project quotas if necessary
	for _, res := range resources {
		now := c.MeasureTime()
		err := datamodel.ApplyComputedProjectQuota(service.Type, res, c.Cluster, now)
		if err != nil {
			return err
		}
	}

	return nil
}

func (c *Collector) scrapeLiquidCapacity(ctx context.Context, connection *core.LiquidConnection) (capacityData liquid.ServiceCapacityReport, serializedMetrics []byte, err error) {
	capacityData, err = connection.ScrapeCapacity(ctx, datamodel.NewCapacityScrapeBackchannel(c.Cluster, c.DB), c.Cluster.Config.AvailabilityZones)
	if err != nil {
		return liquid.ServiceCapacityReport{}, nil, err
	}
	service, sExists := c.Cluster.SIC.GetServiceForType(connection.ServiceType)
	if !sExists { // defense in depth: when we get here, the scrape was successful, so the service should be up to date
		return capacityData, nil, fmt.Errorf("no data found in ServiceInfoCache for type %s", connection.ServiceType)
	}
	capacityMetricFamilies, err := util.JSONToAny[map[liquid.MetricName]liquid.MetricFamilyInfo](service.CapacityMetricFamiliesJSON, "capacity_metric_families")
	if err != nil {
		return liquid.ServiceCapacityReport{}, nil, err
	}
	serializedMetrics, err = liquidSerializeMetrics(capacityMetricFamilies, capacityData.Metrics)
	if err != nil {
		return liquid.ServiceCapacityReport{}, nil, err
	}
	return capacityData, serializedMetrics, nil
}

func (c *Collector) confirmPendingCommitmentsIfNecessary(ctx context.Context, service db.Service, resource db.Resource) error {
	behavior := c.Cluster.CommitmentBehaviorForResource(service.Type, resource.Name).ForCluster()
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
	if resource.Topology == liquid.FlatTopology {
		committableAZs = []liquid.AvailabilityZone{liquid.AvailabilityZoneAny}
	}
	var auditevents []audittools.Event
	for _, az := range committableAZs {
		loc := core.AZResourceLocation{
			ServiceType:      service.Type,
			ResourceName:     resource.Name,
			AvailabilityZone: az,
		}
		auditContext := audit.Context{
			UserIdentity: audit.CollectorUserInfo{
				TaskName: "capacity-scrape",
			},
			Request: audit.CollectorDummyRequest,
		}
		azAuditEvents, err := datamodel.ConfirmPendingCommitments(ctx, loc, c.Cluster, tx, now, c.GenerateProjectCommitmentUUID, c.GenerateTransferToken, auditContext)
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

func enrichCapacityReportTotals(value *liquid.ServiceCapacityReport) {
	if value == nil || value.Resources == nil {
		return
	}

	for resName, resValue := range value.Resources {
		if len(resValue.PerAZ) == 0 {
			continue
		}

		var total liquid.AZResourceCapacityReport
		for _, azValue := range resValue.PerAZ {
			total.Capacity += azValue.Capacity
			if usage, ok := azValue.Usage.Unpack(); ok {
				total.Usage = Some(total.Usage.UnwrapOr(0) + usage)
			}
		}

		resValue.PerAZ[liquid.AvailabilityZoneTotal] = &total
		value.Resources[resName] = resValue
	}
}
