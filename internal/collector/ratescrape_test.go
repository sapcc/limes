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
	"errors"
	"math/big"
	"net/http"
	"regexp"
	"testing"
	"time"

	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/plugins"

	"github.com/sapcc/limes/internal/db"

	"github.com/sapcc/go-bits/jobloop"

	"github.com/sapcc/limes/internal/test"
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
				type: liquid
				params:
					area: testing
					test_mode: true
					liquid_service_type: generic-unittest
	`
)

var usageReport = liquid.ServiceUsageReport{
	InfoVersion: 1,
	Rates: map[liquid.RateName]*liquid.RateUsageReport{
		"firstrate": {
			PerAZ: map[liquid.AvailabilityZone]*liquid.AZRateUsageReport{
				"az-one": {
					Usage: big.NewInt(1024),
				},
			},
		},
		"secondrate": {
			PerAZ: map[liquid.AvailabilityZone]*liquid.AZRateUsageReport{
				"az-two": {
					Usage: big.NewInt(2048),
				},
			},
		},
	},
	SerializedState: []byte(`{"firstrate":1024,"secondrate":2048}`),
}

func commonRateScrapeTestSetup(t *testing.T, s *test.Setup) (job jobloop.Job, withLabel jobloop.Option) {
	s.Cluster.QuotaPlugins["unittest"].(*plugins.LiquidQuotaPlugin).LiquidServiceInfo.Rates = map[liquid.RateName]liquid.RateInfo{
		"firstrate":  {},
		"secondrate": {Unit: "KiB"},
	}
	s.Cluster.Config.QuotaDistributionConfigs = []*core.QuotaDistributionConfiguration{
		{
			FullResourceNameRx: "unittest/capacity",
			Model:              "autogrow",
			Autogrow: &core.AutogrowQuotaDistributionConfiguration{
				GrowthMultiplier: 1,
				AllowQuotaOvercommitUntilAllocatedPercent: 0,
			},
		}, {
			FullResourceNameRx: "unittest/things",
			Model:              "autogrow",
			Autogrow: &core.AutogrowQuotaDistributionConfiguration{
				GrowthMultiplier: 1,
				AllowQuotaOvercommitUntilAllocatedPercent: 0,
			},
		},
	}

	s.Cluster.QuotaPlugins["unittest"].(*plugins.LiquidQuotaPlugin).LiquidServiceInfo.Resources = map[liquid.ResourceName]liquid.ResourceInfo{
		"capacity": {
			Unit: liquid.UnitNone,
		},
		"things": {
			Unit: liquid.UnitNone,
		},
	}

	s.Cluster.QuotaPlugins["unittest"].(*plugins.LiquidQuotaPlugin).LiquidClient.(*test.MockLiquidClient).SetUsageReport(usageReport)
	s.Cluster.QuotaPlugins["unittest"].(*plugins.LiquidQuotaPlugin).LiquidClient.(*test.MockLiquidClient).SetCapacityReport(liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"capacity": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": {
						Capacity: 0,
					},
				},
			},
			"things": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": {
						Capacity: 0,
					},
				},
			},
		},
	})
	prepareDomainsAndProjectsForScrape(t, *s)

	c := getCollector(t, *s)
	job = c.RateScrapeJob(s.Registry)
	withLabel = jobloop.WithLabel("service_type", "unittest")
	return
}

func Test_RateScrapeSuccess(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testRateScrapeBasicConfigYAML),
	)
	job, withLabel := commonRateScrapeTestSetup(t, &s)

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
	//nolint:dupword // false positive on "TRUE, TRUE"
	tr0.AssertEqualf(`
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'otherrate', 42, 120000000000, '');
		INSERT INTO project_rates (service_id, name, rate_limit, window_ns, usage_as_bigint) VALUES (1, 'secondrate', 10, 1000000000, '');
		INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'unittest', TRUE, TRUE, 0, 0);
		INSERT INTO project_services (id, project_id, type, stale, rates_stale, next_scrape_at, rates_next_scrape_at) VALUES (2, 2, 'unittest', TRUE, TRUE, 0, 0);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (2, 1, 'dresden', 'uuid-for-dresden', 'uuid-for-berlin');
	`)

	// first Scrape should create the entries
	s.Clock.StepBy(scrapeInterval)
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel)) // twice because there are two projects

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_rates (service_id, name, usage_as_bigint) VALUES (1, 'firstrate', '1024');
		UPDATE project_rates SET usage_as_bigint = '2048' WHERE service_id = 1 AND name = 'secondrate';
		INSERT INTO project_rates (service_id, name, usage_as_bigint) VALUES (2, 'firstrate', '1024');
		INSERT INTO project_rates (service_id, name, usage_as_bigint) VALUES (2, 'secondrate', '2048');
		UPDATE project_services SET rates_scraped_at = %[1]d, rates_stale = FALSE, rates_scrape_duration_secs = 5, rates_scrape_state = '{"firstrate":1024,"secondrate":2048}', rates_checked_at = %[1]d, rates_next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET rates_scraped_at = %[3]d, rates_stale = FALSE, rates_scrape_duration_secs = 5, rates_scrape_state = '{"firstrate":1024,"secondrate":2048}', rates_checked_at = %[3]d, rates_next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
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
	usageReport.Rates["firstrate"].PerAZ["az-one"].Usage = big.NewInt(2048)
	usageReport.Rates["secondrate"].PerAZ["az-two"].Usage = big.NewInt(4096)
	usageReport.SerializedState = []byte(`{"firstrate":2048,"secondrate":4096}`)
	s.Cluster.QuotaPlugins["unittest"].(*plugins.LiquidQuotaPlugin).LiquidClient.(*test.MockLiquidClient).SetUsageReport(usageReport)

	s.Clock.StepBy(scrapeInterval)
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	usageReport.Rates["firstrate"].PerAZ["az-one"].Usage = big.NewInt(4096)
	usageReport.Rates["secondrate"].PerAZ["az-two"].Usage = big.NewInt(8192)
	usageReport.SerializedState = []byte(`{"firstrate":4096,"secondrate":8192}`)
	s.Cluster.QuotaPlugins["unittest"].(*plugins.LiquidQuotaPlugin).LiquidClient.(*test.MockLiquidClient).SetUsageReport(usageReport)

	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_rates SET usage_as_bigint = '2048' WHERE service_id = 1 AND name = 'firstrate';
		UPDATE project_rates SET usage_as_bigint = '4096' WHERE service_id = 1 AND name = 'secondrate';
		UPDATE project_rates SET usage_as_bigint = '4096' WHERE service_id = 2 AND name = 'firstrate';
		UPDATE project_rates SET usage_as_bigint = '8192' WHERE service_id = 2 AND name = 'secondrate';
		UPDATE project_services SET rates_scraped_at = %[1]d, rates_scrape_state = '{"firstrate":2048,"secondrate":4096}', rates_checked_at = %[1]d, rates_next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET rates_scraped_at = %[3]d, rates_scrape_state = '{"firstrate":4096,"secondrate":8192}', rates_checked_at = %[3]d, rates_next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	// check data metrics generated by this scraping pass
	dmr := &DataMetricsReporter{Cluster: s.Cluster, DB: s.DB, ReportZeroes: true}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": contentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/ratescrape_metrics.prom"),
	}.Check(t, dmr)

	// check data metrics with the skip_zero flag set
	dmr = &DataMetricsReporter{Cluster: s.Cluster, DB: s.DB, ReportZeroes: false}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": contentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/ratescrape_metrics_skipzero.prom"),
	}.Check(t, dmr)
}

func Test_RateScrapeFailure(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testRateScrapeBasicConfigYAML),
	)
	job, withLabel := commonRateScrapeTestSetup(t, &s)

	// this is the error that we expect to appear when ScrapeFails is set
	expectedErrorRx := regexp.MustCompile(`^during rate scrape of project germany/(berlin|dresden): GetUsageReport failed as requested$`)

	// check that ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	// ScrapeRates should not touch the DB when scraping fails
	client := s.Cluster.QuotaPlugins["unittest"].(*plugins.LiquidQuotaPlugin).LiquidClient.(*test.MockLiquidClient)
	client.SetUsageReportError(errors.New("GetUsageReport failed as requested"))
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)

	checkedAt := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET rates_stale = FALSE, rates_checked_at = %[1]d, rates_scrape_error_message = 'GetUsageReport failed as requested', rates_next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
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
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_services (id, project_id, type, stale, rates_scraped_at, rates_scrape_duration_secs, rates_checked_at, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'noop', TRUE, %[1]d, 5, %[1]d, 0, %[2]d);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
	`,
		scrapedAt.Unix(), scrapedAt.Add(scrapeInterval).Unix(),
	)
}
