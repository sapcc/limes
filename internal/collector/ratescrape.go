/*******************************************************************************
*
* Copyright 2020 SAP SE
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
	"fmt"
	"math/big"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// query that finds the next project that needs to have rates scraped
var findProjectForRateScrapeQuery = sqlext.SimplifyWhitespace(`
	SELECT ps.id, ps.rates_scraped_at, ps.rates_scrape_state, p.name, p.uuid, p.parent_uuid, d.name, d.uuid
	FROM project_services ps
	JOIN projects p ON p.id = ps.project_id
	JOIN domains d ON d.id = p.domain_id
	-- filter by service type
	WHERE ps.type = $1
	-- filter by need to be updated (because of user request, because of missing data, or because of outdated data)
	AND (ps.rates_stale OR ps.rates_scraped_at IS NULL OR ps.rates_scraped_at < $2 OR (ps.rates_scraped_at != ps.rates_checked_at AND ps.rates_checked_at < $3))
	-- order by update priority (in the same way: first user-requested, then new projects, then outdated projects, then failed projects, then ID for deterministic test behavior)
	ORDER BY ps.rates_stale DESC, COALESCE(ps.rates_checked_at, to_timestamp(-1)) ASC, ps.id ASC
	-- find only one project to scrape per iteration
	LIMIT 1
`)

// ScrapeRates checks the database periodically for outdated or missing rate
// records for the given cluster and the given service type, and updates them by
// querying the backend service.
//
// Errors are logged instead of returned. The function will not return unless
// startup fails.
func (c *Collector) ScrapeRates() {
	serviceInfo := c.Plugin.ServiceInfo()
	serviceType := serviceInfo.Type

	//make sure that the counters are reported
	labels := prometheus.Labels{
		"service":      serviceType,
		"service_name": serviceInfo.ProductName,
	}
	ratesScrapeSuccessCounter.With(labels).Add(0)
	ratesScrapeFailedCounter.With(labels).Add(0)
	ratesScrapeSuspendedCounter.With(labels).Add(0)

	for {
		var (
			serviceID               int64
			serviceRatesScrapedAt   *time.Time
			serviceRatesScrapeState string
			project                 core.KeystoneProject
		)
		scrapeStartedAt := c.TimeNow()
		err := c.DB.QueryRow(findProjectForRateScrapeQuery, serviceType, scrapeStartedAt.Add(-scrapeInterval), scrapeStartedAt.Add(-recheckInterval)).
			Scan(&serviceID, &serviceRatesScrapedAt, &serviceRatesScrapeState, &project.Name, &project.UUID, &project.ParentUUID, &project.Domain.Name, &project.Domain.UUID)
		if err != nil {
			//ErrNoRows is okay; it just means that nothing needs scraping right now
			if err != sql.ErrNoRows {
				c.LogError("cannot select next project for which to scrape %s rate data: %s", serviceType, err.Error())
			}
			if c.Once {
				return
			}
			time.Sleep(idleInterval)
			continue
		}

		logg.Debug("scraping %s rates for %s/%s", serviceType, project.Domain.Name, project.Name)
		srv := db.ProjectServiceRef{Type: serviceType, ID: serviceID}
		provider, eo := c.Cluster.ProviderClient()
		rateData, serviceRatesScrapeState, err := c.Plugin.ScrapeRates(provider, eo, project, serviceRatesScrapeState)
		scrapeEndedAt := c.TimeNow()

		//special case: stop scraping for a while when the backend service is not
		//yet registered in the catalog (this prevents log spamming during buildup)
		if _, ok := err.(*gophercloud.ErrEndpointNotFound); ok {
			c.LogError("suspending %s rate scraping for %d minutes: %s", serviceType, serviceNotDeployedIdleInterval/time.Minute, err.Error())
			ratesScrapeSuspendedCounter.With(labels).Inc()
			if c.Once {
				return
			}
			time.Sleep(serviceNotDeployedIdleInterval)
			continue
		}

		//write result on success; if anything fails, try to record the error in the DB
		if err == nil {
			err = c.writeRateScrapeResult(srv, rateData, serviceRatesScrapeState, scrapeEndedAt, scrapeEndedAt.Sub(scrapeStartedAt))
			if err != nil {
				err = fmt.Errorf("while writing results into DB: %w", err)
			}
		}
		if err != nil {
			c.writeRateScrapeError(project, srv, err, scrapeEndedAt, scrapeEndedAt.Sub(scrapeStartedAt))
			ratesScrapeFailedCounter.With(labels).Inc()
			c.LogError("scrape %s rate data for %s/%s failed: %s", serviceType, project.Domain.Name, project.Name, util.UnpackError(err).Error())

			if c.Once {
				return
			}
			time.Sleep(idleInterval)
			continue
		}

		ratesScrapeSuccessCounter.With(labels).Inc()
		if c.Once {
			break
		}
		//If no error occurred, continue with the next project immediately, so as
		//to finish scraping as fast as possible when there are multiple projects
		//to scrape at once.
	}
}

func (c *Collector) writeRateScrapeResult(srv db.ProjectServiceRef, rateData map[string]*big.Int, serviceRatesScrapeState string, scrapedAt time.Time, scrapeDuration time.Duration) error {
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	//update existing project_rates entries
	rateExists := make(map[string]bool)
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

		for _, rate := range rates {
			rateExists[rate.Name] = true

			usageData, exists := rateData[rate.Name]
			if !exists {
				if rate.UsageAsBigint != "" {
					c.LogError(
						"could not scrape new data for rate %s in project service %d (was this rate type removed from the scraper plugin for %s?)",
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

	//insert missing project_rates entries
	for _, rateMetadata := range c.Plugin.Rates() {
		if _, exists := rateExists[rateMetadata.Name]; exists {
			continue
		}
		usageData := rateData[rateMetadata.Name]

		rate := &db.ProjectRate{
			ServiceID: srv.ID,
			Name:      rateMetadata.Name,
		}
		if usageData != nil {
			rate.UsageAsBigint = usageData.String()
		}

		err = tx.Insert(rate)
		if err != nil {
			return err
		}
	}

	//update rate scraping metadata and also reset the rates_stale flag on this
	//service so that we don't scrape it again immediately afterwards
	_, err = tx.Exec(
		`UPDATE project_services SET rates_checked_at = $1, rates_scraped_at = $1, rates_scrape_duration_secs = $2, rates_scrape_state = $3, rates_stale = $4, rates_scrape_error_message = '' WHERE id = $5`,
		scrapedAt, scrapeDuration.Seconds(), serviceRatesScrapeState, false, srv.ID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (c *Collector) writeRateScrapeError(project core.KeystoneProject, srv db.ProjectServiceRef, scrapeErr error, checkedAt time.Time, checkDuration time.Duration) {
	_, err := c.DB.Exec(
		`UPDATE project_services SET rates_checked_at = $1, rates_scrape_duration_secs = $2, rates_scrape_error_message = $3, rates_stale = $4 WHERE id = $5`,
		checkedAt, checkDuration.Seconds(), util.UnpackError(scrapeErr).Error(), false, srv.ID,
	)
	if err != nil {
		logg.Error("additional DB error while trying to write rate scraping error for service %s in project %s: %s",
			srv.Type, project.UUID, err.Error(),
		)
	}
}