// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gophercloudext"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/sqlext"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

const (
	// how long to wait before scraping the same project and service again
	ScrapeInterval = 30 * time.Minute
	// how long to wait before re-checking a project service that failed scraping
	RecheckInterval = 5 * time.Minute
)

var (
	// find the next project that needs to have resources scraped
	findProjectForScrapeQuery = sqlext.SimplifyWhitespace(`
		SELECT ps.* FROM project_services ps
		JOIN services s ON ps.service_id = s.id
		-- filter by service type
		WHERE s.type = $1
		-- filter by need to be updated (because of user request, or because of scheduled scrape)
		AND (ps.stale OR ps.next_scrape_at <= $2)
		-- order by update priority (first user-requested scrapes, then scheduled scrapes, then ID for deterministic test behavior)
		ORDER BY ps.stale DESC, ps.next_scrape_at ASC, ps.id ASC
		-- find only one project to scrape per iteration
		LIMIT 1
	`)

	writeScrapeSuccessQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_services SET
			-- timing information
			checked_at = $1, scraped_at = $1, next_scrape_at = $2, scrape_duration_secs = $3,
			-- serialized state returned by LiquidConnection
			serialized_metrics = $4, serialized_scrape_state = $5,
			-- other
			stale = FALSE, scrape_error_message = ''
		WHERE id = $6
	`)

	writeScrapeErrorQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_services SET
			-- timing information
			checked_at = $1, next_scrape_at = $2,
			-- other
			stale = FALSE, scrape_error_message = $3
		WHERE id = $4
	`)
)

// ScrapeJob looks at one specific project service per task, collects
// quota and usage information from the backend service as well as checks the
// database for outdated or missing rate records for the given service.
// The backend quota is adjusted if it differs from the desired values
// and rate records are updated by querying the backend service.
//
// This job is not ConcurrencySafe, but multiple instances can safely be run in
// parallel if they act on separate service types. The job can only be run if
// a target service type is specified using the
// `jobloop.WithLabel("service_type", serviceType)` option.
func (c *Collector) ScrapeJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.ProducerConsumerJob[projectScrapeTask]{
		Metadata: jobloop.JobMetadata{
			ReadableName: "scrape project quota, usage and rate usage",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_scrapes",
				Help: "Counter for scrape operations per Keystone project.",
			},
			CounterLabels: []string{"service_type"},
		},
		DiscoverTask: c.discoverScrapeTask,
		ProcessTask:  c.processScrapeTask,
	}).Setup(registerer)
}

// This is the task type for ScrapeJob. The natural
// task type for this job is just db.ProjectService, but this more elaborate
// task type allows us to reuse timing information from the discover step.
type projectScrapeTask struct {
	// data loaded during discoverScrapeTask
	ProjectService db.ProjectService
	// timing information
	Timing TaskTiming
	// error reporting
	Err error
}

func (c *Collector) discoverScrapeTask(_ context.Context, labels prometheus.Labels) (task projectScrapeTask, err error) {
	serviceType := db.ServiceType(labels["service_type"])
	task.Timing.StartedAt = c.MeasureTime()

	// Defense in depth: Verify that we have a LiquidConnection for the serviceType of this task.
	_, ok := c.Cluster.LiquidConnections[serviceType]
	if !ok {
		return projectScrapeTask{}, fmt.Errorf("no such service type: %q", serviceType)
	}
	// if the above check succeeded, this should never fail because the SIC is updated after the
	// LiquidConnection is initialized.
	_, ok = c.Cluster.SIC.GetSnapshot().GetServiceForType(serviceType)
	if !ok {
		return projectScrapeTask{}, fmt.Errorf("no data found in ServiceInfoCache for type %s", serviceType)
	}

	err = c.DB.SelectOne(&task.ProjectService, findProjectForScrapeQuery, serviceType, task.Timing.StartedAt)
	if err != nil {
		return projectScrapeTask{}, err
	}
	return task, err
}

func (c *Collector) identifyProjectBeingScraped(srv db.ProjectService) (dbProject db.Project, dbDomain db.Domain, project core.KeystoneProject, err error) {
	err = c.DB.SelectOne(&dbProject, `SELECT * FROM projects WHERE id = $1`, srv.ProjectID)
	if err != nil {
		err = fmt.Errorf("while reading the DB record for project %d: %w", srv.ProjectID, err)
		return
	}
	err = c.DB.SelectOne(&dbDomain, `SELECT * FROM domains WHERE id = $1`, dbProject.DomainID)
	if err != nil {
		err = fmt.Errorf("while reading the DB record for domain %d: %w", dbProject.DomainID, err)
		return
	}
	domain := core.KeystoneDomainFromDB(dbDomain)
	project = core.KeystoneProjectFromDB(dbProject, domain)
	return
}

func (c *Collector) processScrapeTask(ctx context.Context, task projectScrapeTask, labels prometheus.Labels) error {
	serviceType := db.ServiceType(labels["service_type"])
	projectService := task.ProjectService
	connection := c.Cluster.LiquidConnections[serviceType] // NOTE: discoverScrapeTask already verified that this exists

	// collect additional DB records
	dbProject, dbDomain, project, err := c.identifyProjectBeingScraped(projectService)
	if err != nil {
		return err
	}
	logg.Debug("scraping %s resources for %s/%s", serviceType, dbDomain.Name, dbProject.Name)

	// perform scrape
	report, serializedMetrics, sis, err := c.scrapeLiquid(ctx, connection, project, projectService)
	task.Timing.FinishedAt = c.MeasureTimeAtEnd()
	if err != nil {
		task.Err = gophercloudext.UnpackError(err)
		return c.recordScrapeError(task, serviceType, dbProject, dbDomain, project)
	}
	rateData, serializedScrapeState := extractRateData(report)

	// collect additional DB records (it is important to do this step after the
	// scrape, because the scrape might observe a new ServiceInfo version)
	// write resource results
	err = c.writeResourceScrapeResult(task, serviceType, dbProject, report, sis)
	if err != nil {
		return fmt.Errorf("while writing resource results into DB: %w", err)
	}

	// write rate results
	err = c.writeRateScrapeResult(task, serviceType, rateData, sis)
	if err != nil {
		return fmt.Errorf("while writing rate results into DB: %w", err)
	}

	// update scraped_at timestamp and reset the stale flag on this service so
	// that we don't scrape it again immediately afterwards
	_, err = c.DB.Exec(writeScrapeSuccessQuery,
		task.Timing.FinishedAt, task.Timing.FinishedAt.Add(c.AddJitter(ScrapeInterval)), task.Timing.Duration().Seconds(),
		string(serializedMetrics), serializedScrapeState, projectService.ID,
	)
	if err != nil {
		return fmt.Errorf("while updating metadata on project service: %w", err)
	}
	return nil
}

func (c *Collector) recordScrapeError(task projectScrapeTask, serviceType db.ServiceType, dbProject db.Project, dbDomain db.Domain, project core.KeystoneProject) error {
	_, err := c.DB.Exec(
		writeScrapeErrorQuery,
		task.Timing.FinishedAt, task.Timing.FinishedAt.Add(c.AddJitter(RecheckInterval)),
		task.Err.Error(), task.ProjectService.ID,
	)
	if err != nil {
		c.LogError("additional DB error while writing resource scrape error for service %s in project %s: %s",
			serviceType, project.UUID, err.Error(),
		)
	}

	if task.ProjectService.ScrapedAt.IsNone() {
		// see explanation inside the called function's body
		err := c.writeDummyResources(dbProject, serviceType)
		if err != nil {
			c.LogError("additional DB error while writing dummy resources for service %s in project %s: %s",
				serviceType, project.UUID, err.Error(),
			)
		}
	}
	return fmt.Errorf("during scrape of project %s/%s: %w", dbDomain.Name, dbProject.Name, task.Err)
}

func (c *Collector) scrapeLiquid(ctx context.Context, connection *core.LiquidConnection, project core.KeystoneProject, projectService db.ProjectService) (liquid.ServiceUsageReport, []byte, *core.ServiceInfoSnapshot, error) {
	report, sis, err := connection.Scrape(ctx, project, c.Cluster.Config.AvailabilityZones, projectService.SerializedScrapeState)
	if err != nil {
		return liquid.ServiceUsageReport{}, nil, &core.ServiceInfoSnapshot{}, err
	}
	service, sExists := sis.GetServiceForType(connection.ServiceType)
	if !sExists { // defense in depth: this snapshot is taken immediately after saving the ServiceInfo
		return liquid.ServiceUsageReport{}, nil, &core.ServiceInfoSnapshot{}, fmt.Errorf("no data found in ServiceInfoCache for type %s", connection.ServiceType)
	}
	usageMetricFamilies, err := util.JSONToAny[map[liquid.MetricName]liquid.MetricFamilyInfo](service.UsageMetricFamiliesJSON, "usage_metric_families")
	if err != nil {
		return liquid.ServiceUsageReport{}, nil, &core.ServiceInfoSnapshot{}, err
	}
	serializedMetrics, err := liquidSerializeMetrics(usageMetricFamilies, report.Metrics)
	if err != nil {
		return liquid.ServiceUsageReport{}, nil, &core.ServiceInfoSnapshot{}, err
	}
	return report, serializedMetrics, sis, nil
}

func extractRateData(report liquid.ServiceUsageReport) (result map[liquid.RateName]*big.Int, serializedState string) {
	result = make(map[liquid.RateName]*big.Int, len(report.Rates))

	for rateName, rateReport := range report.Rates {
		// TODO: add AZ-awareness for rate usage in Limes
		// (until this is done, we take the sum over all AZs here)
		result[rateName] = &big.Int{}
		for _, azReport := range rateReport.PerAZ {
			if usage, ok := azReport.Usage.Unpack(); ok {
				var x big.Int
				result[rateName] = x.Add(result[rateName], usage)
			}
		}
	}

	return result, string(report.SerializedState)
}

func (c *Collector) writeResourceScrapeResult(task projectScrapeTask, serviceType db.ServiceType, dbProject db.Project, resourceData liquid.ServiceUsageReport, sis *core.ServiceInfoSnapshot) error {
	filteredSIS := sis.Filter(core.ServiceInfoFilter{ServiceType: Some(serviceType)})
	service, sExists := filteredSIS.GetServiceForType(serviceType)
	resources, _ := filteredSIS.GetResourcesForType(serviceType)     // can have no resources
	azResources, _ := filteredSIS.GetAZResourcesForType(serviceType) // can have no az_resources
	if !sExists {                                                    // defense in depth: this snapshot is taken immediately after saving the ServiceInfo
		return fmt.Errorf("no data found in ServiceInfoCache for type %s", serviceType)
	}

	for resName, resData := range resourceData.Resources {
		resource, rExists := filteredSIS.GetResourceForTypeName(serviceType, resName)
		if !rExists {
			return fmt.Errorf("no data found in ServiceInfoCache for %s", db.ResourcePath{ServiceType: serviceType, ResourceName: resName})
		}
		if len(resData.PerAZ) == 0 {
			// ensure that there is at least one ProjectAZResource for each ProjectResource
			resData.PerAZ = liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: 0})
			resourceData.Resources[resName] = resData
		} else {
			// AZ separated resources will not include "any" AZ. The basequota will be distributed towards the existing AZs.
			// If an AZ is not available within the scrape response, it will be created to store the basequota.
			if resource.Topology == liquid.AZSeparatedTopology {
				for _, availabilityZone := range c.Cluster.Config.AvailabilityZones {
					_, exists := resData.PerAZ[availabilityZone]
					if !exists {
						resData.PerAZ[availabilityZone] = &liquid.AZResourceUsageReport{Usage: 0}
					}
				}
			} else {
				// for AZ-aware resources, ensure that we also have a ProjectAZResource in
				// "any", because ApplyComputedProjectQuota needs somewhere to write base
				// quotas into if enabled
				_, exists := resData.PerAZ[liquid.AvailabilityZoneAny]
				if !exists {
					resData.PerAZ[liquid.AvailabilityZoneAny] = &liquid.AZResourceUsageReport{Usage: 0}
				}
			}
		}
	}
	enrichUsageReportTotals(&resourceData, filteredSIS)

	tx, err := c.DB.Begin()
	if err != nil {
		return fmt.Errorf("while beginning transaction: %w", err)
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// we have seen UPDATEs in this transaction getting stuck in the past, this
	// should hopefully prevent this (or at least cause loud complaints when it
	// happens)
	//
	// TODO: consider setting this for the entire connection if it helps
	_, err = tx.Exec(`SET LOCAL idle_in_transaction_session_timeout = 5000`) // 5000 ms = 5 seconds
	if err != nil {
		return fmt.Errorf("while applying idle_in_transaction_session_timeout: %w", err)
	}

	// we only need to ensure existence of project_resources - the values don't impact this operation
	err = datamodel.ProjectResourceUpdate{
		UpdateResource: func(res *db.ProjectResource, resName liquid.ResourceName) error {
			resource, rExists := filteredSIS.GetResourceForTypeName(serviceType, resName)
			if !rExists {
				return fmt.Errorf("no data found in ServiceInfoCache for %s", db.ResourcePath{ServiceType: serviceType, ResourceName: resName})
			}
			if resource.HasQuota {
				res.Forbidden = resourceData.Resources[resName].Forbidden
			}
			return nil
		},
	}.Run(tx, dbProject, filteredSIS)
	if err != nil {
		return err
	}

	// For inserting the project_az_resources, we need to find the project_az_resources availabilityZone
	azResourcesByID := make(map[db.AZResourceID]db.AZResource)
	for _, azResourcesByAZ := range azResources {
		maps.Copy(azResourcesByID, db.BuildIndexOfArray(slices.Collect(maps.Values(azResourcesByAZ)), func(azRes db.AZResource) db.AZResourceID { return azRes.ID }))
	}
	projectAZResourcesByAZResourceID, err := db.BuildIndexOfDBResult(
		tx,
		func(pAZRes db.ProjectAZResource) db.AZResourceID { return pAZRes.AZResourceID },
		`SELECT pazr.* FROM project_az_resources pazr JOIN az_resources azr ON PAZR.az_resource_id = azr.id JOIN resources r ON azr.resource_id = r.id WHERE r.service_id = $1 AND pazr.project_id = $2`,
		service.ID, dbProject.ID,
	)
	if err != nil {
		return err
	}
	resourceNames := slices.Sorted(maps.Keys(resources))

	// update project_az_resources for each resource
	hasBackendQuotaDrift := false
	for _, resourceName := range resourceNames {
		resource := resources[resourceName]
		filteredAZResources := azResources[resourceName]
		usageData := resourceData.Resources[resourceName].PerAZ
		projectAZResources := make([]db.ProjectAZResource, 0, len(filteredAZResources))
		for _, azResource := range filteredAZResources {
			projectAZResources = append(projectAZResources, projectAZResourcesByAZResourceID[azResource.ID])
		}
		wantedKeys := make([]db.AZResourceID, 0, len(usageData))
		for _, az := range slices.Sorted(maps.Keys(usageData)) {
			wantedKeys = append(wantedKeys, filteredAZResources[az].ID)
		}

		setUpdate := db.SetUpdate[db.ProjectAZResource, db.AZResourceID]{
			ExistingRecords: projectAZResources,
			WantedKeys:      wantedKeys,
			KeyForRecord: func(azRes db.ProjectAZResource) db.AZResourceID {
				return azRes.AZResourceID
			},
			Create: func(id db.AZResourceID) (db.ProjectAZResource, error) {
				return db.ProjectAZResource{
					ProjectID:    dbProject.ID,
					AZResourceID: id,
				}, nil
			},
			Update: func(azRes *db.ProjectAZResource) (err error) {
				az := azResourcesByID[azRes.AZResourceID].AvailabilityZone
				data := usageData[az]
				azRes.Usage = data.Usage
				azRes.PhysicalUsage = data.PhysicalUsage

				// for the quota values, we want to
				// a) reset both to None when HasQuota is false
				// b) set a default quota of 0 if not set previously
				// c) set backendQuota for the applicable cases according to topology, otherwise set None (important for topology switch)
				// d) check for backendQuota drift
				if !resource.HasQuota {
					azRes.BackendQuota = None[int64]()
					azRes.Quota = None[uint64]()
				} else {
					if datamodel.AZHasQuotaForTopology(resource.Topology, az) && azRes.Quota.IsNone() {
						azRes.Quota = Some[uint64](0)
					}
					if datamodel.AZHasBackendQuotaForTopology(resource.Topology, az) {
						azRes.BackendQuota = data.Quota
					} else {
						azRes.BackendQuota = None[int64]()
					}
					if datamodel.AZHasBackendQuotaForTopology(resource.Topology, az) {
						// check if we need to arrange for SetQuotaJob to look at this project service
						backendQuota := azRes.BackendQuota.UnwrapOr(-1)
						quota := azRes.Quota.UnwrapOr(0)
						if backendQuota < 0 || uint64(backendQuota) != quota {
							hasBackendQuotaDrift = true
						}
					}
				}

				// warn when the backend is inconsistent with itself
				if data.Subresources != nil && uint64(len(data.Subresources)) != data.Usage {
					logg.Info("resource quantity mismatch in project %s, resource %s/%s, AZ %s: usage = %d, but found %d subresources",
						dbProject.UUID, service.Type, resourceName, az,
						data.Usage, len(data.Subresources),
					)
				}

				azRes.SubresourcesJSON, err = util.RenderListToJSON("subresources", data.Subresources)
				if err != nil {
					return err
				}

				// track historical usage if required (only required for AutogrowQuotaDistribution)
				autogrowCfg, ok := c.Cluster.QuotaDistributionConfigForResource(service.Type, resourceName).Autogrow.Unpack()
				if ok {
					ts, err := util.ParseTimeSeries[uint64](azRes.HistoricalUsageJSON)
					if err != nil {
						return fmt.Errorf("while parsing historical_usage for AZ %s: %w", az, err)
					}
					err = ts.AddMeasurement(task.Timing.FinishedAt, data.Usage)
					if err != nil {
						return fmt.Errorf("while tracking historical_usage for AZ %s: %w", az, err)
					}
					ts.PruneOldValues(task.Timing.FinishedAt, autogrowCfg.UsageDataRetentionPeriod.Into())
					azRes.HistoricalUsageJSON, err = ts.Serialize()
					if err != nil {
						return fmt.Errorf("while serializing historical_usage for AZ %s: %w", az, err)
					}
				} else {
					azRes.HistoricalUsageJSON = ""
				}

				return nil
			},
		}
		_, err := setUpdate.Execute(tx)
		if err != nil {
			return err
		}
	}
	if hasBackendQuotaDrift {
		query := `UPDATE project_services ps SET quota_desynced_at = $1 WHERE ps.id = $2 AND quota_desynced_at IS NULL`
		_, err := tx.Exec(query, c.MeasureTime(), task.ProjectService.ID)
		if err != nil {
			return fmt.Errorf("while scheduling backend sync for %s quotas: %w", serviceType, err)
		}
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("while committing transaction: %w", err)
	}

	if task.Timing.Duration() > 5*time.Minute {
		logg.Info("scrape of %s in project %s has taken excessively long (%s)", service.Type, dbProject.UUID, task.Timing.Duration().String())
	}

	return nil
}

func (c *Collector) writeRateScrapeResult(task projectScrapeTask, serviceType db.ServiceType, rateData map[liquid.RateName]*big.Int, sis *core.ServiceInfoSnapshot) error {
	projectService := task.ProjectService
	service, sExists := sis.GetServiceForType(serviceType)
	if !sExists { // defense in depth: this snapshot is taken immediately after saving the ServiceInfo
		return fmt.Errorf("no data found in ServiceInfoCache for type %s", serviceType)
	}
	rates, _ := sis.GetRatesForType(serviceType) // can have no rates

	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// For updating project_rates, we need to find the project_rates' rate_name
	ratesByID := db.BuildIndexOfArray(slices.Collect(maps.Values(rates)), func(rate db.Rate) db.RateID { return rate.ID })

	// update existing project_rates entries
	rateExists := make(map[liquid.RateName]bool)
	var projectRates []db.ProjectRate
	_, err = tx.Select(&projectRates, `SELECT pra.* FROM project_rates pra JOIN rates ra ON pra.rate_id = ra.id WHERE ra.service_id = $1 AND pra.project_id = $2 ORDER BY ra.name`, service.ID, projectService.ProjectID)
	if err != nil {
		return err
	}

	if len(projectRates) > 0 {
		stmt, err := tx.Prepare(`UPDATE project_rates SET usage_as_bigint = $1 WHERE id = $2`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, projectRate := range projectRates {
			rateName := ratesByID[projectRate.RateID].Name
			rateExists[rateName] = true

			usageAsBigint := ""
			if usage, exists := rateData[rateName]; exists {
				usageAsBigint = usage.String()
			}
			if usageAsBigint != projectRate.UsageAsBigint {
				_, err := stmt.Exec(usageAsBigint, projectRate.ID)
				if err != nil {
					return err
				}
			}
		}
	}

	// insert missing project_rates entries
	for _, rateName := range slices.Sorted(maps.Keys(rates)) {
		rate := rates[rateName]
		if _, exists := rateExists[rateName]; exists {
			continue
		}

		usageAsBigint := ""
		if usage, exists := rateData[rateName]; exists {
			usageAsBigint = usage.String()
		}

		err = tx.Insert(&db.ProjectRate{
			ProjectID:     projectService.ProjectID,
			RateID:        rate.ID,
			UsageAsBigint: usageAsBigint,
		})
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (c *Collector) writeDummyResources(dbProject db.Project, serviceType db.ServiceType) error {
	// Rationale: This is called when we first try to scrape a project service,
	// and the scraping fails (most likely due to some internal error in the
	// backend service). We used to just not touch the database at this point,
	// thus resuming scraping of the same project service in the next loop
	// iteration of c.Scrape(). However, when the backend service is down for some
	// time, this means that project_services in new projects are stuck without
	// project_resources, which is an unexpected state that confuses the API.
	//
	// To avoid this situation, this method creates dummy project_resources for an
	// unscrapable project_service. Also, scraped_at is set to 0 (i.e. 1970-01-01
	// 00:00:00 UTC) to make the scraper come back to it after dealing with all
	// new and stale project_services.
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)
	sis := c.Cluster.SIC.GetSnapshot()
	filteredSIS := sis.Filter(core.ServiceInfoFilter{ServiceType: Some(serviceType)})
	service, sExists := sis.GetServiceForType(serviceType)
	resources, _ := sis.GetResourcesForType(serviceType)     // can have no resources
	azResources, _ := sis.GetAZResourcesForType(serviceType) // can have no az_resources
	if !sExists {                                            // defense in depth: when we get here, the scrape never happened, so the sic must be up to date
		return fmt.Errorf("no data found in ServiceInfoCache for type %s", serviceType)
	}

	// create all project_resources, but do not set any particular values
	err = datamodel.ProjectResourceUpdate{
		UpdateResource: func(res *db.ProjectResource, resName liquid.ResourceName) error {
			// until we know better, we will assume Forbidden = true to ensure that
			// quota does not get distributed into projects that cannot accept it
			resource, _ := filteredSIS.GetResourceForTypeName(serviceType, resName)
			if resource.HasQuota {
				res.Forbidden = true
			}
			return nil
		},
	}.Run(tx, dbProject, filteredSIS)
	if err != nil {
		return err
	}

	for _, resName := range slices.Sorted(maps.Keys(azResources)) {
		resByAZ := azResources[resName]
		resource := resources[resName]
		for _, az := range slices.Sorted(maps.Keys(resByAZ)) {
			azResource := resByAZ[az]
			if az == limes.AvailabilityZoneUnknown {
				// we don't write dummy entries for unknown - this should just exist, when there is real usage
				continue
			}
			//  this replicates the logic from writeResourceScrapeResult with the infinite backendQuota (-1)
			backendQuota := None[int64]()
			quota := None[uint64]()
			if resource.HasQuota && datamodel.AZHasBackendQuotaForTopology(resource.Topology, az) {
				backendQuota = Some(int64(-1))
			}
			if resource.HasQuota && datamodel.AZHasQuotaForTopology(resource.Topology, az) {
				quota = Some[uint64](0)
			}
			err := tx.Insert(&db.ProjectAZResource{
				ProjectID:    dbProject.ID,
				AZResourceID: azResource.ID,
				Usage:        0,
				BackendQuota: backendQuota,
				Quota:        quota,
			})
			if err != nil {
				return err
			}
		}
	}

	// TODO: Do we still want to find a way to make the datamodel work without dummy resources?
	// with the total-AZ, we are kind of getting away from this desire, so at some point, we should discuss this again.

	// update scraped_at timestamp and reset stale flag to make sure that we do
	// not scrape this service again immediately afterwards if there are other
	// stale services to cover first
	dummyScrapedAt := time.Unix(0, 0).UTC()
	_, err = tx.Exec(
		`UPDATE project_services
			SET scraped_at = $1, scrape_duration_secs = $2, stale = $3, quota_desynced_at = NULL
			WHERE service_id = $4 AND project_id = $5`,
		dummyScrapedAt, 0.0, false, service.ID, dbProject.ID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func enrichUsageReportTotals(value *liquid.ServiceUsageReport, filteredSIS *core.FilteredServiceInfoSnapshot) {
	if value == nil || value.Resources == nil {
		return
	}
	service := must.BeOK(filteredSIS.GetFilteredService())

	for resName, resValue := range value.Resources {
		if len(resValue.PerAZ) == 0 {
			continue
		}

		// we use the values of resource which default to false
		resource, _ := filteredSIS.GetResourceForTypeName(service.Type, resName)
		var total liquid.AZResourceUsageReport
		for _, azValue := range resValue.PerAZ {
			total.Usage += azValue.Usage
			if physicalUsage, ok := azValue.PhysicalUsage.Unpack(); ok && physicalUsage > 0 {
				total.PhysicalUsage = Some(total.PhysicalUsage.UnwrapOr(0) + physicalUsage)
			}
			// defense in depth: the report from the liquid should be consistent with the topology
			if quota, ok := azValue.Quota.Unpack(); ok && resource.HasQuota && resource.Topology == liquid.AZSeparatedTopology {
				total.Quota = Some(total.Quota.UnwrapOr(0) + quota)
			}
		}
		// if we have a non-az-separated resource with quota, we take the total that the report provides instead of the sum
		// defense in depth: the report from the liquid should be consistent with the topology
		if resource.HasQuota && resource.Topology != liquid.AZSeparatedTopology {
			total.Quota = resValue.Quota
		}

		resValue.PerAZ[liquid.AvailabilityZoneTotal] = &total
		value.Resources[resName] = resValue
	}
}
