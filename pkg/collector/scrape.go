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
	"sort"
	"time"

	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

//how long to sleep after a scraping error, or when nothing needed scraping
var idleInterval = 10 * time.Second

//how long to wait before scraping the same project and service again
var scrapeInterval = 30 * time.Minute

//query that finds the next project that needs to be scraped
var findProjectQuery = `
	SELECT ps.id, p.name, p.uuid, d.name, d.uuid
	FROM project_services ps
	JOIN projects p ON p.id = ps.project_id
	JOIN domains d ON d.id = p.domain_id
	-- filter by cluster ID and service type
	WHERE d.cluster_id = $1 AND ps.name = $2
	-- filter by need to be updated (because of user request, because of missing data, or because of outdated data)
	AND (ps.stale OR ps.scraped_at IS NULL OR ps.scraped_at < $3)
	-- order by update priority (in the same way: first user-requested, then new projects, then outdated projects)
	ORDER BY ps.stale DESC, COALESCE(ps.scraped_at, to_timestamp(0)) ASC
	-- find only one project to scrape per iteration
	LIMIT 1
`

//Scrape checks the database periodically for outdated or missing resource
//records for the given driver's cluster and the given service type, and
//updates them by querying the backend service.
//
//Errors are logged instead of returned. The function will not return unless
//startup fails.
func Scrape(driver limes.Driver, plugin limes.Plugin) {
	scraper{once: false, logError: util.LogError, timeNow: time.Now}.Scrape(driver, plugin)
}

//This provides various hooks to Scrape() that the unit test needs: It can mock
//away the actual clock, send errors to the test runner, and inhibit the
//endless loop logic.
type scraper struct {
	once     bool
	logError func(msg string, args ...interface{})
	timeNow  func() time.Time
}

func (s scraper) Scrape(driver limes.Driver, plugin limes.Plugin) {
	serviceType := plugin.ServiceType()
	clusterID := driver.Cluster().ID

	for {
		var (
			serviceID   int64
			projectName string
			projectUUID string
			domainName  string
			domainUUID  string
		)
		err := db.DB.QueryRow(findProjectQuery, clusterID, serviceType, s.timeNow().Add(-scrapeInterval)).
			Scan(&serviceID, &projectName, &projectUUID, &domainName, &domainUUID)
		if err != nil {
			//ErrNoRows is okay; it just means that needs scraping right now
			if err != sql.ErrNoRows {
				//TODO: there should be some sort of detection for persistent DB errors
				//(such as "the DB has burst into flames"); maybe a separate thread that
				//just pings the DB every now and then and does os.Exit(1) if it fails);
				//check if database/sql has something like that built-in
				s.logError("cannot select next project for which to scrape %s data: %s", serviceType, err.Error())
			}
			if s.once {
				return
			}
			time.Sleep(idleInterval)
			continue
		}

		util.LogDebug("scraping %s for %s/%s", serviceType, domainName, projectName)
		resourceData, err := plugin.Scrape(driver, domainUUID, projectUUID)
		if err != nil {
			s.logError("scrape %s data for %s/%s failed: %s", serviceType, domainName, projectName, err.Error())
			if s.once {
				return
			}
			time.Sleep(idleInterval)
			continue
		}

		err = s.writeScrapeResult(serviceID, resourceData, s.timeNow())
		if err != nil {
			s.logError("write %s backend data for %s/%s failed: %s", serviceType, domainName, projectName, err.Error())
			if s.once {
				return
			}
			time.Sleep(idleInterval)
			continue
		}

		if s.once {
			break
		}
		//If no error occurred, continue with the next project immediately, so as
		//to finish scraping as fast as possible when there are multiple projects
		//to scrape at once.
	}
}

func (s scraper) writeScrapeResult(serviceID int64, resourceData map[string]limes.ResourceData, scrapedAt time.Time) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer db.RollbackUnlessCommitted(tx)

	//update existing project_resources entries
	seen := make(map[string]bool)
	records, err := tx.Select(&db.ProjectResource{}, `SELECT * FROM project_resources WHERE service_id = $1`, serviceID)
	if err != nil {
		return err
	}
	for _, record := range records {
		res := record.(*db.ProjectResource)
		seen[res.Name] = true

		data, exists := resourceData[res.Name]
		if exists {
			//update existing resource record
			res.BackendQuota = data.Quota
			res.Usage = data.Usage
			_, err := tx.Update(res)
			if err != nil {
				return err
			}
		} else {
			s.logError(
				"could not scrape new data for resource %s in project service %d (was this resource type removed from the scraper plugin?)",
				res.Name, serviceID,
			)
		}
	}

	//insert missing project_resources entries
	err = foreachResourceData(resourceData, func(name string, data limes.ResourceData) error {
		if seen[name] {
			return nil
		}
		res := &db.ProjectResource{
			ServiceID:    serviceID,
			Name:         name,
			Quota:        0, //nothing approved yet
			Usage:        data.Usage,
			BackendQuota: data.Quota,
		}
		return tx.Insert(res)
	})
	if err != nil {
		return err
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

	return tx.Commit()
}

//Like a simple `range` over the collection, but sorts the keys before doing so
//in order to achieve deterministic results (which is important for the unit
//tests).
func foreachResourceData(collection map[string]limes.ResourceData, action func(string, limes.ResourceData) error) error {
	keys := make([]string, 0, len(collection))
	for key := range collection {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		err := action(key, collection[key])
		if err != nil {
			return err
		}
	}
	return nil
}
