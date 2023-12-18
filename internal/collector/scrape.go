/*******************************************************************************
*
* Copyright 2017-2023 SAP SE
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
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

const (
	//how long to wait before scraping the same project and service again
	scrapeInterval = 30 * time.Minute
	//how long to wait before re-checking a project service that failed scraping
	recheckInterval = 5 * time.Minute
)

var (
	// find the next project that needs to have resources scraped
	findProjectForResourceScrapeQuery = sqlext.SimplifyWhitespace(`
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

	writeResourceScrapeSuccessQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_services SET
			-- timing information
			checked_at = $1, scraped_at = $1, next_scrape_at = $2, scrape_duration_secs = $3,
			-- serialized state returned by QuotaPlugin
			serialized_metrics = $4,
			-- other
			stale = FALSE, scrape_error_message = ''
		WHERE id = $5
	`)

	writeResourceScrapeErrorQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_services SET
			-- timing information
			checked_at = $1, next_scrape_at = $2,
			-- other
			stale = FALSE, scrape_error_message = $3
		WHERE id = $4
	`)
)

// ResourceScrapeJob looks at one specific project service per task, collects
// quota and usage information from the backend service, and adjusts the
// backend quota if it differs from the desired values.
//
// This job is not ConcurrencySafe, but multiple instances can safely be run in
// parallel if they act on separate service types. The job can only be run if
// a target service type is specified using the
// `jobloop.WithLabel("service_type", serviceType)` option.
func (c *Collector) ResourceScrapeJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.ProducerConsumerJob[projectScrapeTask]{
		Metadata: jobloop.JobMetadata{
			ReadableName: "scrape project quota and usage",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_resource_scrapes",
				Help: "Counter for resource scrape operations per Keystone project.",
			},
			CounterLabels: []string{"service_type", "service_name"},
		},
		DiscoverTask: func(_ context.Context, labels prometheus.Labels) (projectScrapeTask, error) {
			return c.discoverScrapeTask(labels, findProjectForResourceScrapeQuery)
		},
		ProcessTask: c.processResourceScrapeTask,
	}).Setup(registerer)
}

// This is the task type for ResourceScrapeJob and RateScrapeJob. The natural
// task type for these jobs is just db.ProjectService, but this more elaborate
// task type allows us to reuse timing information from the discover step.
type projectScrapeTask struct {
	//data loaded during discoverScrapeTask
	Service db.ProjectService
	//timing information
	Timing TaskTiming
	//error reporting
	Err error
}

func (c *Collector) discoverScrapeTask(labels prometheus.Labels, query string) (task projectScrapeTask, err error) {
	serviceType := labels["service_type"]
	if !c.Cluster.HasService(serviceType) {
		return projectScrapeTask{}, fmt.Errorf("no such service type: %q", serviceType)
	}
	labels["service_name"] = c.Cluster.InfoForService(serviceType).ProductName

	task.Timing.StartedAt = c.MeasureTime()
	err = c.DB.SelectOne(&task.Service, query, serviceType, task.Timing.StartedAt)
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

func (c *Collector) processResourceScrapeTask(_ context.Context, task projectScrapeTask, labels prometheus.Labels) error {
	srv := task.Service
	plugin := c.Cluster.QuotaPlugins[srv.Type] //NOTE: discoverScrapeTask already verified that this exists

	//collect additional DB records
	dbProject, dbDomain, project, err := c.identifyProjectBeingScraped(srv)
	if err != nil {
		return err
	}
	logg.Debug("scraping %s resources for %s/%s", srv.Type, dbDomain.Name, dbProject.Name)

	//perform resource scrape
	resourceData, serializedMetrics, err := plugin.Scrape(project, c.Cluster.Config.AvailabilityZones)
	if err != nil {
		task.Err = util.UnpackError(err)
	}
	task.Timing.FinishedAt = c.MeasureTimeAtEnd()

	//write result on success; if anything fails, try to record the error in the DB
	if task.Err == nil {
		err := c.writeResourceScrapeResult(dbDomain, dbProject, task, resourceData, serializedMetrics)
		if err != nil {
			task.Err = fmt.Errorf("while writing results into DB: %w", err)
		}
	}
	if task.Err != nil {
		_, err := c.DB.Exec(
			writeResourceScrapeErrorQuery,
			task.Timing.FinishedAt, task.Timing.FinishedAt.Add(c.AddJitter(recheckInterval)),
			task.Err.Error(), srv.ID,
		)
		if err != nil {
			c.LogError("additional DB error while writing resource scrape error for service %s in project %s: %s",
				srv.Type, project.UUID, err.Error(),
			)
		}

		if srv.ScrapedAt == nil {
			//see explanation inside the called function's body
			err := c.writeDummyResources(dbDomain, dbProject, srv.Ref())
			if err != nil {
				c.LogError("additional DB error while writing dummy resources for service %s in project %s: %s",
					srv.Type, project.UUID, err.Error(),
				)
			}
		}
	}

	if task.Err == nil {
		return nil
	}
	return fmt.Errorf("during resource scrape of project %s/%s: %w", dbDomain.Name, dbProject.Name, task.Err)
}

func (c *Collector) writeResourceScrapeResult(dbDomain db.Domain, dbProject db.Project, task projectScrapeTask, resourceData map[string]core.ResourceData, serializedMetrics []byte) error {
	srv := task.Service

	//ensure that there is at least one ProjectAZResource for each ProjectResource
	for resName, resData := range resourceData {
		if len(resData.UsageData) == 0 {
			resData.UsageData = core.InAnyAZ(core.UsageData{Usage: 0})
			resourceData[resName] = resData
		}
	}

	tx, err := c.DB.Begin()
	if err != nil {
		return fmt.Errorf("while beginning transaction: %w", err)
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	//we have seen UPDATEs in this transaction getting stuck in the past, this
	//should hopefully prevent this (or at least cause loud complaints when it
	//happens)
	//
	//TODO: consider setting this for the entire connection if it helps
	_, err = tx.Exec(`SET LOCAL idle_in_transaction_session_timeout = 5000`) // 5000 ms = 5 seconds
	if err != nil {
		return fmt.Errorf("while applying idle_in_transaction_session_timeout: %w", err)
	}

	//this is the callback that ProjectResourceUpdate will use to write the scraped data into the project_resources
	updateResource := func(res *db.ProjectResource) error {
		backendQuota := resourceData[res.Name].Quota

		resInfo := c.Cluster.InfoForResource(srv.Type, res.Name)
		if !resInfo.NoQuota {
			//check if we can auto-approve an initial quota
			if res.BackendQuota == nil && (res.Quota == nil || *res.Quota == 0) && backendQuota > 0 && uint64(backendQuota) == resInfo.AutoApproveInitialQuota {
				res.Quota = &resInfo.AutoApproveInitialQuota
				logg.Other("AUDIT", "changing %s/%s quota for project %s/%s from %s to %s through auto-approval",
					srv.Type, res.Name, dbDomain.Name, dbProject.Name,
					limes.ValueWithUnit{Value: 0, Unit: resInfo.Unit},
					limes.ValueWithUnit{Value: resInfo.AutoApproveInitialQuota, Unit: resInfo.Unit},
				)
			}

			res.BackendQuota = &backendQuota
		}

		return nil
	}

	//update project_resources using the action callback from above
	resourceUpdateResult, err := datamodel.ProjectResourceUpdate{
		UpdateResource: updateResource,
		LogError:       c.LogError,
	}.Run(tx, c.Cluster, dbDomain, dbProject, srv.Ref())
	if err != nil {
		return err
	}

	//list existing project_az_resources
	var dbAZResources []db.ProjectAZResource
	_, err = tx.Select(&dbAZResources, findProjectAZResourcesForServiceQuery, srv.ID)
	if err != nil {
		return fmt.Errorf("while reading existing project AZ resources: %w", err)
	}
	dbAZResourcesByResourceID := make(map[int64][]db.ProjectAZResource, len(resourceUpdateResult.DBResources))
	for _, azRes := range dbAZResources {
		dbAZResourcesByResourceID[azRes.ResourceID] = append(dbAZResourcesByResourceID[azRes.ResourceID], azRes)
	}
	allResourceNames := make([]string, len(resourceUpdateResult.DBResources))
	dbResourcesByName := make(map[string]db.ProjectResource, len(resourceUpdateResult.DBResources))
	for idx, res := range resourceUpdateResult.DBResources {
		allResourceNames[idx] = res.Name
		dbResourcesByName[res.Name] = res
	}
	sort.Strings(allResourceNames) //for deterministic test behavior

	//update project_az_resources for each resource
	for _, resourceName := range allResourceNames {
		res := dbResourcesByName[resourceName]
		usageData := resourceData[resourceName].UsageData.Normalize(c.Cluster.Config.AvailabilityZones)

		setUpdate := db.SetUpdate[db.ProjectAZResource, limes.AvailabilityZone]{
			ExistingRecords: dbAZResourcesByResourceID[res.ID],
			WantedKeys:      usageData.Keys(),
			KeyForRecord: func(azRes db.ProjectAZResource) limes.AvailabilityZone {
				return azRes.AvailabilityZone
			},
			Create: func(az limes.AvailabilityZone) (db.ProjectAZResource, error) {
				return db.ProjectAZResource{
					ResourceID:       res.ID,
					AvailabilityZone: az,
				}, nil
			},
			Update: func(azRes *db.ProjectAZResource) (err error) {
				az := azRes.AvailabilityZone
				data := usageData[az]
				azRes.Quota = nil // TODO: add the new quota distribution model that assigns quota per AZ
				azRes.Usage = data.Usage
				azRes.PhysicalUsage = data.PhysicalUsage

				//warn when the backend is inconsistent with itself
				if data.Subresources != nil && uint64(len(data.Subresources)) != data.Usage {
					logg.Info("resource quantity mismatch in project %s, resource %s/%s, AZ %s: usage = %d, but found %d subresources",
						dbProject.UUID, srv.Type, res.Name, az,
						data.Usage, len(data.Subresources),
					)
				}

				azRes.SubresourcesJSON, err = renderListToJSON("subresources", data.Subresources)
				if err != nil {
					return err
				}

				//track historical usage if required (only required for AutogrowQuotaDistribution)
				qdCfg := c.Cluster.QuotaDistributionConfigForResource(srv.Type, res.Name)
				if qdCfg.Autogrow == nil {
					azRes.HistoricalUsageJSON = ""
				} else {
					var ts util.TimeSeries[uint64]
					if azRes.HistoricalUsageJSON == "" {
						ts = util.EmptyTimeSeries[uint64]()
					} else {
						err := json.Unmarshal([]byte(azRes.HistoricalUsageJSON), &ts)
						if err != nil {
							return fmt.Errorf("while parsing historical_usage for AZ %s: %w", az, err)
						}
					}
					err := ts.AddMeasurement(task.Timing.FinishedAt, data.Usage)
					if err != nil {
						return fmt.Errorf("while tracking historical_usage for AZ %s: %w", az, err)
					}
					ts.PruneOldValues(task.Timing.FinishedAt, qdCfg.Autogrow.UsageDataRetentionPeriod.Into())
					buf, err := json.Marshal(ts)
					if err != nil {
						return fmt.Errorf("while serializing historical_usage for AZ %s: %w", az, err)
					}
					azRes.HistoricalUsageJSON = string(buf)
				}

				return nil
			},
		}
		_, err := setUpdate.Execute(tx)
		if err != nil {
			return err
		}
	}

	//update scraped_at timestamp and reset the stale flag on this service so
	//that we don't scrape it again immediately afterwards; also persist all other
	//attributes that we have not written yet
	_, err = tx.Exec(writeResourceScrapeSuccessQuery,
		task.Timing.FinishedAt, task.Timing.FinishedAt.Add(c.AddJitter(scrapeInterval)), task.Timing.Duration().Seconds(),
		string(serializedMetrics), srv.ID,
	)
	if err != nil {
		return fmt.Errorf("while updating metadata on project service: %w", err)
	}

	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("while committing transaction: %w", err)
	}

	if task.Timing.Duration() > 5*time.Minute {
		logg.Info("scrape of %s in project %s has taken excessively long (%s)", srv.Type, dbProject.UUID, task.Timing.Duration().String())
	}

	//if a mismatch between frontend and backend quota was detected, try to
	//rectify it (but an error at this point is non-fatal: we don't want scraping
	//to get stuck because some project has backend_quota > usage > quota, for
	//example)
	if c.Cluster.Authoritative && resourceUpdateResult.HasBackendQuotaDrift {
		domain := core.KeystoneDomainFromDB(dbDomain)
		err := datamodel.ApplyBackendQuota(c.DB, c.Cluster, domain, dbProject, srv.Ref())
		if err != nil {
			logg.Error("could not rectify frontend/backend quota mismatch for service %s in project %s: %s",
				srv.Type, dbProject.UUID, err.Error(),
			)
		}
	}

	return nil
}

func (c *Collector) writeDummyResources(dbDomain db.Domain, dbProject db.Project, srv db.ProjectServiceRef) error {
	//Rationale: This is called when we first try to scrape a project service,
	//and the scraping fails (most likely due to some internal error in the
	//backend service). We used to just not touch the database at this point,
	//thus resuming scraping of the same project service in the next loop
	//iteration of c.Scrape(). However, when the backend service is down for some
	//time, this means that project_services in new projects are stuck without
	//project_resources, which is an unexpected state that confuses the API.
	//
	//To avoid this situation, this method creates dummy project_resources for an
	//unscrapable project_service. Also, scraped_at is set to 0 (i.e. 1970-01-01
	//00:00:00 UTC) to make the scraper come back to it after dealing with all
	//new and stale project_services.
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	//create all project_resources, but do not set any particular values (except
	//that quota constraints and default quotas are enforced)
	updateResult, err := datamodel.ProjectResourceUpdate{
		UpdateResource: func(res *db.ProjectResource) error {
			resInfo := c.Cluster.InfoForResource(srv.Type, res.Name)
			if !resInfo.NoQuota && res.BackendQuota == nil {
				dummyBackendQuota := int64(-1)
				res.BackendQuota = &dummyBackendQuota
			}
			return nil
		},
		LogError: c.LogError,
	}.Run(tx, c.Cluster, dbDomain, dbProject, srv)
	if err != nil {
		return err
	}
	//NOTE: We do not do ApplyBackendQuota here: This function is only
	//called after scraping errors, so ApplyBackendQuota will likely fail, too.

	//create dummy project_az_resources
	for _, res := range updateResult.DBResources {
		err := tx.Insert(&db.ProjectAZResource{
			ResourceID:       res.ID,
			AvailabilityZone: limes.AvailabilityZoneAny,
			Usage:            0,
		})
		if err != nil {
			return err
		}
	}

	//update scraped_at timestamp and reset stale flag to make sure that we do
	//not scrape this service again immediately afterwards if there are other
	//stale services to cover first
	dummyScrapedAt := time.Unix(0, 0).UTC()
	_, err = tx.Exec(
		`UPDATE project_services SET scraped_at = $1, scrape_duration_secs = $2, stale = $3 WHERE id = $4`,
		dummyScrapedAt, 0.0, false, srv.ID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}
