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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/test"
)

func Test_RateScrapeSuccess(t *testing.T) {
	rates := []limes.RateInfo{
		{Name: "firstrate"},
		{Name: "secondrate", Unit: limes.UnitKibibytes},
	}
	plugin := test.NewPlugin("unittest", rates...)
	cluster := prepareScrapeTest(t, 2, plugin)
	c := Collector{
		Cluster:  cluster,
		Plugin:   plugin,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}

	//for one of the projects, put some records in for rate limits, to check that
	//the scraper does not mess with those values
	err := db.DB.Insert(&db.ProjectRate{
		ServiceID: 1,
		Name:      "secondrate",
		Limit:     p2u64(10),
		Window:    p2window(1 * limes.WindowSeconds),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.DB.Insert(&db.ProjectRate{
		ServiceID: 1,
		Name:      "otherrate",
		Limit:     p2u64(42),
		Window:    p2window(2 * limes.WindowMinutes),
	})
	if err != nil {
		t.Fatal(err)
	}

	//check that ScanDomains created the domain, project and their services; and
	//we set up our initial rates correctly
	tr, tr0 := easypg.NewTracker(t, db.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'unittest');
		INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');
		INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'otherrate', 42, 120000000000, '');
		INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'secondrate', 10, 1000000000, '');
		INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics) VALUES (1, 1, 'unittest', NULL, FALSE, 0, NULL, FALSE, 0, '', '');
		INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics) VALUES (2, 2, 'unittest', NULL, FALSE, 0, NULL, FALSE, 0, '', '');
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin', TRUE);
	`)

	//first Scrape should create the entries
	c.ScrapeRates()
	c.ScrapeRates() //twice because there are two projects
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'firstrate', NULL, NULL, '9');
		UPDATE project_rates SET usage_as_bigint = '10' WHERE service_id = 1 AND name = 'secondrate';
		INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (2, 'firstrate', NULL, NULL, '9');
		INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (2, 'secondrate', NULL, NULL, '10');
		UPDATE project_services SET rates_scraped_at = 1, rates_scrape_duration_secs = 1, rates_scrape_state = '{"firstrate":0,"secondrate":0}' WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET rates_scraped_at = 3, rates_scrape_duration_secs = 1, rates_scrape_state = '{"firstrate":0,"secondrate":0}' WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	//second Scrape should not change anything (not even the timestamps) since
	//less than 30 minutes have passed since the last Scrape()
	c.ScrapeRates()
	tr.DBChanges().AssertEmpty()

	//manually mess with one of the ratesScrapeState
	_, err = db.DB.Exec(`UPDATE project_services SET rates_scrape_state = $1 WHERE id = $2`, `{"firstrate":4096,"secondrate":0}`, 1)
	if err != nil {
		t.Fatal(err)
	}
	//this alone should not cause a new scrape
	c.ScrapeRates()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET rates_scrape_state = '{"firstrate":4096,"secondrate":0}' WHERE id = 1 AND project_id = 1 AND type = 'unittest';
	`)

	//but the changed state will be taken into account when the next scrape is in order
	setProjectServicesRatesStale(t)
	c.ScrapeRates()
	c.ScrapeRates()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_rates SET usage_as_bigint = '5129' WHERE service_id = 1 AND name = 'firstrate';
		UPDATE project_rates SET usage_as_bigint = '1034' WHERE service_id = 1 AND name = 'secondrate';
		UPDATE project_rates SET usage_as_bigint = '1033' WHERE service_id = 2 AND name = 'firstrate';
		UPDATE project_rates SET usage_as_bigint = '1034' WHERE service_id = 2 AND name = 'secondrate';
		UPDATE project_services SET rates_scraped_at = 7, rates_scrape_state = '{"firstrate":5120,"secondrate":1024}' WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET rates_scraped_at = 9, rates_scrape_state = '{"firstrate":1024,"secondrate":1024}' WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	//check data metrics generated by this scraping pass
	registry := prometheus.NewPedanticRegistry()
	amc := &AggregateMetricsCollector{Cluster: cluster}
	registry.MustRegister(amc)
	dmc := &DataMetricsCollector{Cluster: cluster, ReportZeroes: true}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/ratescrape_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	//check data metrics with the skip_zero flag set
	registry = prometheus.NewPedanticRegistry()
	amc = &AggregateMetricsCollector{Cluster: cluster}
	registry.MustRegister(amc)
	dmc = &DataMetricsCollector{Cluster: cluster, ReportZeroes: false}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/ratescrape_metrics_skipzero.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
}

func Test_RateScrapeFailure(t *testing.T) {
	rates := []limes.RateInfo{
		{Name: "firstrate"},
		{Name: "secondrate", Unit: limes.UnitKibibytes},
	}
	plugin := test.NewPlugin("unittest", rates...)
	cluster := prepareScrapeTest(t, 2, plugin)
	c := Collector{
		Cluster:  cluster,
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
	tr, tr0 := easypg.NewTracker(t, db.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	//ScrapeRates should not touch the DB when scraping fails
	plugin.ScrapeFails = true
	c.ScrapeRates()
	tr.DBChanges().AssertEmpty()
}

func setProjectServicesRatesStale(t *testing.T) {
	t.Helper()
	//make sure that the project is scraped again
	_, err := db.DB.Exec(`UPDATE project_services SET rates_stale = $1`, true)
	if err != nil {
		t.Fatal(err)
	}
}

func p2window(val limes.Window) *limes.Window {
	return &val
}

func Test_ScrapeRatesButNoRates(t *testing.T) {
	plugin := noopQuotaPlugin{}
	cluster := prepareScrapeTest(t, 1, plugin)
	c := Collector{
		Cluster:  cluster,
		Plugin:   plugin,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}

	//check that ScrapeRates() behaves properly when encountering a quota plugin
	//with no Rates() (in the wild, this can happen because some quota plugins
	//only have Resources())
	c.ScrapeRates()
	_, tr0 := easypg.NewTracker(t, db.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'noop');
		INSERT INTO domains (id, cluster_id, name, uuid) VALUES (1, 'west', 'germany', 'uuid-for-germany');
		INSERT INTO project_services (id, project_id, type, scraped_at, stale, scrape_duration_secs, rates_scraped_at, rates_stale, rates_scrape_duration_secs, rates_scrape_state, serialized_metrics) VALUES (1, 1, 'noop', NULL, FALSE, 0, 1, FALSE, 1, '', '');
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
	`)
}
