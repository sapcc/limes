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
	"context"
	"fmt"
	"math/big"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

var (
	// find the next project that needs to have rates scraped
	findProjectForRateScrapeQuery = sqlext.SimplifyWhitespace(`
		SELECT * FROM project_services
		-- filter by service type
		WHERE type = $1
		-- filter by need to be updated (because of user request, or because of scheduled scrape)
		AND (rates_stale OR rates_next_scrape_at <= $2)
		-- order by update priority (first user-requested scrapes, then scheduled scrapes, then ID for deterministic test behavior)
		ORDER BY rates_stale DESC, rates_next_scrape_at ASC, id ASC
		-- find only one project to scrape per iteration
		LIMIT 1
	`)

	writeRateScrapeSuccessQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_services SET
			-- timing information
			rates_checked_at = $1, rates_scraped_at = $1, rates_next_scrape_at = $2, rates_scrape_duration_secs = $3,
			-- serialized state returned by QuotaPlugin
			rates_scrape_state = $4,
			-- other
			rates_stale = FALSE, rates_scrape_error_message = ''
		WHERE id = $5
	`)

	writeRateScrapeErrorQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_services SET
			-- timing information
			rates_checked_at = $1, rates_next_scrape_at = $2,
			-- other
			rates_stale = FALSE, rates_scrape_error_message = $3
		WHERE id = $4
	`)
)

// RateScrapeJob looks at one specific project service per task, checks the
// database for outdated or missing rate records for the given service, and
// updates them by querying the backend service.
//
// This job is not ConcurrencySafe, but multiple instances can safely be run in
// parallel if they act on separate service types. The job can only be run if
// a target service type is specified using the
// `jobloop.WithLabel("service_type", serviceType)` option.
func (c *Collector) RateScrapeJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.ProducerConsumerJob[projectScrapeTask]{
		Metadata: jobloop.JobMetadata{
			ReadableName: "scrape project rate usage",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_rate_scrapes",
				Help: "Counter for rate scrape operations per Keystone project.",
			},
			CounterLabels: []string{"service_type", "service_name"},
		},
		DiscoverTask: func(_ context.Context, labels prometheus.Labels) (projectScrapeTask, error) {
			return c.discoverScrapeTask(labels, findProjectForRateScrapeQuery)
		},
		ProcessTask: c.processRateScrapeTask,
	}).Setup(registerer)
}

func (c *Collector) processRateScrapeTask(ctx context.Context, task projectScrapeTask, labels prometheus.Labels) error {
	srv := task.Service
	plugin := c.Cluster.QuotaPlugins[srv.Type] //NOTE: discoverScrapeTask already verified that this exists

	// collect additional DB records
	_, _, project, err := c.identifyProjectBeingScraped(srv)
	if err != nil {
		return err
	}
	logg.Debug("scraping %s rates for %s/%s", srv.Type, project.Domain.Name, project.Name)

	// perform rate scrape
	rateData, ratesScrapeState, err := plugin.ScrapeRates(ctx, project, c.Cluster.Config.AvailabilityZones, srv.RatesScrapeState)
	if err != nil {
		task.Err = util.UnpackError(err)
	}
	task.Timing.FinishedAt = c.MeasureTimeAtEnd()

	// write result on success; if anything fails, try to record the error in the DB
	if task.Err == nil {
		err := c.writeRateScrapeResult(task, rateData, ratesScrapeState)
		if err != nil {
			task.Err = fmt.Errorf("while writing results into DB: %w", err)
		}
	}
	if task.Err != nil {
		_, err := c.DB.Exec(
			writeRateScrapeErrorQuery,
			task.Timing.FinishedAt, task.Timing.FinishedAt.Add(c.AddJitter(recheckInterval)),
			task.Err.Error(), srv.ID,
		)
		if err != nil {
			c.LogError("additional DB error while writing rate scrape error for service %s in project %s: %s",
				srv.Type, project.UUID, err.Error(),
			)
		}
	}

	if task.Err == nil {
		return nil
	}
	return fmt.Errorf("during rate scrape of project %s/%s: %w", project.Domain.Name, project.Name, task.Err)
}

func (c *Collector) writeRateScrapeResult(task projectScrapeTask, rateData map[liquid.RateName]*big.Int, ratesScrapeState string) error {
	srv := task.Service
	plugin := c.Cluster.QuotaPlugins[srv.Type] //NOTE: discoverScrapeTask already verified that this exists

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

	// insert missing project_rates entries
	for rateName := range plugin.Rates() {
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

	// update rate scraping metadata and also reset the rates_stale flag on this
	// service so that we don't scrape it again immediately afterwards
	_, err = tx.Exec(writeRateScrapeSuccessQuery,
		task.Timing.FinishedAt, task.Timing.FinishedAt.Add(c.AddJitter(scrapeInterval)), task.Timing.Duration().Seconds(),
		ratesScrapeState, srv.ID,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}
