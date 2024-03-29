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
	"regexp"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/jobloop"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/plugins"
)

func mustT(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustFailT(t *testing.T, err, expected error) {
	t.Helper()
	if err == nil {
		t.Errorf("expected to fail with %q, but got no error", expected.Error())
	} else if err.Error() != expected.Error() {
		t.Errorf("expected to fail with %q, but failed with %q", expected.Error(), err.Error())
	}
}

func mustFailLikeT(t *testing.T, err error, rx *regexp.Regexp) {
	t.Helper()
	if err == nil {
		t.Errorf("expected to fail with %q, but got no error", rx.String())
	} else if !rx.MatchString(err.Error()) {
		t.Errorf("expected to fail with %q, but failed with %q", rx.String(), err.Error())
	}
}

func p2u64(x uint64) *uint64 {
	return &x
}

func prepareDomainsAndProjectsForScrape(t *testing.T, s test.Setup) {
	// ScanDomains is required to create the entries in `domains`,
	// `domain_services`, `projects` and `project_services`
	timeZero := func() time.Time { return time.Unix(0, 0).UTC() }
	_, err := (&Collector{Cluster: s.Cluster, DB: s.DB, MeasureTime: timeZero, AddJitter: test.NoJitter}).ScanDomains(ScanDomainsOpts{})
	if err != nil {
		t.Fatal(err)
	}

	// if we have two projects and bursting is enabled, we are going to test with
	// and without bursting on the two projects
	if s.Cluster.Config.Bursting.MaxMultiplier > 0 {
		_, err := s.DB.Exec(`UPDATE projects SET has_bursting = (name = $1)`, "dresden")
		if err != nil {
			t.Fatal(err)
		}
	}
}

const (
	testScrapeBasicConfigYAML = `
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
		bursting:
			max_multiplier: 0.2
		quota_distribution_configs:
			# this is only used to check that historical_usage is tracked
			- { resource: unittest/things, model: autogrow, autogrow: { growth_multiplier: 1.0, usage_data_retention_period: 48h } }
	`
)

func Test_ScrapeSuccess(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testScrapeBasicConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	// setup a quota constraint for the projects that we're scraping
	//
	//TODO: duplicated with Test_ScrapeFailure
	//NOTE: This is set only *after* ScanDomains has run, in order to exercise
	// the code path in Scrape() that applies constraints when first creating
	// project_resources entries. If we had set this before ScanDomains, then
	// ScanDomains would already have created the project_resources entries.
	projectConstraints := core.QuotaConstraints{
		"unittest": {
			"capacity": {Minimum: p2u64(10), Maximum: p2u64(40)},
		},
	}
	s.Cluster.QuotaConstraints = &core.QuotaConstraintSet{
		Projects: map[string]map[string]core.QuotaConstraints{
			"germany": {
				"berlin":  projectConstraints,
				"dresden": projectConstraints,
			},
		},
	}

	s.Cluster.Authoritative = true
	c := getCollector(t, s)
	job := c.ResourceScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "unittest")

	// check that ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	// first Scrape should create the entries in `project_resources` with the
	// correct usage and backend quota values (and quota = 0 because nothing was approved yet)
	// and set `project_services.scraped_at` to the current time
	s.Clock.StepBy(scrapeInterval)
	plugin := s.Cluster.QuotaPlugins["unittest"].(*plugins.GenericQuotaPlugin) //nolint:errcheck
	plugin.SetQuotaFails = true
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel)) // twice because there are two projects

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (1, 1, 'any', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (10, 4, 'any', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (11, 4, 'az-one', 0, 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (12, 4, 'az-two', 0, 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (13, 5, 'any', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (14, 5, 'az-one', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (15, 5, 'az-two', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (16, 6, 'any', 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (17, 6, 'az-one', 2, '[{"index":0},{"index":1}]', '{"t":[%[3]d],"v":[2]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (18, 6, 'az-two', 2, '[{"index":2},{"index":3}]', '{"t":[%[3]d],"v":[2]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (2, 1, 'az-one', 0, 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (3, 1, 'az-two', 0, 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (4, 2, 'any', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (5, 2, 'az-one', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (6, 2, 'az-two', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (7, 3, 'any', 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (8, 3, 'az-one', 2, '[{"index":0},{"index":1}]', '{"t":[%[1]d],"v":[2]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (9, 3, 'az-two', 2, '[{"index":2},{"index":3}]', '{"t":[%[1]d],"v":[2]}');
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (1, 1, 'capacity', 10, 100, 10);
		INSERT INTO project_resources (id, service_id, name) VALUES (2, 1, 'capacity_portion');
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (3, 1, 'things', 0, 42, 0);
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (4, 2, 'capacity', 10, 100, 12);
		INSERT INTO project_resources (id, service_id, name) VALUES (5, 2, 'capacity_portion');
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (6, 2, 'things', 0, 42, 0);
		UPDATE project_services SET scraped_at = %[1]d, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":0,"things_usage":4}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":0,"things_usage":4}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)
	firstScrapedAt1 := scrapedAt1
	firstScrapedAt2 := scrapedAt2

	// second Scrape should not change anything (not even the timestamps) since
	// less than 30 minutes have passed since the last Scrape("unittest")
	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), sql.ErrNoRows)
	tr.DBChanges().AssertEmpty()

	// change the data that is reported by the plugin
	s.Clock.StepBy(scrapeInterval)
	plugin.StaticResourceData["capacity"].Quota = 110
	plugin.StaticResourceData["things"].UsageData["az-two"].Usage = 3
	// Scrape should pick up the changed resource data
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET usage = 3, subresources = '[{"index":2},{"index":3},{"index":4}]', historical_usage = '{"t":[%[6]d,%[3]d],"v":[2,3]}' WHERE id = 18 AND resource_id = 6 AND az = 'az-two';
		UPDATE project_az_resources SET usage = 3, subresources = '[{"index":2},{"index":3},{"index":4}]', historical_usage = '{"t":[%[5]d,%[1]d],"v":[2,3]}' WHERE id = 9 AND resource_id = 3 AND az = 'az-two';
		UPDATE project_resources SET backend_quota = 110 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET backend_quota = 110 WHERE id = 4 AND service_id = 2 AND name = 'capacity';
		UPDATE project_services SET scraped_at = %[1]d, serialized_metrics = '{"capacity_usage":0,"things_usage":5}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, serialized_metrics = '{"capacity_usage":0,"things_usage":5}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
		firstScrapedAt1.Unix(), firstScrapedAt2.Unix(),
	)

	// check reporting of MinQuotaFromBackend/MaxQuotaFromBackend
	s.Clock.StepBy(scrapeInterval)
	plugin.MinQuota = map[string]uint64{"capacity": 10}
	plugin.MaxQuota = map[string]uint64{"things": 1000}
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET min_quota_from_backend = 10 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET max_quota_from_backend = 1000 WHERE id = 3 AND service_id = 1 AND name = 'things';
		UPDATE project_resources SET min_quota_from_backend = 10 WHERE id = 4 AND service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET max_quota_from_backend = 1000 WHERE id = 6 AND service_id = 2 AND name = 'things';
		UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	// check quota overrides
	s.Clock.StepBy(scrapeInterval)
	s.Cluster.QuotaOverrides = map[string]map[string]map[string]map[string]uint64{
		"germany": {
			"berlin": {
				"unittest": {
					"capacity": 10,
					"things":   1000,
				},
			},
		},
	}
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET override_quota_from_config = 10 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET override_quota_from_config = 1000 WHERE id = 3 AND service_id = 1 AND name = 'things';
		UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	// set some new quota values (note that "capacity" already had a non-zero
	// quota because of the cluster.QuotaConstraints)
	_, err := s.DB.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 20, "capacity")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 13, "things")
	if err != nil {
		t.Fatal(err)
	}

	// Scrape should try to enforce quota values in the backend (this did not work
	// until now because the test.Plugin was instructed to have SetQuota fail);
	// also the quota change above causes ApplyComputedDomainQuota to have an effect here
	s.Clock.StepBy(scrapeInterval)
	plugin.SetQuotaFails = false
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE domain_resources SET quota = 26 WHERE id = 3 AND service_id = 1 AND name = 'things';
		UPDATE project_resources SET quota = 20, backend_quota = 20, desired_backend_quota = 20 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET quota = 13, backend_quota = 13, desired_backend_quota = 13 WHERE id = 3 AND service_id = 1 AND name = 'things';
		UPDATE project_resources SET quota = 20, backend_quota = 24, desired_backend_quota = 24 WHERE id = 4 AND service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET quota = 13, backend_quota = 13, desired_backend_quota = 13 WHERE id = 6 AND service_id = 2 AND name = 'things';
		UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	// another Scrape (with SetQuota disabled again) should show that the quota
	// update was durable
	s.Clock.StepBy(scrapeInterval)
	plugin.SetQuotaFails = true
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	// set a quota that contradicts the cluster.QuotaConstraints
	_, err = s.DB.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 50, "capacity")
	if err != nil {
		t.Fatal(err)
	}

	// Scrape should apply the constraint, then enforce quota values in the backend
	s.Clock.StepBy(scrapeInterval)
	plugin.SetQuotaFails = false
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET quota = 40, backend_quota = 40, desired_backend_quota = 40 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET quota = 40, backend_quota = 48, desired_backend_quota = 48 WHERE id = 4 AND service_id = 2 AND name = 'capacity';
		UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	// set "capacity" to a non-zero usage to observe a non-zero usage on
	// "capacity_portion" (otherwise this resource has been all zeroes this entire
	// time)
	s.Clock.StepBy(scrapeInterval)
	plugin.StaticResourceData["capacity"].UsageData["az-one"].Usage = 20
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET usage = 20, physical_usage = 10 WHERE id = 11 AND resource_id = 4 AND az = 'az-one';
		UPDATE project_az_resources SET usage = 5 WHERE id = 14 AND resource_id = 5 AND az = 'az-one';
		UPDATE project_az_resources SET usage = 20, physical_usage = 10 WHERE id = 2 AND resource_id = 1 AND az = 'az-one';
		UPDATE project_az_resources SET usage = 5 WHERE id = 5 AND resource_id = 2 AND az = 'az-one';
		UPDATE project_services SET scraped_at = %[1]d, serialized_metrics = '{"capacity_usage":20,"things_usage":5}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, serialized_metrics = '{"capacity_usage":20,"things_usage":5}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	// check data metrics generated by this scraping pass
	registry := prometheus.NewPedanticRegistry()
	amc := &AggregateMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(amc)
	pmc := &QuotaPluginMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(pmc)
	dmc := &DataMetricsCollector{Cluster: s.Cluster, DB: s.DB, ReportZeroes: true}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/scrape_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	// check data metrics with the skip_zero flag set
	registry = prometheus.NewPedanticRegistry()
	amc = &AggregateMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(amc)
	pmc = &QuotaPluginMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(pmc)
	dmc = &DataMetricsCollector{Cluster: s.Cluster, DB: s.DB, ReportZeroes: false}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/scrape_metrics_skipzero.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
}

func Test_ScrapeFailure(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testScrapeBasicConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	// setup a quota constraint for the projects that we're scraping
	//
	//TODO: duplicated with Test_ScrapeSuccess
	//NOTE: This is set only *after* ScanDomains has run, in order to exercise
	// the code path in Scrape() that applies constraints when first creating
	// project_resources entries. If we had set this before ScanDomains, then
	// ScanDomains would already have created the project_resources entries.
	projectConstraints := core.QuotaConstraints{
		"unittest": {
			"capacity": {Minimum: p2u64(10), Maximum: p2u64(40)},
		},
	}
	s.Cluster.QuotaConstraints = &core.QuotaConstraintSet{
		Projects: map[string]map[string]core.QuotaConstraints{
			"germany": {
				"berlin":  projectConstraints,
				"dresden": projectConstraints,
			},
		},
	}

	c := getCollector(t, s)
	job := c.ResourceScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "unittest")

	// we will see an expected ERROR during testing, do not make the test fail because of this
	expectedErrorRx := regexp.MustCompile(`^during resource scrape of project germany/(berlin|dresden): Scrape failed as requested$`)

	// check that ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	// failing Scrape should create dummy records to ensure that the API finds
	// plausibly-structured data
	s.Clock.StepBy(scrapeInterval)
	plugin := s.Cluster.QuotaPlugins["unittest"].(*plugins.GenericQuotaPlugin) //nolint:errcheck
	plugin.ScrapeFails = true
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx) // twice because there are two projects

	checkedAt1 := s.Clock.Now().Add(-5 * time.Second)
	checkedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (1, 1, 'any', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (2, 2, 'any', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (3, 3, 'any', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (4, 4, 'any', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (5, 5, 'any', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (6, 6, 'any', 0);
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (1, 1, 'capacity', 10, -1, 10);
		INSERT INTO project_resources (id, service_id, name) VALUES (2, 1, 'capacity_portion');
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (3, 1, 'things', 0, -1, 0);
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (4, 2, 'capacity', 10, -1, 12);
		INSERT INTO project_resources (id, service_id, name) VALUES (5, 2, 'capacity_portion');
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (6, 2, 'things', 0, -1, 0);
		UPDATE project_services SET scraped_at = 0, checked_at = %[1]d, scrape_error_message = 'Scrape failed as requested', next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = 0, checked_at = %[3]d, scrape_error_message = 'Scrape failed as requested', next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		checkedAt1.Unix(), checkedAt1.Add(recheckInterval).Unix(),
		checkedAt2.Unix(), checkedAt2.Add(recheckInterval).Unix(),
	)

	// next Scrape should yield the same result
	s.Clock.StepBy(scrapeInterval)
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)

	checkedAt1 = s.Clock.Now().Add(-5 * time.Second)
	checkedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		checkedAt1.Unix(), checkedAt1.Add(recheckInterval).Unix(),
		checkedAt2.Unix(), checkedAt2.Add(recheckInterval).Unix(),
	)

	// once the backend starts working, we start to see plausible data again
	s.Clock.StepBy(scrapeInterval)
	plugin.ScrapeFails = false
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel)) // twice because there are two projects

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (10, 2, 'az-two', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (11, 3, 'az-one', 2, '[{"index":0},{"index":1}]', '{"t":[%[1]d],"v":[2]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (12, 3, 'az-two', 2, '[{"index":2},{"index":3}]', '{"t":[%[1]d],"v":[2]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (13, 4, 'az-one', 0, 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (14, 4, 'az-two', 0, 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (15, 5, 'az-one', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (16, 5, 'az-two', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (17, 6, 'az-one', 2, '[{"index":0},{"index":1}]', '{"t":[%[3]d],"v":[2]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (18, 6, 'az-two', 2, '[{"index":2},{"index":3}]', '{"t":[%[3]d],"v":[2]}');
		UPDATE project_az_resources SET historical_usage = '{"t":[%[1]d],"v":[0]}' WHERE id = 3 AND resource_id = 3 AND az = 'any';
		UPDATE project_az_resources SET historical_usage = '{"t":[%[3]d],"v":[0]}' WHERE id = 6 AND resource_id = 6 AND az = 'any';
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (7, 1, 'az-one', 0, 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage) VALUES (8, 1, 'az-two', 0, 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (9, 2, 'az-one', 0);
		UPDATE project_resources SET backend_quota = 100 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET backend_quota = 42 WHERE id = 3 AND service_id = 1 AND name = 'things';
		UPDATE project_resources SET backend_quota = 100 WHERE id = 4 AND service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET backend_quota = 42 WHERE id = 6 AND service_id = 2 AND name = 'things';
		UPDATE project_services SET scraped_at = %[1]d, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":0,"things_usage":4}', checked_at = %[1]d, scrape_error_message = '', next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":0,"things_usage":4}', checked_at = %[3]d, scrape_error_message = '', next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	// backend fails again and we need to scrape because of the stale flag ->
	// touch neither scraped_at nor the existing resources (this also tests that a
	// failed check causes Scrape("unittest") to continue with the next resource afterwards)
	s.Clock.StepBy(scrapeInterval)
	plugin.ScrapeFails = true
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx) // twice because there are two projects

	checkedAt1 = s.Clock.Now().Add(-5 * time.Second)
	checkedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET checked_at = %[1]d, scrape_error_message = 'Scrape failed as requested', next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET checked_at = %[3]d, scrape_error_message = 'Scrape failed as requested', next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		checkedAt1.Unix(), checkedAt1.Add(recheckInterval).Unix(),
		checkedAt2.Unix(), checkedAt2.Add(recheckInterval).Unix(),
	)
}

////////////////////////////////////////////////////////////////////////////////
// test for auto-approval

const (
	testAutoApprovalConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: --test-static
			params:
				domains:
					- { name: germany, id: uuid-for-germany }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
		services:
			- service_type: autoapprovaltest
				type: --test-auto-approval
				params:
					static_backend_quota: 10
	`
)

func Test_AutoApproveInitialQuota(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testAutoApprovalConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	c := getCollector(t, s)
	job := c.ResourceScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "autoapprovaltest")

	// ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	// when first scraping, the initial backend quota of the "approve" resource
	// shall be approved automatically
	s.Clock.StepBy(scrapeInterval)
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (1, 1, 'any', 0);
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (2, 2, 'any', 0);
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (1, 1, 'approve', 10, 10, 10);
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (2, 1, 'noapprove', 0, 20, 0);
		UPDATE project_services SET scraped_at = %[1]d, scrape_duration_secs = 5, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'autoapprovaltest';
	`,
		scrapedAt.Unix(), scrapedAt.Add(scrapeInterval).Unix(),
	)

	// modify the backend quota; verify that the second scrape does not
	// auto-approve the changed value again (auto-approval is limited to the
	// initial scrape)
	s.Clock.StepBy(scrapeInterval)
	plugin := s.Cluster.QuotaPlugins["autoapprovaltest"].(*plugins.AutoApprovalQuotaPlugin) //nolint:errcheck
	plugin.StaticBackendQuota += 10
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET backend_quota = 20 WHERE id = 1 AND service_id = 1 AND name = 'approve';
		UPDATE project_resources SET backend_quota = 30 WHERE id = 2 AND service_id = 1 AND name = 'noapprove';
		UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'autoapprovaltest';
	`,
		scrapedAt.Unix(), scrapedAt.Add(scrapeInterval).Unix(),
	)
}

const (
	testNoopConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: --test-static
			params:
				domains:
					- { name: germany, id: uuid-for-germany }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
		services:
			- service_type: noop
				type: --test-noop
	`
)

//nolint:dupl
func Test_ScrapeButNoResources(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testNoopConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	c := getCollector(t, s)
	job := c.ResourceScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "noop")

	// check that Scrape() behaves properly when encountering a quota plugin with
	// no Resources() (in the wild, this can happen because some quota plugins
	// only have Rates())
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt := s.Clock.Now()
	_, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'noop');
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_services (id, project_id, type, scraped_at, scrape_duration_secs, checked_at, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'noop', %[1]d, 5, %[1]d, %[2]d, 0);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
	`,
		scrapedAt.Unix(), scrapedAt.Add(scrapeInterval).Unix(),
	)
}

////////////////////////////////////////////////////////////////////////////////
// test for empty UsageData

const (
	testNoUsageDataConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: --test-static
			params:
				domains:
					- { name: germany, id: uuid-for-germany }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
		services:
			- service_type: noop
				type: --test-noop
				params:
					with_empty_resource: true
	`
)

//nolint:dupl
func Test_ScrapeReturnsNoUsageData(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testNoUsageDataConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	c := getCollector(t, s)
	job := c.ResourceScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "noop")

	// check that Scrape() behaves properly when encountering a quota plugin with
	// no Resources() (in the wild, this can happen because some quota plugins
	// only have Rates())
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt := s.Clock.Now()
	_, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO domain_resources (id, service_id, name, quota) VALUES (1, 1, 'things', 0);
		INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'noop');
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (1, 1, 'any', 0);
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota, desired_backend_quota) VALUES (1, 1, 'things', 0, 0, 0);
		INSERT INTO project_services (id, project_id, type, scraped_at, scrape_duration_secs, checked_at, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'noop', %[1]d, 5, %[1]d, %[2]d, 0);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
	`,
		scrapedAt.Unix(), scrapedAt.Add(scrapeInterval).Unix(),
	)
}
