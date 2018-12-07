/*******************************************************************************
*
* Copyright 2017 SAP SE
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
	"math"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/datamodel"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
)

//how long to sleep after a scraping error, or when nothing needed scraping
var idleInterval = 10 * time.Second

//how long to sleep when scraping fails because the backend service is not in the catalog
var serviceNotDeployedIdleInterval = 10 * time.Minute

//how long to wait before scraping the same project and service again
var scrapeInterval = 30 * time.Minute

//query that finds the next project that needs to be scraped
var findProjectQuery = `
	SELECT ps.id, ps.scraped_at, p.name, p.uuid, p.id, d.name, d.uuid
	FROM project_services ps
	JOIN projects p ON p.id = ps.project_id
	JOIN domains d ON d.id = p.domain_id
	-- filter by cluster ID and service type
	WHERE d.cluster_id = $1 AND ps.type = $2
	-- filter by need to be updated (because of user request, because of missing data, or because of outdated data)
	AND (ps.stale OR ps.scraped_at IS NULL OR ps.scraped_at < $3)
	-- order by update priority (in the same way: first user-requested, then new projects, then outdated projects, then ID for deterministic test behavior)
	ORDER BY ps.stale DESC, COALESCE(ps.scraped_at, to_timestamp(-1)) ASC, ps.id ASC
	-- find only one project to scrape per iteration
	LIMIT 1
`

//Scrape checks the database periodically for outdated or missing resource
//records for the given cluster and the given service type, and updates them by
//querying the backend service.
//
//Errors are logged instead of returned. The function will not return unless
//startup fails.
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

	for {
		var (
			serviceID        int64
			serviceScrapedAt *time.Time
			projectName      string
			projectUUID      string
			projectID        int64
			domainName       string
			domainUUID       string
		)
		err := db.DB.QueryRow(findProjectQuery, c.Cluster.ID, serviceType, c.TimeNow().Add(-scrapeInterval)).
			Scan(&serviceID, &serviceScrapedAt, &projectName, &projectUUID, &projectID, &domainName, &domainUUID)
		if err != nil {
			//ErrNoRows is okay; it just means that nothing needs scraping right now
			if err != sql.ErrNoRows {
				//TODO: there should be some sort of detection for persistent DB errors
				//(such as "the DB has burst into flames"); maybe a separate thread that
				//just pings the DB every now and then and does os.Exit(1) if it fails);
				//check if database/sql has something like that built-in
				c.LogError("cannot select next project for which to scrape %s data: %s", serviceType, err.Error())
			}
			if c.Once {
				return
			}
			time.Sleep(idleInterval)
			continue
		}

		logg.Debug("scraping %s for %s/%s", serviceType, domainName, projectName)
		provider, eo := c.Cluster.ProviderClientForService(serviceType)
		resourceData, err := c.Plugin.Scrape(provider, eo, c.Cluster.ID, domainUUID, projectUUID)
		if err != nil {
			//special case: stop scraping for a while when the backend service is not
			//yet registered in the catalog (this prevents log spamming during buildup)
			sleepInterval := idleInterval
			if _, ok := err.(*gophercloud.ErrEndpointNotFound); ok {
				sleepInterval = serviceNotDeployedIdleInterval
				c.LogError("suspending %s data scraping for %d minutes: %s", serviceType, sleepInterval/time.Minute, err.Error())
			} else {
				c.LogError("scrape %s data for %s/%s failed: %s", serviceType, domainName, projectName, err.Error())
				scrapeFailedCounter.With(labels).Inc()

				if serviceScrapedAt == nil {
					//see explanation inside the called function's body
					err := c.writeDummyResources(domainName, projectName, serviceType, serviceID)
					if err != nil {
						c.LogError("write dummy resource data for service %s for %s/%s failed: %s", serviceType, domainName, projectName, err.Error())
					}
				}
			}

			if c.Once {
				return
			}
			time.Sleep(sleepInterval)
			continue
		}

		err = c.writeScrapeResult(domainName, domainUUID, projectName, projectUUID, projectID, serviceType, serviceID, resourceData, c.TimeNow())
		if err != nil {
			c.LogError("write %s backend data for %s/%s failed: %s", serviceType, domainName, projectName, err.Error())
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

func (c *Collector) writeScrapeResult(domainName, domainUUID, projectName, projectUUID string, projectID int64, serviceType string, serviceID int64, resourceData map[string]limes.ResourceData, scrapedAt time.Time) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer db.RollbackUnlessCommitted(tx)

	var serviceConstraints map[string]limes.QuotaConstraint
	if c.Cluster.QuotaConstraints != nil {
		serviceConstraints = c.Cluster.QuotaConstraints.Projects[domainName][projectName][serviceType]
	}

	//update existing project_resources entries
	resourceExists := make(map[string]bool)
	var resources []db.ProjectResource
	_, err = tx.Select(&resources, `SELECT * FROM project_resources WHERE service_id = $1`, serviceID)
	if err != nil {
		return err
	}
	for _, res := range resources {
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
		if constraint.Validate(res.Quota) != nil {
			resInfo := c.Cluster.InfoForResource(serviceType, res.Name)
			newQuota := constraint.ApplyTo(res.Quota)
			logg.Info("changing %s/%s quota for project %s/%s from %s to %s to satisfy constraint %q",
				serviceType, res.Name, domainName, projectName,
				limes.ValueWithUnit{Value: res.Quota, Unit: resInfo.Unit},
				limes.ValueWithUnit{Value: newQuota, Unit: resInfo.Unit},
				constraint.String(),
			)
			res.Quota = newQuota
		}

		//update existing resource record
		res.Usage = data.Usage
		res.BackendQuota = data.Quota
		if c.Cluster.InfoForResource(serviceType, res.Name).ExternallyManaged {
			if data.Quota >= 0 {
				res.Quota = uint64(data.Quota)
			} else {
				res.Quota = math.MaxUint64
			}
		}

		if len(data.Subresources) == 0 {
			res.SubresourcesJSON = ""
		} else {
			//warn when the backend is inconsistent with itself
			if uint64(len(data.Subresources)) != res.Usage {
				logg.Info("resource quantity mismatch in project %s, resource %s/%s: usage = %d, but found %d subresources",
					projectUUID, serviceType, res.Name,
					res.Usage, len(data.Subresources),
				)
			}
			bytes, err := json.Marshal(data.Subresources)
			if err != nil {
				return fmt.Errorf("failed to convert subresources to JSON: %s", err.Error())
			}
			res.SubresourcesJSON = string(bytes)
		}

		//TODO: Update() only if required
		_, err := tx.Update(&res)
		if err != nil {
			return err
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
			Quota:            initialQuota,
			Usage:            data.Usage,
			BackendQuota:     data.Quota,
			SubresourcesJSON: "", //but see below
		}

		if resMetadata.ExternallyManaged {
			if data.Quota >= 0 {
				res.Quota = uint64(data.Quota)
			} else {
				res.Quota = math.MaxUint64
			}
		}

		if res.Quota == 0 && data.Quota > 0 && uint64(data.Quota) == resMetadata.AutoApproveInitialQuota {
			res.Quota = resMetadata.AutoApproveInitialQuota

			//temp workaround until CADF audit trail is implemented
			logg.Other("AUDIT", fmt.Sprintf("set quota %s.%s = 0 -> %d for project %s through auto-approval",
				serviceType, resMetadata.Name, res.Quota, projectUUID),
			)
		}

		if len(data.Subresources) != 0 {
			//warn when the backend is inconsistent with itself
			if uint64(len(data.Subresources)) != data.Usage {
				logg.Info("resource quantity mismatch in project %s, resource %s/%s: usage = %d, but found %d subresources",
					projectUUID, serviceType, res.Name,
					data.Usage, len(data.Subresources),
				)
			}
			bytes, err := json.Marshal(data.Subresources)
			if err != nil {
				return fmt.Errorf("failed to convert subresources to JSON: %s", err.Error())
			}
			res.SubresourcesJSON = string(bytes)
		}

		err = tx.Insert(res)
		if err != nil {
			return err
		}
	}

	//update scraped_at timestamp and reset the stale flag on this service so
	//that we don't scrape it again immediately afterwards
	_, err = tx.Exec(
		`UPDATE project_services SET scraped_at = $1, stale = $2 WHERE id = $3`,
		scrapedAt, false, serviceID,
	)
	if err != nil {
		return err
	}

	err = tx.Commit()
	if err != nil {
		return err
	}

	//if a mismatch between frontend and backend quota was detected, try to
	//rectify it (but an error at this point is non-fatal: we don't want scraping
	//to get stuck because some project has backend_quota > usage > quota, for
	//example)
	if c.Cluster.Authoritative {
		var project db.Project
		err := db.DB.SelectOne(&project, `SELECT * FROM projects WHERE id = $1`, projectID)
		if err == nil {
			err = datamodel.ApplyBackendQuota(db.DB, c.Cluster, domainUUID, project, serviceID, serviceType)
		}
		if err != nil {
			logg.Error("could not rectify frontend/backend quota mismatch for service %s in project %s: %s",
				serviceType, projectUUID, err.Error(),
			)
		}
	}

	return nil
}

func (c *Collector) writeDummyResources(domainName, projectName, serviceType string, serviceID int64) error {
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
	defer db.RollbackUnlessCommitted(tx)

	var serviceConstraints map[string]limes.QuotaConstraint
	if c.Cluster.QuotaConstraints != nil {
		serviceConstraints = c.Cluster.QuotaConstraints.Projects[domainName][projectName][serviceType]
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

		res := &db.ProjectResource{
			ServiceID:        serviceID,
			Name:             resMetadata.Name,
			Quota:            initialQuota,
			Usage:            0,
			BackendQuota:     -1,
			SubresourcesJSON: "",
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
		`UPDATE project_services SET scraped_at = $1, stale = $2 WHERE id = $3`,
		dummyScrapedAt, false, serviceID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}
