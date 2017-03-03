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
	"errors"
	"time"

	"github.com/sapcc/limes/pkg/drivers"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/models"
)

//how long to sleep after a scraping error, or when nothing needed scraping
var idleInterval = 10 * time.Second

//how long to wait before scraping the same project and service again
var scrapeInterval = 30 * time.Minute

//Scrape checks the database periodically for outdated or missing resource
//records for the given driver's cluster and the given service type, and
//updates them by querying the backend service.
//
//Errors are only returned if the pre-flight checks fail. Once in flight, the
//function will log errors and never return.
func Scrape(driver drivers.Driver, serviceType string) error {
	plugin := GetPlugin(serviceType)
	if plugin == nil {
		return errors.New("no scraper plugin found for this service")
	}
	clusterID := driver.Cluster().ID

	//prepare SQL statement to find the next project to update
	findProjectStmt, err := limes.DB.Prepare(`
		SELECT ps.id, p.uuid, d.uuid
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
	`)
	if err != nil {
		return err
	}
	defer findProjectStmt.Close()

	for {
		var (
			serviceID   uint64
			projectUUID string
			domainUUID  string
		)
		err := findProjectStmt.
			QueryRow(clusterID, serviceType, time.Now().Add(-scrapeInterval)).
			Scan(&serviceID, &projectUUID, &domainUUID)
		if err != nil {
			//ErrNoRows is okay; it just means that needs scraping right now
			if err != sql.ErrNoRows {
				//TODO: there should be some sort of detection for persistent DB errors
				//(such as "the DB has burst into flames"); maybe a separate thread that
				//just pings the DB every now and then and does os.Exit(1) if it fails);
				//check if database/sql has something like that built-in
				limes.Log(limes.LogError, err.Error())
			}
			time.Sleep(idleInterval)
			continue
		}

		resourceDataList, err := plugin.Scrape(driver, domainUUID, projectUUID)
		if err != nil {
			limes.Log(limes.LogError, err.Error())
			time.Sleep(idleInterval)
			continue
		}

		err = writeScrapeResult(serviceID, resourceDataList, time.Now())
		if err != nil {
			limes.Log(limes.LogError, err.Error())
			time.Sleep(idleInterval)
			continue
		}

		//If no error occurred, continue with the next project immediately, so as
		//to finish scraping as fast as possible when there are multiple projects
		//to scrape at once.
	}

	return nil
}

func writeScrapeResult(serviceID uint64, resourceDataList []ResourceData, scrapedAt time.Time) error {
	tx, err := limes.DB.Begin()
	if err != nil {
		return err
	}

	//find existing resource records
	existing := make(map[string]*models.ProjectResource)
	err = models.ProjectResourcesTable.Where(`service_id = $1`, serviceID).
		Foreach(tx, func(record models.Record) error {
			res := record.(*models.ProjectResource)
			existing[res.Name] = res
			return nil
		})
	if err != nil {
		return err
	}

	//insert or update resource records
	for _, data := range resourceDataList {
		record, exists := existing[data.Name]
		if exists {
			//update existing resource record
			_, err := tx.Exec(
				`UPDATE project_resources SET backend_quota = $1, usage = $2 WHERE service_id = $3 AND name = $4`,
				data.Quota, data.Usage, serviceID, data.Name,
			)
			if err != nil {
				return err
			}
		} else {
			//insert new resource record
			record = &models.ProjectResource{
				ServiceID:    serviceID,
				Name:         data.Name,
				Quota:        0, //nothing approved yet
				Usage:        data.Usage,
				BackendQuota: data.Quota,
			}
			err := record.Insert(tx)
			if err != nil {
				return err
			}
		}
	}

	//warn about resource records that cannot be scraped anymore (don't delete
	//these immediately; it might just be due to a bug in the scraper plugin, and
	//we don't want to lose our approved quota values from the local DB)
	for _, data := range resourceDataList {
		delete(existing, data.Name)
	}
	for name := range existing {
		limes.Log(limes.LogError,
			"could not scrape new data for resource %s in project service %d (was this resource type removed from the scraper plugin?)",
			name, serviceID,
		)
	}

	//update scraped_at timestamp on this service so that we don't scrape it
	//again immediately afterwards
	_, err = tx.Exec(
		`UPDATE project_services SET scraped_at = $1 WHERE id = $2`,
		scrapedAt, serviceID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}
