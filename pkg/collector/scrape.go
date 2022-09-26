/*******************************************************************************
*
* Copyright 2017-2020 SAP SE
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
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/datamodel"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/util"
)

const (
	//how long to sleep after a scraping error, or when nothing needed scraping
	idleInterval = 5 * time.Second
	//how long to sleep when scraping fails because the backend service is not in the catalog
	serviceNotDeployedIdleInterval = 5 * time.Minute
	//how long to wait before scraping the same project and service again
	scrapeInterval = 30 * time.Minute
	//how long to wait before re-checking a project service that failed scraping
	recheckInterval = 5 * time.Minute
)

// query that finds the next project that needs to have resources scraped
var findProjectForResourceScrapeQuery = sqlext.SimplifyWhitespace(`
	SELECT ps.id, ps.scraped_at, p.name, p.uuid, p.parent_uuid, p.id, p.has_bursting, d.name, d.uuid
	FROM project_services ps
	JOIN projects p ON p.id = ps.project_id
	JOIN domains d ON d.id = p.domain_id
	-- filter by cluster ID and service type
	WHERE d.cluster_id = $1 AND ps.type = $2
	-- filter by need to be updated (because of user request, because of missing data, or because of outdated data)
	AND (ps.stale OR ps.scraped_at IS NULL OR ps.scraped_at < $3 OR (ps.scraped_at != ps.checked_at AND ps.checked_at < $4))
	-- order by update priority (in the same way: first user-requested, then new projects, then outdated projects, then failed projects, then ID for deterministic test behavior)
	ORDER BY ps.stale DESC, COALESCE(ps.checked_at, to_timestamp(-1)) ASC, ps.id ASC
	-- find only one project to scrape per iteration
	LIMIT 1
`)

// Scrape checks the database periodically for outdated or missing resource
// records for the given cluster and the given service type, and updates them by
// querying the backend service.
//
// Errors are logged instead of returned. The function will not return unless
// startup fails.
func (c *Collector) Scrape() {
	serviceInfo := c.Plugin.ServiceInfo()
	serviceType := serviceInfo.Type

	//make sure that the counters are reported
	labels := prometheus.Labels{
		"os_cluster":   c.Cluster.ID,
		"service":      serviceType,
		"service_name": serviceInfo.ProductName,
	}
	scrapeSuccessCounter.With(labels).Add(0)
	scrapeFailedCounter.With(labels).Add(0)
	scrapeSuspendedCounter.With(labels).Add(0)

	for {
		var (
			serviceID          int64
			serviceScrapedAt   *time.Time
			project            core.KeystoneProject
			projectID          int64
			projectHasBursting bool
		)
		scrapeStartedAt := c.TimeNow()
		err := db.DB.QueryRow(findProjectForResourceScrapeQuery, c.Cluster.ID, serviceType, scrapeStartedAt.Add(-scrapeInterval), scrapeStartedAt.Add(-recheckInterval)).
			Scan(&serviceID, &serviceScrapedAt, &project.Name, &project.UUID, &project.ParentUUID, &projectID, &projectHasBursting, &project.Domain.Name, &project.Domain.UUID)
		if err != nil {
			//ErrNoRows is okay; it just means that nothing needs scraping right now
			if err != sql.ErrNoRows {
				c.LogError("cannot select next project for which to scrape %s resource data: %s", serviceType, err.Error())
			}
			if c.Once {
				return
			}
			time.Sleep(idleInterval)
			continue
		}

		logg.Debug("scraping %s resources for %s/%s", serviceType, project.Domain.Name, project.Name)
		provider, eo := c.Cluster.ProviderClient()
		resourceData, serializedMetrics, err := c.Plugin.Scrape(provider, eo, project)
		scrapeEndedAt := c.TimeNow()
		if err != nil {
			c.writeScrapeError(project, serviceType, serviceID, err, scrapeEndedAt, scrapeEndedAt.Sub(scrapeStartedAt))
			scrapeFailedCounter.With(labels).Inc()
			//special case: stop scraping for a while when the backend service is not
			//yet registered in the catalog (this prevents log spamming during buildup)
			sleepInterval := idleInterval
			if _, ok := err.(*gophercloud.ErrEndpointNotFound); ok {
				sleepInterval = serviceNotDeployedIdleInterval
				c.LogError("suspending %s resource scraping for %d minutes: %s", serviceType, sleepInterval/time.Minute, err.Error())
				scrapeSuspendedCounter.With(labels).Inc()
			} else {
				c.LogError("scrape %s resources for %s/%s failed: %s", serviceType, project.Domain.Name, project.Name, util.UnpackError(err).Error())

				if serviceScrapedAt == nil {
					//see explanation inside the called function's body
					err := c.writeDummyResources(project, projectHasBursting, serviceType, serviceID)
					if err != nil {
						c.LogError("write dummy resource data for service %s for %s/%s failed: %s", serviceType, project.Domain.Name, project.Name, err.Error())
					}
				}
			}

			if c.Once {
				return
			}
			time.Sleep(sleepInterval)
			continue
		}

		err = c.writeScrapeResult(project, projectID, projectHasBursting, serviceType, serviceID, resourceData, serializedMetrics, scrapeEndedAt, scrapeEndedAt.Sub(scrapeStartedAt))
		if err != nil {
			c.writeScrapeError(project, serviceType, serviceID, err, scrapeEndedAt, scrapeEndedAt.Sub(scrapeStartedAt))
			c.LogError("write %s backend data for %s/%s failed: %s", serviceType, project.Domain.Name, project.Name, err.Error())
			scrapeFailedCounter.With(labels).Inc()
			if c.Once {
				return
			}
			time.Sleep(idleInterval)
			continue
		}

		scrapeSuccessCounter.With(labels).Inc()
		if c.Once {
			break
		}
		//If no error occurred, continue with the next project immediately, so as
		//to finish scraping as fast as possible when there are multiple projects
		//to scrape at once.
	}
}

func (c *Collector) writeScrapeResult(project core.KeystoneProject, projectID int64, projectHasBursting bool, serviceType string, serviceID int64, resourceData map[string]core.ResourceData, serializedMetrics string, scrapedAt time.Time, scrapeDuration time.Duration) error {
	tx, err := db.DB.Begin()
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

	var serviceConstraints map[string]core.QuotaConstraint
	if c.Cluster.QuotaConstraints != nil {
		serviceConstraints = c.Cluster.QuotaConstraints.Projects[project.Domain.Name][project.Name][serviceType]
	}

	//update existing project_resources entries
	resourceExists := make(map[string]bool)
	var resources []db.ProjectResource
	_, err = tx.Select(&resources, `SELECT * FROM project_resources WHERE service_id = $1`, serviceID)
	if err != nil {
		return fmt.Errorf("while loading existing project resources: %w", err)
	}
	for _, resOriginal := range resources {
		res := resOriginal

		resourceExists[res.Name] = true

		data, exists := resourceData[res.Name]
		if !exists {
			c.LogError(
				"could not scrape new data for resource %s in project service %d (was this resource type removed from the scraper plugin?)",
				res.Name, serviceID,
			)
			continue
		}

		//check if we need to enforce a constraint
		constraint := serviceConstraints[res.Name]
		if res.Quota != nil && constraint.Validate(*res.Quota) != nil {
			resInfo := c.Cluster.InfoForResource(serviceType, res.Name)
			newQuota := constraint.ApplyTo(*res.Quota)
			logg.Info("changing %s/%s quota for project %s/%s from %s to %s to satisfy constraint %q",
				serviceType, res.Name, project.Domain.Name, project.Name,
				limes.ValueWithUnit{Value: *res.Quota, Unit: resInfo.Unit},
				limes.ValueWithUnit{Value: newQuota, Unit: resInfo.Unit},
				constraint.String(),
			)
			res.Quota = &newQuota
		}

		//update existing resource record
		resInfo := c.Cluster.InfoForResource(serviceType, res.Name)
		res.Usage = data.Usage
		res.PhysicalUsage = data.PhysicalUsage
		if resInfo.NoQuota {
			res.Quota = nil
			res.BackendQuota = nil
			res.DesiredBackendQuota = nil
		} else {
			res.BackendQuota = &data.Quota
		}

		if len(data.Subresources) == 0 {
			res.SubresourcesJSON = ""
		} else {
			//warn when the backend is inconsistent with itself
			if uint64(len(data.Subresources)) != res.Usage {
				logg.Info("resource quantity mismatch in project %s, resource %s/%s: usage = %d, but found %d subresources",
					project.UUID, serviceType, res.Name,
					res.Usage, len(data.Subresources),
				)
			}
			bytes, err := json.Marshal(data.Subresources)
			if err != nil {
				return fmt.Errorf("failed to convert subresources to JSON: %s", err.Error())
			}
			res.SubresourcesJSON = string(bytes)
		}

		if res != resOriginal {
			logg.Debug("writing scrape result in service %d: %#v", serviceID, res)
			_, err := tx.Update(&res)
			if err != nil {
				return fmt.Errorf("while writing %q project resource: %w", res.Name, err)
			}
		}
	}

	//insert missing project_resources entries
	for _, resMetadata := range c.Plugin.Resources() {
		if _, exists := resourceExists[resMetadata.Name]; exists {
			continue
		}
		data := resourceData[resMetadata.Name]

		initialQuota := uint64(0)
		if constraint := serviceConstraints[resMetadata.Name]; constraint.Minimum != nil {
			initialQuota = *constraint.Minimum
		}

		res := &db.ProjectResource{
			ServiceID:        serviceID,
			Name:             resMetadata.Name,
			Quota:            &initialQuota,
			Usage:            data.Usage,
			PhysicalUsage:    data.PhysicalUsage,
			BackendQuota:     &data.Quota,
			SubresourcesJSON: "", //but see below
		}

		if resMetadata.NoQuota {
			res.Quota = nil
			res.BackendQuota = nil
		} else {
			if *res.Quota == 0 && data.Quota > 0 && uint64(data.Quota) == resMetadata.AutoApproveInitialQuota {
				res.Quota = &resMetadata.AutoApproveInitialQuota

				logg.Other("AUDIT", fmt.Sprintf("set quota %s/%s = 0 -> %d for project %s through auto-approval",
					serviceType, resMetadata.Name, *res.Quota, project.UUID),
				)
			}

			if projectHasBursting {
				behavior := c.Cluster.BehaviorForResource(serviceType, resMetadata.Name, project.Domain.Name+"/"+project.Name)
				desiredBackendQuota := behavior.MaxBurstMultiplier.ApplyTo(*res.Quota)
				res.DesiredBackendQuota = &desiredBackendQuota
			} else {
				res.DesiredBackendQuota = res.Quota
			}
		}

		if len(data.Subresources) != 0 {
			//warn when the backend is inconsistent with itself
			if uint64(len(data.Subresources)) != data.Usage {
				logg.Info("resource quantity mismatch in project %s, resource %s/%s: usage = %d, but found %d subresources",
					project.UUID, serviceType, res.Name,
					data.Usage, len(data.Subresources),
				)
			}
			bytes, err := json.Marshal(data.Subresources)
			if err != nil {
				return fmt.Errorf("failed to convert subresources to JSON: %s", err.Error())
			}
			res.SubresourcesJSON = string(bytes)
		}

		logg.Debug("writing scrape result in service %d: %#v", serviceID, res)
		err = tx.Insert(res)
		if err != nil {
			return fmt.Errorf("while creating %q project resource: %w", res.Name, err)
		}
	}

	//update scraped_at timestamp and reset the stale flag on this service so
	//that we don't scrape it again immediately afterwards; also persist all other
	//attributes that we have not written yet
	logg.Debug("writing scrape result into service %d", serviceID)
	_, err = tx.Exec(
		`UPDATE project_services SET checked_at = $1, scraped_at = $1, scrape_duration_secs = $2, stale = $3, serialized_metrics = $4, scrape_error_message = '' WHERE id = $5`,
		scrapedAt, scrapeDuration.Seconds(), false, serializedMetrics, serviceID,
	)
	if err != nil {
		return fmt.Errorf("while updating metadata on project service: %w", err)
	}

	logg.Debug("committing scrape result in service %d", serviceID)
	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("while committing transaction: %w", err)
	}

	if scrapeDuration > 5*time.Minute {
		logg.Info("scrape of %s in project %s has taken excessively long (%s)", serviceType, project.UUID, scrapeDuration.String())
	}

	//if a mismatch between frontend and backend quota was detected, try to
	//rectify it (but an error at this point is non-fatal: we don't want scraping
	//to get stuck because some project has backend_quota > usage > quota, for
	//example)
	if c.Cluster.Authoritative {
		var dbProject db.Project
		logg.Debug("collecting project information for ApplyBackendQuota in service %d", serviceID)
		err := db.DB.SelectOne(&dbProject, `SELECT * FROM projects WHERE id = $1`, projectID)
		if err == nil {
			logg.Debug("calling into ApplyBackendQuota for service %d", serviceID)
			err = datamodel.ApplyBackendQuota(db.DB, c.Cluster, project.Domain, dbProject, serviceID, serviceType)
		}
		if err != nil {
			logg.Error("could not rectify frontend/backend quota mismatch for service %s in project %s: %s",
				serviceType, project.UUID, err.Error(),
			)
		}
	}

	return nil
}

func (c *Collector) writeScrapeError(project core.KeystoneProject, serviceType string, serviceID int64, scrapeErr error, checkedAt time.Time, checkDuration time.Duration) {
	_, err := db.DB.Exec(
		`UPDATE project_services SET checked_at = $1, scrape_duration_secs = $2, scrape_error_message = $3, stale = $4 WHERE id = $5`,
		checkedAt, checkDuration.Seconds(), util.UnpackError(scrapeErr).Error(), false, serviceID,
	)
	if err != nil {
		logg.Error("additional DB error while trying to write scraping error for service %s in project %s: %s",
			serviceType, project.UUID, err.Error(),
		)
	}
}

func (c *Collector) writeDummyResources(project core.KeystoneProject, projectHasBursting bool, serviceType string, serviceID int64) error {
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
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	var serviceConstraints map[string]core.QuotaConstraint
	if c.Cluster.QuotaConstraints != nil {
		serviceConstraints = c.Cluster.QuotaConstraints.Projects[project.Domain.Name][project.Name][serviceType]
	}

	//find existing project_resources entries (we don't want to touch those)
	var existingResources []db.ProjectResource
	_, err = tx.Select(&existingResources,
		`SELECT * FROM project_resources WHERE service_id = $1`, serviceID)
	if err != nil {
		return err
	}
	isExistingResource := make(map[string]bool)
	for _, res := range existingResources {
		isExistingResource[res.Name] = true
	}

	//create dummy resources
	for _, resMetadata := range c.Plugin.Resources() {
		if isExistingResource[resMetadata.Name] {
			continue
		}

		initialQuota := uint64(0)
		if constraint := serviceConstraints[resMetadata.Name]; constraint.Minimum != nil {
			initialQuota = *constraint.Minimum
		}

		dummyBackendQuota := int64(-1)
		res := &db.ProjectResource{
			ServiceID:        serviceID,
			Name:             resMetadata.Name,
			Quota:            &initialQuota,
			Usage:            0,
			PhysicalUsage:    nil,
			BackendQuota:     &dummyBackendQuota,
			SubresourcesJSON: "",
		}

		if resMetadata.NoQuota {
			res.Quota = nil
			res.BackendQuota = nil
		} else {
			if projectHasBursting {
				behavior := c.Cluster.BehaviorForResource(serviceType, resMetadata.Name, project.Domain.Name+"/"+project.Name)
				desiredBackendQuota := behavior.MaxBurstMultiplier.ApplyTo(*res.Quota)
				res.DesiredBackendQuota = &desiredBackendQuota
			} else {
				res.DesiredBackendQuota = res.Quota
			}
		}

		err = tx.Insert(res)
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
		dummyScrapedAt, 0.0, false, serviceID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}
