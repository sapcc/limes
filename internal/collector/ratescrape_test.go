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
	"fmt"
	"regexp"
	"testing"

	"github.com/go-gorp/gorp/v3"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

func Test_RateScrapeSuccess(t *testing.T) {
	rates := []limesrates.RateInfo{
		{Name: "firstrate"},
		{Name: "secondrate", Unit: limes.UnitKibibytes},
	}
	plugin := test.NewPlugin("unittest", rates...)
	cluster, dbm := prepareScrapeTest(t, 2, plugin)
	c := Collector{
		Cluster:  cluster,
		DB:       dbm,
		Plugin:   plugin,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}

	//for one of the projects, put some records in for rate limits, to check that
	//the scraper does not mess with those values
	err := dbm.Insert(&db.ProjectRate{
		ServiceID: 1,
		Name:      "secondrate",
		Limit:     p2u64(10),
		Window:    p2window(1 * limesrates.WindowSeconds),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = dbm.Insert(&db.ProjectRate{
		ServiceID: 1,
		Name:      "otherrate",
		Limit:     p2u64(42),
		Window:    p2window(2 * limesrates.WindowMinutes),
	})
	if err != nil {
		t.Fatal(err)
	}

	//check that ScanDomains created the domain, project and their services; and
	//we set up our initial rates correctly
	tr, tr0 := easypg.NewTracker(t, dbm.Db)
	tr0.AssertEqualf(`
		INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity', 0);
		INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'capacity_portion', 0);
		INSERT INTO domain_resources (service_id, name, quota) VALUES (1, 'things', 0);
		INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unittest');
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'otherrate', 42, 120000000000, '');
		INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'secondrate', 10, 1000000000, '');
		INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'unittest', 0, 0);
		INSERT INTO project_services (id, project_id, type, next_scrape_at, rates_next_scrape_at) VALUES (2, 2, 'unittest', 0, 0);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
	`)

	//first Scrape should create the entries
	c.ScrapeRates()
	c.ScrapeRates() //twice because there are two projects
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_rates (service_id, name, usage_as_bigint) VALUES (1, 'firstrate', '9');
		UPDATE project_rates SET usage_as_bigint = '10' WHERE service_id = 1 AND name = 'secondrate';
		INSERT INTO project_rates (service_id, name, usage_as_bigint) VALUES (2, 'firstrate', '9');
		INSERT INTO project_rates (service_id, name, usage_as_bigint) VALUES (2, 'secondrate', '10');
		UPDATE project_services SET rates_scraped_at = 1, rates_scrape_duration_secs = 1, rates_scrape_state = '{"firstrate":0,"secondrate":0}', rates_checked_at = 1, rates_next_scrape_at = 1801 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET rates_scraped_at = 3, rates_scrape_duration_secs = 1, rates_scrape_state = '{"firstrate":0,"secondrate":0}', rates_checked_at = 3, rates_next_scrape_at = 1803 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	//second Scrape should not change anything (not even the timestamps) since
	//less than 30 minutes have passed since the last Scrape()
	c.ScrapeRates()
	tr.DBChanges().AssertEmpty()

	//manually mess with one of the ratesScrapeState
	_, err = dbm.Exec(`UPDATE project_services SET rates_scrape_state = $1 WHERE id = $2`, `{"firstrate":4096,"secondrate":0}`, 1)
	if err != nil {
		t.Fatal(err)
	}
	//this alone should not cause a new scrape
	c.ScrapeRates()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET rates_scrape_state = '{"firstrate":4096,"secondrate":0}' WHERE id = 1 AND project_id = 1 AND type = 'unittest';
	`)

	//but the changed state will be taken into account when the next scrape is in order
	setProjectServicesRatesStale(t, dbm)
	c.ScrapeRates()
	c.ScrapeRates()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_rates SET usage_as_bigint = '5129' WHERE service_id = 1 AND name = 'firstrate';
		UPDATE project_rates SET usage_as_bigint = '1034' WHERE service_id = 1 AND name = 'secondrate';
		UPDATE project_rates SET usage_as_bigint = '1033' WHERE service_id = 2 AND name = 'firstrate';
		UPDATE project_rates SET usage_as_bigint = '1034' WHERE service_id = 2 AND name = 'secondrate';
		UPDATE project_services SET rates_scraped_at = 7, rates_scrape_state = '{"firstrate":5120,"secondrate":1024}', rates_checked_at = 7, rates_next_scrape_at = 1807 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET rates_scraped_at = 9, rates_scrape_state = '{"firstrate":1024,"secondrate":1024}', rates_checked_at = 9, rates_next_scrape_at = 1809 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	//check data metrics generated by this scraping pass
	registry := prometheus.NewPedanticRegistry()
	amc := &AggregateMetricsCollector{Cluster: cluster, DB: dbm}
	registry.MustRegister(amc)
	dmc := &DataMetricsCollector{Cluster: cluster, DB: dbm, ReportZeroes: true}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/ratescrape_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	//check data metrics with the skip_zero flag set
	registry = prometheus.NewPedanticRegistry()
	amc = &AggregateMetricsCollector{Cluster: cluster, DB: dbm}
	registry.MustRegister(amc)
	dmc = &DataMetricsCollector{Cluster: cluster, DB: dbm, ReportZeroes: false}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/ratescrape_metrics_skipzero.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
}

func Test_RateScrapeFailure(t *testing.T) {
	rates := []limesrates.RateInfo{
		{Name: "firstrate"},
		{Name: "secondrate", Unit: limes.UnitKibibytes},
	}
	plugin := test.NewPlugin("unittest", rates...)
	cluster, dbm := prepareScrapeTest(t, 2, plugin)
	c := Collector{
		Cluster:  cluster,
		DB:       dbm,
		Plugin:   plugin,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}

	//we will see an expected ERROR during testing, do not make the test fail because of this
	expectedErrorRx := regexp.MustCompile(`^scrape unittest rate data for germany/(berlin|dresden) failed: ScrapeRates failed as requested$`)
	c.LogError = func(msg string, args ...interface{}) {
		msg = fmt.Sprintf(msg, args...)
		if expectedErrorRx.MatchString(msg) {
			logg.Info(msg)
		} else {
			t.Error(msg)
		}
	}

	//check that ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, dbm.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	//ScrapeRates should not touch the DB when scraping fails
	plugin.ScrapeFails = true
	c.ScrapeRates()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET rates_scrape_duration_secs = 1, rates_checked_at = 1, rates_scrape_error_message = 'ScrapeRates failed as requested', rates_next_scrape_at = 301 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
	`)
}

func setProjectServicesRatesStale(t *testing.T, dbm *gorp.DbMap) {
	t.Helper()
	//make sure that the project is scraped again
	_, err := dbm.Exec(`UPDATE project_services SET rates_stale = $1`, true)
	if err != nil {
		t.Fatal(err)
	}
}

func p2window(val limesrates.Window) *limesrates.Window {
	return &val
}

func Test_ScrapeRatesButNoRates(t *testing.T) {
	plugin := noopQuotaPlugin{}
	cluster, dbm := prepareScrapeTest(t, 1, plugin)
	c := Collector{
		Cluster:  cluster,
		DB:       dbm,
		Plugin:   plugin,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}

	//check that ScrapeRates() behaves properly when encountering a quota plugin
	//with no Rates() (in the wild, this can happen because some quota plugins
	//only have Resources())
	c.ScrapeRates()
	_, tr0 := easypg.NewTracker(t, dbm.Db)
	tr0.AssertEqualf(`
		INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'noop');
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_services (id, project_id, type, rates_scraped_at, rates_scrape_duration_secs, rates_checked_at, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'noop', 1, 1, 1, 0, 1801);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
	`)
}
