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
	"regexp"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/jobloop"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/plugins"
)

const (
	testRateScrapeBasicConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: --test-static
			params:
				domains:
					- { name: germany, id: uuid-for-germany }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
						- { name: dresden, id: uuid-for-dresden, parent_id: uuid-for-berlin }
		services:
			- service_type: unittest
				type: --test-generic
				params:
					rate_infos:
						- name: firstrate
						- name: secondrate
							unit: KiB
		bursting:
			# this should have no effect on rates
			max_multiplier: 0.1
	`
)

func Test_RateScrapeSuccess(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testRateScrapeBasicConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	c := getCollector(t, s)
	job := c.RateScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "unittest")

	// for one of the projects, put some records in for rate limits, to check that
	// the scraper does not mess with those values
	err := s.DB.Insert(&db.ProjectRate{
		ServiceID: 1,
		Name:      "secondrate",
		Limit:     p2u64(10),
		Window:    p2window(1 * limesrates.WindowSeconds),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.DB.Insert(&db.ProjectRate{
		ServiceID: 1,
		Name:      "otherrate",
		Limit:     p2u64(42),
		Window:    p2window(2 * limesrates.WindowMinutes),
	})
	if err != nil {
		t.Fatal(err)
	}

	// check that ScanDomains created the domain, project and their services; and
	// we set up our initial rates correctly
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO domain_resources (id, service_id, name, quota) VALUES (1, 1, 'capacity', 0);
		INSERT INTO domain_resources (id, service_id, name, quota) VALUES (2, 1, 'capacity_portion', 0);
		INSERT INTO domain_resources (id, service_id, name, quota) VALUES (3, 1, 'things', 0);
		INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unittest');
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'otherrate', 42, 120000000000, '');
		INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'secondrate', 10, 1000000000, '');
		INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'unittest', 0, 0);
		INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (2, 2, 'unittest', 0, 0);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
	`)

	// first Scrape should create the entries
	s.Clock.StepBy(scrapeInterval)
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel)) // twice because there are two projects

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_rates (service_id, name, usage_as_bigint) VALUES (1, 'firstrate', '9');
		UPDATE project_rates SET usage_as_bigint = '10' WHERE service_id = 1 AND name = 'secondrate';
		INSERT INTO project_rates (service_id, name, usage_as_bigint) VALUES (2, 'firstrate', '9');
		INSERT INTO project_rates (service_id, name, usage_as_bigint) VALUES (2, 'secondrate', '10');
		UPDATE project_services SET rates_scraped_at = %[1]d, rates_scrape_duration_secs = 5, rates_scrape_state = '{"firstrate":0,"secondrate":0}', rates_checked_at = %[1]d, rates_next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET rates_scraped_at = %[3]d, rates_scrape_duration_secs = 5, rates_scrape_state = '{"firstrate":0,"secondrate":0}', rates_checked_at = %[3]d, rates_next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	// second Scrape should not change anything (not even the timestamps) since
	// less than 30 minutes have passed since the last Scrape()
	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), sql.ErrNoRows)
	tr.DBChanges().AssertEmpty()

	// manually mess with one of the ratesScrapeState
	_, err = s.DB.Exec(`UPDATE project_services SET rates_scrape_state = $1 WHERE id = $2`, `{"firstrate":4096,"secondrate":0}`, 1)
	if err != nil {
		t.Fatal(err)
	}
	// this alone should not cause a new scrape
	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), sql.ErrNoRows)
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET rates_scrape_state = '{"firstrate":4096,"secondrate":0}' WHERE id = 1 AND project_id = 1 AND type = 'unittest';
	`)

	// but the changed state will be taken into account when the next scrape is in order
	s.Clock.StepBy(scrapeInterval)
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_rates SET usage_as_bigint = '5129' WHERE service_id = 1 AND name = 'firstrate';
		UPDATE project_rates SET usage_as_bigint = '1034' WHERE service_id = 1 AND name = 'secondrate';
		UPDATE project_rates SET usage_as_bigint = '1033' WHERE service_id = 2 AND name = 'firstrate';
		UPDATE project_rates SET usage_as_bigint = '1034' WHERE service_id = 2 AND name = 'secondrate';
		UPDATE project_services SET rates_scraped_at = %[1]d, rates_scrape_state = '{"firstrate":5120,"secondrate":1024}', rates_checked_at = %[1]d, rates_next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET rates_scraped_at = %[3]d, rates_scrape_state = '{"firstrate":1024,"secondrate":1024}', rates_checked_at = %[3]d, rates_next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	// check data metrics generated by this scraping pass
	registry := prometheus.NewPedanticRegistry()
	amc := &AggregateMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(amc)
	dmc := &DataMetricsCollector{Cluster: s.Cluster, DB: s.DB, ReportZeroes: true}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/ratescrape_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	// check data metrics with the skip_zero flag set
	registry = prometheus.NewPedanticRegistry()
	amc = &AggregateMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(amc)
	dmc = &DataMetricsCollector{Cluster: s.Cluster, DB: s.DB, ReportZeroes: false}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/ratescrape_metrics_skipzero.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
}

func Test_RateScrapeFailure(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testRateScrapeBasicConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	c := getCollector(t, s)
	job := c.RateScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "unittest")

	// this is the error that we expect to appear when ScrapeFails is set
	expectedErrorRx := regexp.MustCompile(`^during rate scrape of project germany/(berlin|dresden): ScrapeRates failed as requested$`)

	// check that ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	// ScrapeRates should not touch the DB when scraping fails
	plugin := s.Cluster.QuotaPlugins["unittest"].(*plugins.GenericQuotaPlugin) //nolint:errcheck
	plugin.ScrapeFails = true
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)

	checkedAt := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET rates_checked_at = %[1]d, rates_scrape_error_message = 'ScrapeRates failed as requested', rates_next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
	`,
		checkedAt.Unix(), checkedAt.Add(recheckInterval).Unix(),
	)
}

func p2window(val limesrates.Window) *limesrates.Window {
	return &val
}

func Test_ScrapeRatesButNoRates(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testNoopConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	c := getCollector(t, s)
	job := c.RateScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "noop")

	// check that ScrapeRates() behaves properly when encountering a quota plugin
	// with no Rates() (in the wild, this can happen because some quota plugins
	// only have Resources())
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt := s.Clock.Now()
	_, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'noop');
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_services (id, project_id, type, rates_scraped_at, rates_scrape_duration_secs, rates_checked_at, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'noop', %[1]d, 5, %[1]d, 0, %[2]d);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
	`,
		scrapedAt.Unix(), scrapedAt.Add(scrapeInterval).Unix(),
	)
}
