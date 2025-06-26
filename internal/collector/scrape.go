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
	// how long to wait before scraping the same project and service again
	scrapeInterval = 30 * time.Minute
	// how long to wait before re-checking a project service that failed scraping
	recheckInterval = 5 * time.Minute
)

var (
	// find the next project that needs to have resources scraped
	findProjectForScrapeQuery = sqlext.SimplifyWhitespace(`
		SELECT * FROM project_services
		-- filter by service type
		WHERE type = $1
		-- filter by need to be updated (because of user request, or because of scheduled scrape)
		AND (stale OR next_scrape_at <= $2)
		-- order by update priority (first user-requested scrapes, then scheduled scrapes, then ID for deterministic test behavior)
		ORDER BY stale DESC, next_scrape_at ASC, id ASC
		-- find only one project to scrape per iteration
		LIMIT 1
	`)

	findProjectAZResourcesForServiceQuery = sqlext.SimplifyWhitespace(`
		SELECT par.* FROM project_az_resources par
		JOIN project_resources pr ON par.resource_id = pr.id
		WHERE pr.service_id = $1
	`)

	writeScrapeSuccessQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_services SET
			-- timing information
			checked_at = $1, scraped_at = $1, next_scrape_at = $2, scrape_duration_secs = $3,
			-- serialized state returned by LiquidConnection
			serialized_metrics = $4, rates_scrape_state = $5,
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
	Service db.ProjectService
	// timing information
	Timing TaskTiming
	// error reporting
	Err error
}

func (c *Collector) discoverScrapeTask(_ context.Context, labels prometheus.Labels) (task projectScrapeTask, err error) {
	serviceType := db.ServiceType(labels["service_type"])

	maybeServiceInfo, err := c.Cluster.InfoForService(serviceType)
	if err != nil {
		return projectScrapeTask{}, err
	}
	_, ok := maybeServiceInfo.Unpack()

	if !ok {
		return projectScrapeTask{}, fmt.Errorf("no such service type: %q", serviceType)
	}

	task.Timing.StartedAt = c.MeasureTime()
	err = c.DB.SelectOne(&task.Service, findProjectForScrapeQuery, serviceType, task.Timing.StartedAt)
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
	srv := task.Service
	connection := c.Cluster.LiquidConnections[srv.Type] // NOTE: discoverScrapeTask already verified that this exists

	// collect additional DB records
	dbProject, dbDomain, project, err := c.identifyProjectBeingScraped(srv)
	if err != nil {
		return err
	}
	logg.Debug("scraping %s resources for %s/%s", srv.Type, dbDomain.Name, dbProject.Name)

	maybeServiceInfo, err := c.Cluster.InfoForService(srv.Type)
	if err != nil {
		task.Err = fmt.Errorf("while getting ServiceInfos: %w", err)
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		task.Err = fmt.Errorf("no such service type: %q", srv.Type)
	}

	// perform resource scrape
	resourceData, serializedMetrics, err := c.scrapeLiquid(ctx, connection, project)
	if err != nil {
		task.Timing.FinishedAt = c.MeasureTimeAtEnd()
		task.Err = util.UnpackError(err)
		return c.recordScrapeError(task, srv, dbProject, dbDomain, project, serviceInfo)
	}

	// perform rate scrape
	rateData, ratesScrapeState, err := connection.ScrapeRates(ctx, project, c.Cluster.Config.AvailabilityZones, srv.RatesScrapeState)
	task.Timing.FinishedAt = c.MeasureTimeAtEnd()
	if err != nil {
		task.Err = util.UnpackError(err)
		return c.recordScrapeError(task, srv, dbProject, dbDomain, project, serviceInfo)
	}

	// write resource results
	err = c.writeResourceScrapeResult(dbDomain, dbProject, task, resourceData, serviceInfo)
	if err != nil {
		return fmt.Errorf("while writing resource results into DB: %w", err)
	}

	// write rate results
	err = c.writeRateScrapeResult(task, rateData)
	if err != nil {
		return fmt.Errorf("while writing rate results into DB: %w", err)
	}

	// update scraped_at timestamp and reset the stale flag on this service so
	// that we don't scrape it again immediately afterwards
	_, err = c.DB.Exec(writeScrapeSuccessQuery,
		task.Timing.FinishedAt, task.Timing.FinishedAt.Add(c.AddJitter(scrapeInterval)), task.Timing.Duration().Seconds(),
		string(serializedMetrics), ratesScrapeState, srv.ID,
	)
	if err != nil {
		return fmt.Errorf("while updating metadata on project service: %w", err)
	}
	return nil
}

func (c *Collector) recordScrapeError(task projectScrapeTask, srv db.ProjectService, dbProject db.Project, dbDomain db.Domain, project core.KeystoneProject, serviceInfo liquid.ServiceInfo) error {
	_, err := c.DB.Exec(
		writeScrapeErrorQuery,
		task.Timing.FinishedAt, task.Timing.FinishedAt.Add(c.AddJitter(recheckInterval)),
		task.Err.Error(), srv.ID,
	)
	if err != nil {
		c.LogError("additional DB error while writing resource scrape error for service %s in project %s: %s",
			srv.Type, project.UUID, err.Error(),
		)
	}

	if srv.ScrapedAt.IsNone() {
		// see explanation inside the called function's body
		err := c.writeDummyResources(dbDomain, dbProject, srv.Ref(), serviceInfo)
		if err != nil {
			c.LogError("additional DB error while writing dummy resources for service %s in project %s: %s",
				srv.Type, project.UUID, err.Error(),
			)
		}
	}
	return fmt.Errorf("during scrape of project %s/%s: %w", dbDomain.Name, dbProject.Name, task.Err)
}

func (c *Collector) scrapeLiquid(ctx context.Context, connection *core.LiquidConnection, project core.KeystoneProject) (liquid.ServiceUsageReport, []byte, error) {
	resourceData, err := connection.Scrape(ctx, project, c.Cluster.Config.AvailabilityZones)
	if err != nil {
		return liquid.ServiceUsageReport{}, nil, err
	}
	serializedMetrics, err := liquidSerializeMetrics(connection.ServiceInfo().UsageMetricFamilies, resourceData.Metrics)
	if err != nil {
		return liquid.ServiceUsageReport{}, nil, err
	}
	return resourceData, serializedMetrics, nil
}

func (c *Collector) writeResourceScrapeResult(dbDomain db.Domain, dbProject db.Project, task projectScrapeTask, resourceData liquid.ServiceUsageReport, serviceInfo liquid.ServiceInfo) error {
	srv := task.Service

	for resName, resData := range resourceData.Resources {
		resInfo := core.InfoForResource(serviceInfo, resName)
		if len(resData.PerAZ) == 0 {
			// ensure that there is at least one ProjectAZResource for each ProjectResource
			resData.PerAZ = liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: 0})
			resourceData.Resources[resName] = resData
		} else {
			// AZ separated resources will not include "any" AZ. The basequota will be distributed towards the existing AZs.
			// If an AZ is not available within the scrape response, it will be created to store the basequota.
			if resInfo.Topology == liquid.AZSeparatedTopology {
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

	// this is the callback that ProjectResourceUpdate will use to write the scraped data into the project_resources
	updateResource := func(res *db.ProjectResource) error {
		backendQuota := resourceData.Resources[res.Name].Quota

		resInfo := core.InfoForResource(serviceInfo, res.Name)
		if resInfo.HasQuota {
			if resInfo.Topology != liquid.AZSeparatedTopology {
				res.BackendQuota = backendQuota
			}
			res.Forbidden = resourceData.Resources[res.Name].Forbidden
		}

		return nil
	}

	// update project_resources using the action callback from above
	dbResources, err := datamodel.ProjectResourceUpdate{
		UpdateResource: updateResource,
		LogError:       c.LogError,
	}.Run(tx, serviceInfo, c.MeasureTime(), dbDomain, dbProject, srv.Ref())
	if err != nil {
		return err
	}

	// list existing project_az_resources
	var dbAZResources []db.ProjectAZResource
	_, err = tx.Select(&dbAZResources, findProjectAZResourcesForServiceQuery, srv.ID)
	if err != nil {
		return fmt.Errorf("while reading existing project AZ resources: %w", err)
	}
	dbAZResourcesByResourceID := make(map[db.ProjectResourceID][]db.ProjectAZResource, len(dbResources))
	for _, azRes := range dbAZResources {
		dbAZResourcesByResourceID[azRes.ResourceID] = append(dbAZResourcesByResourceID[azRes.ResourceID], azRes)
	}
	allResourceNames := make([]liquid.ResourceName, len(dbResources))
	dbResourcesByName := make(map[liquid.ResourceName]db.ProjectResource, len(dbResources))
	for idx, res := range dbResources {
		allResourceNames[idx] = res.Name
		dbResourcesByName[res.Name] = res
	}
	slices.Sort(allResourceNames) // for deterministic test behavior

	// update project_az_resources for each resource
	for _, resourceName := range allResourceNames {
		res := dbResourcesByName[resourceName]
		usageData := resourceData.Resources[resourceName].PerAZ

		setUpdate := db.SetUpdate[db.ProjectAZResource, liquid.AvailabilityZone]{
			ExistingRecords: dbAZResourcesByResourceID[res.ID],
			WantedKeys:      slices.Sorted(maps.Keys(usageData)),
			KeyForRecord: func(azRes db.ProjectAZResource) liquid.AvailabilityZone {
				return azRes.AvailabilityZone
			},
			Create: func(az liquid.AvailabilityZone) (db.ProjectAZResource, error) {
				return db.ProjectAZResource{
					ResourceID:       res.ID,
					AvailabilityZone: az,
				}, nil
			},
			Update: func(azRes *db.ProjectAZResource) (err error) {
				az := azRes.AvailabilityZone
				data := usageData[az]
				azRes.Usage = data.Usage
				azRes.PhysicalUsage = data.PhysicalUsage

				// set AZ backend quota.
				resInfo := core.InfoForResource(serviceInfo, res.Name)
				if resInfo.Topology == liquid.AZSeparatedTopology && resInfo.HasQuota {
					azRes.BackendQuota = data.Quota
				} else {
					azRes.BackendQuota = None[int64]()
				}

				// warn when the backend is inconsistent with itself
				if data.Subresources != nil && uint64(len(data.Subresources)) != data.Usage {
					logg.Info("resource quantity mismatch in project %s, resource %s/%s, AZ %s: usage = %d, but found %d subresources",
						dbProject.UUID, srv.Type, res.Name, az,
						data.Usage, len(data.Subresources),
					)
				}

				azRes.SubresourcesJSON, err = util.RenderListToJSON("subresources", data.Subresources)
				if err != nil {
					return err
				}

				// track historical usage if required (only required for AutogrowQuotaDistribution)
				autogrowCfg, ok := c.Cluster.QuotaDistributionConfigForResource(srv.Type, res.Name).Autogrow.Unpack()
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

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("while committing transaction: %w", err)
	}

	if task.Timing.Duration() > 5*time.Minute {
		logg.Info("scrape of %s in project %s has taken excessively long (%s)", srv.Type, dbProject.UUID, task.Timing.Duration().String())
	}

	return nil
}

func (c *Collector) writeRateScrapeResult(task projectScrapeTask, rateData map[liquid.RateName]*big.Int) error {
	srv := task.Service
	connection := c.Cluster.LiquidConnections[srv.Type] //NOTE: discoverScrapeTask already verified that this exists

	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// update existing project_rates entries
	rateExists := make(map[liquid.RateName]bool)
	var rates []db.ProjectRate
	_, err = tx.Select(&rates, `SELECT * FROM project_rates WHERE service_id = $1`, srv.ID)
	if err != nil {
		return err
	}

	if len(rates) > 0 {
		stmt, err := tx.Prepare(`UPDATE project_rates SET usage_as_bigint = $1 WHERE service_id = $2 AND name = $3`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, rate := range rates {
			rateExists[rate.Name] = true

			usageData, exists := rateData[rate.Name]
			if !exists {
				if rate.UsageAsBigint != "" {
					c.LogError(
						"could not scrape new data for rate %s in project service %d (was this rate type removed from the scraper connection for %s?)",
						rate.Name, srv.ID, srv.Type,
					)
				}
				continue
			}
			usageAsBigint := usageData.String()
			if usageAsBigint != rate.UsageAsBigint {
				_, err := stmt.Exec(usageAsBigint, srv.ID, rate.Name)
				if err != nil {
					return err
				}
			}
		}
	}

	// insert missing project_rates entries
	for rateName := range connection.ServiceInfo().Rates {
		if _, exists := rateExists[rateName]; exists {
			continue
		}
		usageData := rateData[rateName]

		rate := &db.ProjectRate{
			ServiceID: srv.ID,
			Name:      rateName,
		}
		if usageData != nil {
			rate.UsageAsBigint = usageData.String()
		}

		err = tx.Insert(rate)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (c *Collector) writeDummyResources(dbDomain db.Domain, dbProject db.Project, srv db.ServiceRef[db.ProjectServiceID], serviceInfo liquid.ServiceInfo) error {
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

	// create all project_resources, but do not set any particular values (except
	// that quota overrides are persisted)
	dbResources, err := datamodel.ProjectResourceUpdate{
		UpdateResource: func(res *db.ProjectResource) error {
			resInfo := core.InfoForResource(serviceInfo, res.Name)
			if resInfo.HasQuota && res.BackendQuota.IsNone() {
				res.BackendQuota = Some[int64](-1)
			}
			return nil
		},
		LogError: c.LogError,
	}.Run(tx, serviceInfo, c.MeasureTime(), dbDomain, dbProject, srv)
	if err != nil {
		return err
	}
	// NOTE: We do not do ApplyBackendQuota here: This function is only
	// called after scraping errors, so ApplyBackendQuota will likely fail, too.

	// create dummy project_az_resources
	for _, res := range dbResources {
		err := tx.Insert(&db.ProjectAZResource{
			ResourceID:       res.ID,
			AvailabilityZone: liquid.AvailabilityZoneAny,
			Usage:            0,
		})
		if err != nil {
			return err
		}
	}

	// FIXME: These dummy resources do not conform to `resInfo.Topology` and are never AZ-aware.
	//        I'm not fixing this right now because dummy resources are an extremely rare corner-case anyway.
	// TODO:  When we rework the DB schema next year, we should build it so that dummy resources can be avoided entirely.

	// update scraped_at timestamp and reset stale flag to make sure that we do
	// not scrape this service again immediately afterwards if there are other
	// stale services to cover first
	dummyScrapedAt := time.Unix(0, 0).UTC()
	_, err = tx.Exec(
		`UPDATE project_services SET scraped_at = $1, scrape_duration_secs = $2, stale = $3, quota_desynced_at = NULL WHERE id = $4`,
		dummyScrapedAt, 0.0, false, srv.ID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}
