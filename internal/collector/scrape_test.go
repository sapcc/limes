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
	"github.com/sapcc/go-bits/logg"

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
	//ScanDomains is required to create the entries in `domains`,
	//`domain_services`, `projects` and `project_services`
	timeZero := func() time.Time { return time.Unix(0, 0).UTC() }
	_, err := (&Collector{Cluster: s.Cluster, DB: s.DB, MeasureTime: timeZero, AddJitter: test.NoJitter}).ScanDomains(ScanDomainsOpts{})
	if err != nil {
		t.Fatal(err)
	}

	//if we have two projects and bursting is enabled, we are going to test with
	//and without bursting on the two projects
	if s.Cluster.Config.Bursting.MaxMultiplier > 0 {
		_, err := s.DB.Exec(`UPDATE projects SET has_bursting = (name = $1)`, "dresden")
		if err != nil {
			t.Fatal(err)
		}
	}
}

const (
	testScrapeBasicConfigYAML = `
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
	`
)

func Test_ScrapeSuccess(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testScrapeBasicConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	//setup a quota constraint for the projects that we're scraping
	//
	//TODO: duplicated with Test_ScrapeFailure
	//NOTE: This is set only *after* ScanDomains has run, in order to exercise
	//the code path in Scrape() that applies constraints when first creating
	//project_resources entries. If we had set this before ScanDomains, then
	//ScanDomains would already have created the project_resources entries.
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

	//check that ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	//first Scrape should create the entries in `project_resources` with the
	//correct usage and backend quota values (and quota = 0 because nothing was approved yet)
	//and set `project_services.scraped_at` to the current time
	s.Clock.StepBy(scrapeInterval)
	plugin := s.Cluster.QuotaPlugins["unittest"].(*plugins.GenericQuotaPlugin) //nolint:errcheck
	plugin.SetQuotaFails = true
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel)) //twice because there are two projects

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota, physical_usage) VALUES (1, 'capacity', 10, 0, 100, 10, 0);
		INSERT INTO project_resources (service_id, name, usage) VALUES (1, 'capacity_portion', 0);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota) VALUES (1, 'things', 0, 2, 42, '[{"index":0},{"index":1}]', 0);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota, physical_usage) VALUES (2, 'capacity', 10, 0, 100, 12, 0);
		INSERT INTO project_resources (service_id, name, usage) VALUES (2, 'capacity_portion', 0);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota) VALUES (2, 'things', 0, 2, 42, '[{"index":0},{"index":1}]', 0);
		UPDATE project_services SET scraped_at = %[1]d, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":0,"things_usage":2}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":0,"things_usage":2}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	//second Scrape should not change anything (not even the timestamps) since
	//less than 30 minutes have passed since the last Scrape("unittest")
	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), sql.ErrNoRows)
	tr.DBChanges().AssertEmpty()

	//change the data that is reported by the plugin
	s.Clock.StepBy(scrapeInterval)
	plugin.StaticResourceData["capacity"].Quota = 110
	plugin.StaticResourceData["things"].UsageData.Regional.Usage = 5
	//Scrape should pick up the changed resource data
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET backend_quota = 110 WHERE service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET usage = 5, subresources = '[{"index":0},{"index":1},{"index":2},{"index":3},{"index":4}]' WHERE service_id = 1 AND name = 'things';
		UPDATE project_resources SET backend_quota = 110 WHERE service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET usage = 5, subresources = '[{"index":0},{"index":1},{"index":2},{"index":3},{"index":4}]' WHERE service_id = 2 AND name = 'things';
		UPDATE project_services SET scraped_at = %[1]d, serialized_metrics = '{"capacity_usage":0,"things_usage":5}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, serialized_metrics = '{"capacity_usage":0,"things_usage":5}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	//set some new quota values (note that "capacity" already had a non-zero
	//quota because of the cluster.QuotaConstraints)
	_, err := s.DB.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 20, "capacity")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 13, "things")
	if err != nil {
		t.Fatal(err)
	}

	//Scrape should try to enforce quota values in the backend (this did not work
	//until now because the test.Plugin was instructed to have SetQuota fail)
	s.Clock.StepBy(scrapeInterval)
	plugin.SetQuotaFails = false
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET quota = 20, backend_quota = 20, desired_backend_quota = 20 WHERE service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET quota = 13, backend_quota = 13, desired_backend_quota = 13 WHERE service_id = 1 AND name = 'things';
		UPDATE project_resources SET quota = 20, backend_quota = 24, desired_backend_quota = 24 WHERE service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET quota = 13, backend_quota = 15, desired_backend_quota = 15 WHERE service_id = 2 AND name = 'things';
		UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	//another Scrape (with SetQuota disabled again) should show that the quota
	//update was durable
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

	//set a quota that contradicts the cluster.QuotaConstraints
	_, err = s.DB.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 50, "capacity")
	if err != nil {
		t.Fatal(err)
	}

	//Scrape should apply the constraint, then enforce quota values in the backend
	s.Clock.StepBy(scrapeInterval)
	plugin.SetQuotaFails = false
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET quota = 40, backend_quota = 40, desired_backend_quota = 40 WHERE service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET quota = 40, backend_quota = 48, desired_backend_quota = 48 WHERE service_id = 2 AND name = 'capacity';
		UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	//set "capacity" to a non-zero usage to observe a non-zero usage on
	//"capacity_portion" (otherwise this resource has been all zeroes this entire
	//time)
	s.Clock.StepBy(scrapeInterval)
	plugin.StaticResourceData["capacity"].UsageData.Regional.Usage = 20
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET usage = 20, physical_usage = 10 WHERE service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET usage = 5 WHERE service_id = 1 AND name = 'capacity_portion';
		UPDATE project_resources SET usage = 20, physical_usage = 10 WHERE service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET usage = 5 WHERE service_id = 2 AND name = 'capacity_portion';
		UPDATE project_services SET scraped_at = %[1]d, serialized_metrics = '{"capacity_usage":20,"things_usage":5}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, serialized_metrics = '{"capacity_usage":20,"things_usage":5}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	//check data metrics generated by this scraping pass
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

	//check data metrics with the skip_zero flag set
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

	//setup a quota constraint for the projects that we're scraping
	//
	//TODO: duplicated with Test_ScrapeSuccess
	//NOTE: This is set only *after* ScanDomains has run, in order to exercise
	//the code path in Scrape() that applies constraints when first creating
	//project_resources entries. If we had set this before ScanDomains, then
	//ScanDomains would already have created the project_resources entries.
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

	//we will see an expected ERROR during testing, do not make the test fail because of this
	expectedErrorRx := regexp.MustCompile(`^during resource scrape of project germany/(berlin|dresden): Scrape failed as requested$`)

	//check that ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	//failing Scrape should create dummy records to ensure that the API finds
	//plausibly-structured data
	s.Clock.StepBy(scrapeInterval)
	plugin := s.Cluster.QuotaPlugins["unittest"].(*plugins.GenericQuotaPlugin) //nolint:errcheck
	plugin.ScrapeFails = true
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx) //twice because there are two projects

	checkedAt1 := s.Clock.Now().Add(-5 * time.Second)
	checkedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota) VALUES (1, 'capacity', 10, 0, -1, 10);
		INSERT INTO project_resources (service_id, name, usage) VALUES (1, 'capacity_portion', 0);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota) VALUES (1, 'things', 0, 0, -1, 0);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota) VALUES (2, 'capacity', 10, 0, -1, 12);
		INSERT INTO project_resources (service_id, name, usage) VALUES (2, 'capacity_portion', 0);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota) VALUES (2, 'things', 0, 0, -1, 0);
		UPDATE project_services SET scraped_at = 0, checked_at = %[1]d, scrape_error_message = 'Scrape failed as requested', next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = 0, checked_at = %[3]d, scrape_error_message = 'Scrape failed as requested', next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		checkedAt1.Unix(), checkedAt1.Add(recheckInterval).Unix(),
		checkedAt2.Unix(), checkedAt2.Add(recheckInterval).Unix(),
	)

	//next Scrape should yield the same result
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

	//once the backend starts working, we start to see plausible data again
	s.Clock.StepBy(scrapeInterval)
	plugin.ScrapeFails = false
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel)) //twice because there are two projects

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET backend_quota = 100, physical_usage = 0 WHERE service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET usage = 2, backend_quota = 42, subresources = '[{"index":0},{"index":1}]' WHERE service_id = 1 AND name = 'things';
		UPDATE project_resources SET backend_quota = 100, physical_usage = 0 WHERE service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET usage = 2, backend_quota = 42, subresources = '[{"index":0},{"index":1}]' WHERE service_id = 2 AND name = 'things';
		UPDATE project_services SET scraped_at = %[1]d, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":0,"things_usage":2}', checked_at = %[1]d, scrape_error_message = '', next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":0,"things_usage":2}', checked_at = %[3]d, scrape_error_message = '', next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	//backend fails again and we need to scrape because of the stale flag ->
	//touch neither scraped_at nor the existing resources (this also tests that a
	//failed check causes Scrape("unittest") to continue with the next resource afterwards)
	s.Clock.StepBy(scrapeInterval)
	plugin.ScrapeFails = true
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx) //twice because there are two projects

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

const (
	testScrapeCentralizedConfigYAML = `
		discovery:
			method: --test-static
			params:
				domains:
					- { name: germany, id: uuid-for-germany }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
		services:
			- service_type: centralized
				type: --test-generic
		quota_distribution_configs:
			- { resource: centralized/capacity, model: centralized, default_project_quota: 10 }
			- { resource: centralized/things,   model: centralized, default_project_quota: 15 }
		bursting:
			# should not make a difference because CQD ignores bursting
			max_multiplier: 0.2
	`
)

func Test_ScrapeCentralized(t *testing.T) {
	//since all resources in this test operate under centralized quota
	//distribution, bursting makes absolutely no difference
	for _, hasBursting := range []bool{false, true} {
		logg.Info("===== hasBursting = %t =====", hasBursting)

		s := test.NewSetup(t,
			test.WithConfig(testScrapeCentralizedConfigYAML),
		)
		prepareDomainsAndProjectsForScrape(t, s)

		//setup a quota constraint for the project that we're scraping (this is ignored by Test_ScrapeFailure())
		//
		//NOTE: This is set only *after* ScanDomains has run, in order to exercise
		//the code path in Scrape() that applies constraints when first creating
		//project_resources entries. If we had set this before ScanDomains, then
		//ScanDomains would already have created the project_resources entries.
		projectConstraints := core.QuotaConstraints{
			"centralized": {
				"capacity": {Minimum: p2u64(5)},  //below the DefaultProjectQuota, so the DefaultProjectQuota should take precedence
				"things":   {Minimum: p2u64(20)}, //above the DefaultProjectQuota, so the constraint.Minimum should take precedence
			},
		}
		s.Cluster.QuotaConstraints = &core.QuotaConstraintSet{
			Projects: map[string]map[string]core.QuotaConstraints{
				"germany": {"berlin": projectConstraints},
			},
		}

		s.Cluster.Authoritative = true
		c := getCollector(t, s)
		job := c.ResourceScrapeJob(s.Registry)
		withLabel := jobloop.WithLabel("service_type", "centralized")

		//check that ScanDomains created the domain, project and their services and
		//applied the DefaultProjectQuota from the QuotaDistributionConfiguration
		tr, tr0 := easypg.NewTracker(t, s.DB.Db)
		tr0.AssertEqualToFile("fixtures/scrape-centralized0.sql")

		if hasBursting {
			_, err := s.DB.Exec(`UPDATE projects SET has_bursting = TRUE WHERE id = 1`)
			if err != nil {
				t.Fatal(err)
			}
			tr.DBChanges().Ignore()
			s.Cluster.Config.Bursting.MaxMultiplier = 0.2
		}

		//first Scrape creates the remaining project_resources, fills usage and
		//enforces quota constraints (note that both projects behave identically
		//since bursting is ineffective under centralized quota distribution)
		s.Clock.StepBy(scrapeInterval)
		mustT(t, job.ProcessOne(s.Ctx, withLabel))

		scrapedAt := s.Clock.Now()
		tr.DBChanges().AssertEqualf(`
			UPDATE domain_resources SET quota = 10 WHERE service_id = 1 AND name = 'capacity';
			UPDATE domain_resources SET quota = 20 WHERE service_id = 1 AND name = 'things';
			INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota, physical_usage) VALUES (1, 'capacity', 10, 0, 10, 10, 0);
			INSERT INTO project_resources (service_id, name, usage) VALUES (1, 'capacity_portion', 0);
			INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota) VALUES (1, 'things', 20, 2, 20, '[{"index":0},{"index":1}]', 20);
			UPDATE project_services SET scraped_at = %[1]d, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":0,"things_usage":2}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'centralized';
		`,
			scrapedAt.Unix(), scrapedAt.Add(scrapeInterval).Unix(),
		)

		//check that DefaultProjectQuota gets reapplied when the quota is 0 (zero
		//quota on CQD resources is defined to mean "DefaultProjectQuota not
		//applied yet"; this check is also relevant for resources moving from HQD to CQD)
		s.Clock.StepBy(scrapeInterval)
		_, err := s.DB.Exec(`UPDATE project_resources SET quota = 0 WHERE service_id = 1`)
		if err != nil {
			t.Fatal(err)
		}
		mustT(t, job.ProcessOne(s.Ctx, withLabel))

		//because Scrape converges back into the same state, the only change is in the timestamp fields
		scrapedAt = s.Clock.Now()
		tr.DBChanges().AssertEqualf(`
			UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'centralized';
		`,
			scrapedAt.Unix(), scrapedAt.Add(scrapeInterval).Unix(),
		)
	}
}

////////////////////////////////////////////////////////////////////////////////
// test for auto-approval

const (
	testAutoApprovalConfigYAML = `
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

	//ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	//when first scraping, the initial backend quota of the "approve" resource
	//shall be approved automatically
	s.Clock.StepBy(scrapeInterval)
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota) VALUES (1, 'approve', 10, 0, 10, 10);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota) VALUES (1, 'noapprove', 0, 0, 20, 0);
		UPDATE project_services SET scraped_at = %[1]d, scrape_duration_secs = 5, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'autoapprovaltest';
	`,
		scrapedAt.Unix(), scrapedAt.Add(scrapeInterval).Unix(),
	)

	//modify the backend quota; verify that the second scrape does not
	//auto-approve the changed value again (auto-approval is limited to the
	//initial scrape)
	s.Clock.StepBy(scrapeInterval)
	plugin := s.Cluster.QuotaPlugins["autoapprovaltest"].(*plugins.AutoApprovalQuotaPlugin) //nolint:errcheck
	plugin.StaticBackendQuota += 10
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET backend_quota = 20 WHERE service_id = 1 AND name = 'approve';
		UPDATE project_resources SET backend_quota = 30 WHERE service_id = 1 AND name = 'noapprove';
		UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'autoapprovaltest';
	`,
		scrapedAt.Unix(), scrapedAt.Add(scrapeInterval).Unix(),
	)
}

const (
	testNoopConfigYAML = `
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

	//check that Scrape() behaves properly when encountering a quota plugin with
	//no Resources() (in the wild, this can happen because some quota plugins
	//only have Rates())
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
