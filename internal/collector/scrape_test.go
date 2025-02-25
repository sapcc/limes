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
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/jobloop"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/plugins"
	"github.com/sapcc/limes/internal/test"
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

func p2i64(x int64) *int64 {
	return &x
}

func prepareDomainsAndProjectsForScrape(t *testing.T, s test.Setup) {
	// ScanDomains is required to create the entries in `domains`, `projects` and `project_services`
	timeZero := func() time.Time { return time.Unix(0, 0).UTC() }
	_, err := (&Collector{Cluster: s.Cluster, DB: s.DB, MeasureTime: timeZero, AddJitter: test.NoJitter}).ScanDomains(s.Ctx, ScanDomainsOpts{})
	if err != nil {
		t.Fatal(err)
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
				type: liquid
				params:
					area: testing
					test_mode: true
					liquid_service_type: generic-unittest
		quota_distribution_configs:
			# this is only used to check that historical_usage is tracked
			- { resource: unittest/things, model: autogrow, autogrow: { growth_multiplier: 1.0, usage_data_retention_period: 48h } }
	`
)

func commonComplexScrapeTestSetup(t *testing.T, nonCapacityPortionResourceTopology liquid.ResourceTopology) (s test.Setup, scrapeJob jobloop.Job, withLabel jobloop.Option, syncJob jobloop.Job, plugin *plugins.LiquidQuotaPlugin, serviceInfo liquid.ServiceInfo, serviceUsageReport liquid.ServiceUsageReport) {
	s = test.NewSetup(t,
		test.WithConfig(testScrapeBasicConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	c := getCollector(t, s)
	scrapeJob = c.ResourceScrapeJob(s.Registry)
	withLabel = jobloop.WithLabel("service_type", "unittest")
	syncJob = c.SyncQuotaToBackendJob(s.Registry)
	plugin = s.Cluster.QuotaPlugins["unittest"].(*plugins.LiquidQuotaPlugin)

	serviceInfo = liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"capacity": {
				Unit:                liquid.UnitBytes,
				Topology:            nonCapacityPortionResourceTopology,
				HasCapacity:         true,
				HasQuota:            true,
				NeedsResourceDemand: true,
			},
			"capacity_portion": {
				Unit:     liquid.UnitBytes,
				HasQuota: false,
				Topology: liquid.AZAwareResourceTopology,
			},
			"things": {
				Unit:        liquid.UnitNone,
				Topology:    nonCapacityPortionResourceTopology,
				HasCapacity: false,
				HasQuota:    true,
			},
		},
		UsageMetricFamilies: map[liquid.MetricName]liquid.MetricFamilyInfo{
			"capacity_usage": {Type: liquid.MetricTypeGauge},
			"things_usage":   {Type: liquid.MetricTypeGauge},
		},
	}

	serviceUsageReport = liquid.ServiceUsageReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceUsageReport{
			"capacity": {
				Quota: pointerTo(int64(100)),
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
					"az-one": {
						Usage:         0,
						Quota:         pointerTo(int64(50)),
						PhysicalUsage: new(uint64),
					},
					"az-two": {
						Usage:         0,
						Quota:         pointerTo(int64(50)),
						PhysicalUsage: new(uint64),
					},
				},
			},
			"capacity_portion": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
					"az-one": {
						Usage: 0,
					},
					"az-two": {
						Usage: 0,
					},
				},
			},
			"things": {
				Quota: pointerTo(int64(42)),
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
					"az-one": {
						Usage: 2,
						Quota: pointerTo(int64(21)),
						Subresources: []liquid.Subresource{
							{
								Name:  "index",
								Usage: pointerTo(uint64(0)),
							},
							{
								Name:  "index",
								Usage: pointerTo(uint64(1)),
							},
						},
					},
					"az-two": {
						Usage: 2,
						Quota: pointerTo(int64(21)),
						Subresources: []liquid.Subresource{
							{
								Name:  "index",
								Usage: pointerTo(uint64(2)),
							},
							{
								Name:  "index",
								Usage: pointerTo(uint64(3)),
							},
						},
					},
				},
			},
		},
		Metrics: map[liquid.MetricName][]liquid.Metric{
			"capacity_usage": {{Value: 0}},
			"things_usage":   {{Value: 4}},
		},
	}
	return
}

func Test_ScrapeSuccess(t *testing.T) {
	s, job, withLabel, syncJob, plugin, serviceInfo, serviceUsageReport := commonComplexScrapeTestSetup(t, liquid.AZAwareResourceTopology)
	plugin.LiquidServiceInfo = serviceInfo
	plugin.LiquidClient.(*core.MockLiquidClient).SetUsageReport(serviceUsageReport)

	// check that ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	// first Scrape should create the entries in `project_resources` with the
	// correct usage and backend quota values (and quota = 0 because no ACPQ has run yet)
	// and set `project_services.scraped_at` to the current time;
	// a desync should be noted, but we will not run syncJob until later in this test
	s.Clock.StepBy(scrapeInterval)
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel)) // twice because there are two projects

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (1, 1, 'any', 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (10, 4, 'any', 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage, historical_usage) VALUES (11, 4, 'az-one', 0, 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage, historical_usage) VALUES (12, 4, 'az-two', 0, 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (13, 5, 'any', 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (14, 5, 'az-one', 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (15, 5, 'az-two', 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (16, 6, 'any', 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (17, 6, 'az-one', 2, '[{"name":"index","usage":0},{"name":"index","usage":1}]', '{"t":[%[3]d],"v":[2]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (18, 6, 'az-two', 2, '[{"name":"index","usage":2},{"name":"index","usage":3}]', '{"t":[%[3]d],"v":[2]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage, historical_usage) VALUES (2, 1, 'az-one', 0, 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage, historical_usage) VALUES (3, 1, 'az-two', 0, 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (4, 2, 'any', 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (5, 2, 'az-one', 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (6, 2, 'az-two', 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (7, 3, 'any', 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (8, 3, 'az-one', 2, '[{"name":"index","usage":0},{"name":"index","usage":1}]', '{"t":[%[1]d],"v":[2]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (9, 3, 'az-two', 2, '[{"name":"index","usage":2},{"name":"index","usage":3}]', '{"t":[%[1]d],"v":[2]}');
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (1, 1, 'capacity', 0, 100);
		INSERT INTO project_resources (id, service_id, name) VALUES (2, 1, 'capacity_portion');
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (3, 1, 'things', 0, 42);
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (4, 2, 'capacity', 0, 100);
		INSERT INTO project_resources (id, service_id, name) VALUES (5, 2, 'capacity_portion');
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (6, 2, 'things', 0, 42);
		UPDATE project_services SET scraped_at = %[1]d, stale = FALSE, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"things_usage":{"lk":null,"m":[{"v":4,"l":null}]}}', checked_at = %[1]d, next_scrape_at = %[2]d, quota_desynced_at = %[1]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, stale = FALSE, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"things_usage":{"lk":null,"m":[{"v":4,"l":null}]}}', checked_at = %[3]d, next_scrape_at = %[4]d, quota_desynced_at = %[3]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
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
	serviceUsageReport.Resources["capacity"].Quota = p2i64(110)
	serviceUsageReport.Resources["things"].PerAZ["az-two"].Usage = 3
	serviceUsageReport.Resources["things"].PerAZ["az-two"].Subresources = append(serviceUsageReport.Resources["things"].PerAZ["az-two"].Subresources, liquid.Subresource{Name: "index", Usage: pointerTo(uint64(4))})
	serviceUsageReport.Metrics["things_usage"] = []liquid.Metric{{Value: 3}}
	// Scrape should pick up the changed resource data
	// (no quota sync should be requested since there is one requested already)
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	// TODO: How does the ResourceScrapeJob trigger backend_quota updates? The jobs are distinct from each other. This caused quite some confusion when understanding the tests.
	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET usage = 3, subresources = '[{"name":"index","usage":2},{"name":"index","usage":3},{"name":"index","usage":4}]', historical_usage = '{"t":[%[6]d,%[3]d],"v":[2,3]}' WHERE id = 18 AND resource_id = 6 AND az = 'az-two';
		UPDATE project_az_resources SET usage = 3, subresources = '[{"name":"index","usage":2},{"name":"index","usage":3},{"name":"index","usage":4}]', historical_usage = '{"t":[%[5]d,%[1]d],"v":[2,3]}' WHERE id = 9 AND resource_id = 3 AND az = 'az-two';
		UPDATE project_resources SET backend_quota = 110 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET backend_quota = 110 WHERE id = 4 AND service_id = 2 AND name = 'capacity';
		UPDATE project_services SET scraped_at = %[1]d, serialized_metrics = '{"capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"things_usage":{"lk":null,"m":[{"v":3,"l":null}]}}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, serialized_metrics = '{"capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"things_usage":{"lk":null,"m":[{"v":3,"l":null}]}}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
		firstScrapedAt1.Unix(), firstScrapedAt2.Unix(),
	)

	// TODO: How to handle minQuata/maxQuota with liquid?

	/*
		// check reporting of MinQuotaFromBackend/MaxQuotaFromBackend
		s.Clock.StepBy(scrapeInterval)
		plugin.MinQuota = map[liquid.ResourceName]uint64{"capacity": 10}
		plugin.MaxQuota = map[liquid.ResourceName]uint64{"things": 1000}
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
	*/

	// set some new quota values and align the report values with it, so nothing changes when next Scrape happens
	serviceUsageReport.Resources["capacity"].Quota = p2i64(20)
	serviceUsageReport.Resources["things"].Quota = p2i64(13)
	_, err := s.DB.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 20, "capacity")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 13, "things")
	if err != nil {
		t.Fatal(err)
	}
	tr.DBChanges().Ignore()

	// test SyncQuotaToBackendJob running and failing (this checks that it does
	// not get stuck on a failing project service and moves on to the other one
	// in the second attempt)
	plugin.LiquidClient.(*core.MockLiquidClient).SetQuotaError(errors.New("SetQuota failed as requested"))
	expectedErrorRx := regexp.MustCompile(`SetQuota failed as requested$`)
	mustFailLikeT(t, syncJob.ProcessOne(s.Ctx, withLabel), expectedErrorRx)
	mustFailLikeT(t, syncJob.ProcessOne(s.Ctx, withLabel), expectedErrorRx) // twice because there are two projects
	failedAt1 := s.Clock.Now().Add(-5 * time.Second)
	failedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET quota_desynced_at = %[1]d, quota_sync_duration_secs = 5 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET quota_desynced_at = %[2]d, quota_sync_duration_secs = 5 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		failedAt1.Add(30*time.Second).Unix(),
		failedAt2.Add(30*time.Second).Unix(),
	)

	// test SyncQuotaToBackendJob running successfully
	plugin.LiquidClient.(*core.MockLiquidClient).SetQuotaError(nil)
	mustT(t, syncJob.ProcessOne(s.Ctx, withLabel))
	mustT(t, syncJob.ProcessOne(s.Ctx, withLabel))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET backend_quota = 20 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET backend_quota = 13 WHERE id = 3 AND service_id = 1 AND name = 'things';
		UPDATE project_resources SET backend_quota = 20 WHERE id = 4 AND service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET backend_quota = 13 WHERE id = 6 AND service_id = 2 AND name = 'things';
		UPDATE project_services SET quota_desynced_at = NULL WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET quota_desynced_at = NULL WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	// test SyncQuotaToBackendJob not having anything to do
	mustFailT(t, syncJob.ProcessOne(s.Ctx, withLabel), sql.ErrNoRows)
	tr.DBChanges().AssertEmpty()

	// Scrape should show that the quota update was durable
	s.Clock.StepBy(scrapeInterval)
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

	// set "capacity" to a non-zero usage to observe a non-zero usage on
	// "capacity_portion" (otherwise this resource has been all zeroes this entire
	// time)
	s.Clock.StepBy(scrapeInterval)
	// note: there is currently no concistency check between the metrics and the actual resources
	serviceUsageReport.Resources["capacity"].PerAZ["az-one"].Usage = 20
	serviceUsageReport.Metrics["capacity_usage"] = []liquid.Metric{{Value: 20}}
	serviceUsageReport.Resources["capacity"].PerAZ["az-one"].PhysicalUsage = p2u64(10)
	serviceUsageReport.Resources["capacity_portion"].PerAZ["az-one"].Usage = 5

	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET usage = 20, physical_usage = 10, historical_usage = '{"t":[%[6]d,%[3]d],"v":[0,20]}' WHERE id = 11 AND resource_id = 4 AND az = 'az-one';
		UPDATE project_az_resources SET usage = 5, historical_usage = '{"t":[%[6]d,%[3]d],"v":[0,5]}' WHERE id = 14 AND resource_id = 5 AND az = 'az-one';
		UPDATE project_az_resources SET usage = 20, physical_usage = 10, historical_usage = '{"t":[%[5]d,%[1]d],"v":[0,20]}' WHERE id = 2 AND resource_id = 1 AND az = 'az-one';
		UPDATE project_az_resources SET usage = 5, historical_usage = '{"t":[%[5]d,%[1]d],"v":[0,5]}' WHERE id = 5 AND resource_id = 2 AND az = 'az-one';
		UPDATE project_services SET scraped_at = %[1]d, serialized_metrics = '{"capacity_usage":{"lk":null,"m":[{"v":20,"l":null}]},"things_usage":{"lk":null,"m":[{"v":3,"l":null}]}}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, serialized_metrics = '{"capacity_usage":{"lk":null,"m":[{"v":20,"l":null}]},"things_usage":{"lk":null,"m":[{"v":3,"l":null}]}}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
		firstScrapedAt1.Unix(),
		firstScrapedAt2.Unix(),
	)

	// add some commitments in order to test the `limes_project_committed_per_az` metric below
	commitmentForOneYear, err := limesresources.ParseCommitmentDuration("1 year")
	mustT(t, err)
	now := s.Clock.Now()
	// AZResourceID = 2 has two commitments in state "active" to test summing by state
	creationContext := db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate}
	buf, err := json.Marshal(creationContext)
	mustT(t, err)
	for _, amount := range []uint64{7, 8} {
		mustT(t, s.DB.Insert(&db.ProjectCommitment{
			AZResourceID:        2,
			Amount:              amount,
			Duration:            commitmentForOneYear,
			CreatedAt:           now,
			CreatorUUID:         "dummy",
			CreatorName:         "dummy",
			ConfirmedAt:         &now,
			ExpiresAt:           commitmentForOneYear.AddTo(now),
			State:               db.CommitmentStateActive,
			CreationContextJSON: buf,
		}))
	}
	// AZResourceID = 11 has two commitments in different states to test aggregation over different states
	mustT(t, s.DB.Insert(&db.ProjectCommitment{
		AZResourceID:        11,
		Amount:              10,
		Duration:            commitmentForOneYear,
		CreatedAt:           now,
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ConfirmedAt:         &now,
		ExpiresAt:           commitmentForOneYear.AddTo(now),
		State:               db.CommitmentStateActive,
		CreationContextJSON: buf,
	}))
	mustT(t, s.DB.Insert(&db.ProjectCommitment{
		AZResourceID:        11,
		Amount:              10,
		Duration:            commitmentForOneYear,
		CreatedAt:           now,
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ConfirmBy:           &now,
		ExpiresAt:           commitmentForOneYear.AddTo(now),
		State:               db.CommitmentStatePending,
		CreationContextJSON: buf,
	}))

	// TODO: the scraping metrics in this file have been changed to an earlier value, because the timeslice from the other TODO is skipped --> change when TODO is handled
	// check data metrics generated by this scraping pass
	registry := prometheus.NewPedanticRegistry()
	amc := &AggregateMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(amc)
	pmc := &QuotaPluginMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(pmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": contentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/scrape_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	// TODO: the limes_project_commitment_min_expires_at was changed to an earlier value, because the timeslice from the other TODO is skipped --> change when TODO is handled
	dmr := &DataMetricsReporter{Cluster: s.Cluster, DB: s.DB, ReportZeroes: true}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": contentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/scrape_data_metrics.prom"),
	}.Check(t, dmr)

	// TODO: the limes_project_commitment_min_expires_at was changed to an earlier value, because the timeslice from the other TODO is skipped --> change when TODO is handled
	// check data metrics with the skip_zero flag set
	dmr.ReportZeroes = false
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": contentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/scrape_data_metrics_skipzero.prom"),
	}.Check(t, dmr)
}

func Test_ScrapeFailure(t *testing.T) {
	s, job, withLabel, _, plugin, serviceInfo, serviceUsageReport := commonComplexScrapeTestSetup(t, liquid.AZAwareResourceTopology)

	// we will see an expected ERROR during testing, do not make the test fail because of this
	expectedErrorRx := regexp.MustCompile(`^during resource scrape of project germany/(berlin|dresden): GetUsageReport failed as requested$`)

	// check that ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	// failing Scrape should create dummy records to ensure that the API finds
	// plausibly-structured data
	//
	// Note that this does *not* set quota_desynced_at. We would rather not
	// write any quotas while we cannot even get correct usage numbers.
	s.Clock.StepBy(scrapeInterval)
	plugin.LiquidClient.(*core.MockLiquidClient).SetUsageReportError(errors.New("GetUsageReport failed as requested"))
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
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (1, 1, 'capacity', 0, -1);
		INSERT INTO project_resources (id, service_id, name) VALUES (2, 1, 'capacity_portion');
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (3, 1, 'things', 0, -1);
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (4, 2, 'capacity', 0, -1);
		INSERT INTO project_resources (id, service_id, name) VALUES (5, 2, 'capacity_portion');
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (6, 2, 'things', 0, -1);
		UPDATE project_services SET scraped_at = 0, stale = FALSE, checked_at = %[1]d, scrape_error_message = 'GetUsageReport failed as requested', next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = 0, stale = FALSE, checked_at = %[3]d, scrape_error_message = 'GetUsageReport failed as requested', next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
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

	plugin.LiquidClient.(*core.MockLiquidClient).SetUsageReportError(nil)
	plugin.LiquidServiceInfo = serviceInfo
	plugin.LiquidClient.(*core.MockLiquidClient).SetUsageReport(serviceUsageReport)

	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel)) // twice because there are two projects

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET historical_usage = '{"t":[%[1]d],"v":[0]}' WHERE id = 1 AND resource_id = 1 AND az = 'any';
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (10, 2, 'az-two', 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (11, 3, 'az-one', 2, '[{"name":"index","usage":0},{"name":"index","usage":1}]', '{"t":[%[1]d],"v":[2]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (12, 3, 'az-two', 2, '[{"name":"index","usage":2},{"name":"index","usage":3}]', '{"t":[%[1]d],"v":[2]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage, historical_usage) VALUES (13, 4, 'az-one', 0, 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage, historical_usage) VALUES (14, 4, 'az-two', 0, 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (15, 5, 'az-one', 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (16, 5, 'az-two', 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (17, 6, 'az-one', 2, '[{"name":"index","usage":0},{"name":"index","usage":1}]', '{"t":[%[3]d],"v":[2]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage) VALUES (18, 6, 'az-two', 2, '[{"name":"index","usage":2},{"name":"index","usage":3}]', '{"t":[%[3]d],"v":[2]}');
		UPDATE project_az_resources SET historical_usage = '{"t":[%[1]d],"v":[0]}' WHERE id = 2 AND resource_id = 2 AND az = 'any';
		UPDATE project_az_resources SET historical_usage = '{"t":[%[1]d],"v":[0]}' WHERE id = 3 AND resource_id = 3 AND az = 'any';
		UPDATE project_az_resources SET historical_usage = '{"t":[%[3]d],"v":[0]}' WHERE id = 4 AND resource_id = 4 AND az = 'any';
		UPDATE project_az_resources SET historical_usage = '{"t":[%[3]d],"v":[0]}' WHERE id = 5 AND resource_id = 5 AND az = 'any';
		UPDATE project_az_resources SET historical_usage = '{"t":[%[3]d],"v":[0]}' WHERE id = 6 AND resource_id = 6 AND az = 'any';
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage, historical_usage) VALUES (7, 1, 'az-one', 0, 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage, historical_usage) VALUES (8, 1, 'az-two', 0, 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (9, 2, 'az-one', 0, '{"t":[%[1]d],"v":[0]}');
		UPDATE project_resources SET backend_quota = 100 WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET backend_quota = 42 WHERE id = 3 AND service_id = 1 AND name = 'things';
		UPDATE project_resources SET backend_quota = 100 WHERE id = 4 AND service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET backend_quota = 42 WHERE id = 6 AND service_id = 2 AND name = 'things';
		UPDATE project_services SET scraped_at = %[1]d, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"things_usage":{"lk":null,"m":[{"v":4,"l":null}]}}', checked_at = %[1]d, scrape_error_message = '', next_scrape_at = %[2]d, quota_desynced_at = %[1]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"things_usage":{"lk":null,"m":[{"v":4,"l":null}]}}', checked_at = %[3]d, scrape_error_message = '', next_scrape_at = %[4]d, quota_desynced_at = %[3]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	// backend fails again and we need to scrape because of the stale flag ->
	// touch neither scraped_at nor the existing resources (this also tests that a
	// failed check causes Scrape("unittest") to continue with the next resource afterwards)
	s.Clock.StepBy(scrapeInterval)
	plugin.LiquidClient.(*core.MockLiquidClient).SetUsageReportError(errors.New("GetUsageReport failed as requested"))
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx) // twice because there are two projects

	checkedAt1 = s.Clock.Now().Add(-5 * time.Second)
	checkedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET checked_at = %[1]d, scrape_error_message = 'GetUsageReport failed as requested', next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET checked_at = %[3]d, scrape_error_message = 'GetUsageReport failed as requested', next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		checkedAt1.Unix(), checkedAt1.Add(recheckInterval).Unix(),
		checkedAt2.Unix(), checkedAt2.Add(recheckInterval).Unix(),
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
				type: liquid
				params:	
					area: testing
					test_mode: true
	`
)

func Test_ScrapeButNoResources(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testNoopConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	// override some defaults we set in the MockLiquidClient
	plugin := s.Cluster.QuotaPlugins["noop"].(*plugins.LiquidQuotaPlugin)
	plugin.LiquidServiceInfo = liquid.ServiceInfo{
		Version:   1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{},
	}
	plugin.LiquidClient.(*core.MockLiquidClient).SetUsageReport(liquid.ServiceUsageReport{InfoVersion: 1})

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
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_services (id, project_id, type, scraped_at, scrape_duration_secs, rates_stale, checked_at, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'noop', %[1]d, 5, TRUE, %[1]d, %[2]d, 0);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
	`,
		scrapedAt.Unix(), scrapedAt.Add(scrapeInterval).Unix(),
	)
}

////////////////////////////////////////////////////////////////////////////////
// test for empty UsageData

func Test_ScrapeReturnsNoUsageData(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testNoopConfigYAML),
	)
	prepareDomainsAndProjectsForScrape(t, s)

	// override some defaults we set in the MockQuotaPlugin
	// override some defaults we set in the MockLiquidClient
	plugin := s.Cluster.QuotaPlugins["noop"].(*plugins.LiquidQuotaPlugin)
	plugin.LiquidServiceInfo = liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"things": {Unit: limes.UnitNone, HasQuota: true},
		},
	}
	plugin.LiquidClient.(*core.MockLiquidClient).SetUsageReport(liquid.ServiceUsageReport{InfoVersion: 1})

	c := getCollector(t, s)
	job := c.ResourceScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "noop")

	// check that Scrape() behaves properly when encountering a quota plugin with
	// no Resources() (in the wild, this can happen because some quota plugins
	// only have Rates())
	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), errors.New(`during resource scrape of project germany/berlin: missing report for resource "things"`))

	scrapedAt := s.Clock.Now()
	_, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_az_resources (id, resource_id, az, usage) VALUES (1, 1, 'any', 0);
		INSERT INTO project_resources (id, service_id, name, quota, backend_quota) VALUES (1, 1, 'things', 0, -1);
		INSERT INTO project_services (id, project_id, type, scraped_at, rates_stale, checked_at, scrape_error_message, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'noop', 0, %[1]d, TRUE, 'missing report for resource "things"', %[2]d, 0);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
	`,
		scrapedAt.Unix(), scrapedAt.Add(recheckInterval).Unix(),
	)
}

func Test_TopologyScrapes(t *testing.T) {
	s, job, withLabel, syncJob, plugin, serviceInfo, serviceUsageReport := commonComplexScrapeTestSetup(t, liquid.AZSeparatedResourceTopology)
	plugin.LiquidServiceInfo = serviceInfo
	plugin.LiquidClient.(*core.MockLiquidClient).SetUsageReport(serviceUsageReport)

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	// positive: Sync az-separated quota values with the backend
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage, historical_usage, backend_quota) VALUES (1, 1, 'az-one', 0, 0, '{"t":[%[1]d],"v":[0]}', 50);
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (10, 5, 'any', 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (11, 5, 'az-one', 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (12, 5, 'az-two', 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage, backend_quota) VALUES (13, 6, 'az-one', 2, '[{"name":"index","usage":0},{"name":"index","usage":1}]', '{"t":[%[3]d],"v":[2]}', 21);
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage, backend_quota) VALUES (14, 6, 'az-two', 2, '[{"name":"index","usage":2},{"name":"index","usage":3}]', '{"t":[%[3]d],"v":[2]}', 21);
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage, historical_usage, backend_quota) VALUES (2, 1, 'az-two', 0, 0, '{"t":[%[1]d],"v":[0]}', 50);
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (3, 2, 'any', 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (4, 2, 'az-one', 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (5, 2, 'az-two', 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage, backend_quota) VALUES (6, 3, 'az-one', 2, '[{"name":"index","usage":0},{"name":"index","usage":1}]', '{"t":[%[1]d],"v":[2]}', 21);
		INSERT INTO project_az_resources (id, resource_id, az, usage, subresources, historical_usage, backend_quota) VALUES (7, 3, 'az-two', 2, '[{"name":"index","usage":2},{"name":"index","usage":3}]', '{"t":[%[1]d],"v":[2]}', 21);
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage, historical_usage, backend_quota) VALUES (8, 4, 'az-one', 0, 0, '{"t":[%[3]d],"v":[0]}', 50);
		INSERT INTO project_az_resources (id, resource_id, az, usage, physical_usage, historical_usage, backend_quota) VALUES (9, 4, 'az-two', 0, 0, '{"t":[%[3]d],"v":[0]}', 50);
		INSERT INTO project_resources (id, service_id, name) VALUES (1, 1, 'capacity');
		INSERT INTO project_resources (id, service_id, name) VALUES (2, 1, 'capacity_portion');
		INSERT INTO project_resources (id, service_id, name) VALUES (3, 1, 'things');
		INSERT INTO project_resources (id, service_id, name) VALUES (4, 2, 'capacity');
		INSERT INTO project_resources (id, service_id, name) VALUES (5, 2, 'capacity_portion');
		INSERT INTO project_resources (id, service_id, name) VALUES (6, 2, 'things');
		UPDATE project_services SET scraped_at = %[1]d, stale = FALSE, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"things_usage":{"lk":null,"m":[{"v":4,"l":null}]}}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, stale = FALSE, scrape_duration_secs = 5, serialized_metrics = '{"capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"things_usage":{"lk":null,"m":[{"v":4,"l":null}]}}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
		`,
		scrapedAt1.Unix(), scrapedAt1.Add(scrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(scrapeInterval).Unix(),
	)

	// set some quota acpq values.
	_, err := s.DB.Exec(`UPDATE project_az_resources SET quota = $1 WHERE resource_id IN (1,4) and az != 'any'`, 20)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`UPDATE project_az_resources SET quota = $1 WHERE resource_id IN (3,6) and az != 'any'`, 13)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`UPDATE project_services SET quota_desynced_at = $1`, s.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	tr.DBChanges().Ignore()

	mustT(t, syncJob.ProcessOne(s.Ctx, withLabel))
	mustT(t, syncJob.ProcessOne(s.Ctx, withLabel))

	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET backend_quota = 20 WHERE id = 1 AND resource_id = 1 AND az = 'az-one';
		UPDATE project_az_resources SET backend_quota = 13 WHERE id = 13 AND resource_id = 6 AND az = 'az-one';
		UPDATE project_az_resources SET backend_quota = 13 WHERE id = 14 AND resource_id = 6 AND az = 'az-two';
		UPDATE project_az_resources SET backend_quota = 20 WHERE id = 2 AND resource_id = 1 AND az = 'az-two';
		UPDATE project_az_resources SET backend_quota = 13 WHERE id = 6 AND resource_id = 3 AND az = 'az-one';
		UPDATE project_az_resources SET backend_quota = 13 WHERE id = 7 AND resource_id = 3 AND az = 'az-two';
		UPDATE project_az_resources SET backend_quota = 20 WHERE id = 8 AND resource_id = 4 AND az = 'az-one';
		UPDATE project_az_resources SET backend_quota = 20 WHERE id = 9 AND resource_id = 4 AND az = 'az-two';
		UPDATE project_services SET quota_desynced_at = NULL, quota_sync_duration_secs = 5 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET quota_desynced_at = NULL, quota_sync_duration_secs = 5 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	s.Clock.StepBy(scrapeInterval)

	// topology of a resource changes. Reset AZ-separated backend_quota
	resourceEntry := serviceInfo.Resources["things"]
	resourceEntry.Topology = liquid.AZAwareResourceTopology
	serviceInfo.Resources["things"] = resourceEntry
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	checkedAt1 := s.Clock.Now().Add(-5 * time.Second)
	checkedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET backend_quota = 50 WHERE id = 1 AND resource_id = 1 AND az = 'az-one';
		UPDATE project_az_resources SET backend_quota = NULL WHERE id = 13 AND resource_id = 6 AND az = 'az-one';
		UPDATE project_az_resources SET backend_quota = NULL WHERE id = 14 AND resource_id = 6 AND az = 'az-two';
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (15, 3, 'any', 0, '{"t":[1825],"v":[0]}');
		INSERT INTO project_az_resources (id, resource_id, az, usage, historical_usage) VALUES (16, 6, 'any', 0, '{"t":[1830],"v":[0]}');
		UPDATE project_az_resources SET backend_quota = 50 WHERE id = 2 AND resource_id = 1 AND az = 'az-two';
		UPDATE project_az_resources SET backend_quota = NULL WHERE id = 6 AND resource_id = 3 AND az = 'az-one';
		UPDATE project_az_resources SET backend_quota = NULL WHERE id = 7 AND resource_id = 3 AND az = 'az-two';
		UPDATE project_az_resources SET backend_quota = 50 WHERE id = 8 AND resource_id = 4 AND az = 'az-one';
		UPDATE project_az_resources SET backend_quota = 50 WHERE id = 9 AND resource_id = 4 AND az = 'az-two';
		UPDATE project_resources SET quota = 0, backend_quota = 42 WHERE id = 3 AND service_id = 1 AND name = 'things';
		UPDATE project_resources SET quota = 0, backend_quota = 42 WHERE id = 6 AND service_id = 2 AND name = 'things';
		UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d, quota_desynced_at = %[1]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d, quota_desynced_at = %[3]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		checkedAt1.Unix(), checkedAt1.Add(scrapeInterval).Unix(),
		checkedAt2.Unix(), checkedAt2.Add(scrapeInterval).Unix(),
	)

	s.Clock.StepBy(scrapeInterval)
	// positive: missing AZ in resource report will be created by the scraper in order to assign basequota later.
	// warning: any AZs will be removed, because resource things switches from AZAware to AZSeparated.
	resourceEntry.Topology = liquid.AZSeparatedResourceTopology
	serviceInfo.Resources["things"] = resourceEntry
	delete(serviceUsageReport.Resources["things"].PerAZ, "az-two")
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	checkedAt1 = s.Clock.Now().Add(-5 * time.Second)
	checkedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET backend_quota = 21 WHERE id = 13 AND resource_id = 6 AND az = 'az-one';
		UPDATE project_az_resources SET usage = 0, subresources = '', historical_usage = '{"t":[%[2]d,%[5]d],"v":[2,0]}' WHERE id = 14 AND resource_id = 6 AND az = 'az-two';
		DELETE FROM project_az_resources WHERE id = 15 AND resource_id = 3 AND az = 'any';
		DELETE FROM project_az_resources WHERE id = 16 AND resource_id = 6 AND az = 'any';
		UPDATE project_az_resources SET backend_quota = 21 WHERE id = 6 AND resource_id = 3 AND az = 'az-one';
		UPDATE project_az_resources SET usage = 0, subresources = '', historical_usage = '{"t":[%[1]d,%[3]d],"v":[2,0]}' WHERE id = 7 AND resource_id = 3 AND az = 'az-two';
		UPDATE project_resources SET quota = NULL, backend_quota = NULL WHERE id = 3 AND service_id = 1 AND name = 'things';
		UPDATE project_resources SET quota = NULL, backend_quota = NULL WHERE id = 6 AND service_id = 2 AND name = 'things';
		UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = %[5]d, checked_at = %[5]d, next_scrape_at = %[6]d WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`,
		scrapedAt1.Unix(), scrapedAt2.Unix(),
		checkedAt1.Unix(), checkedAt1.Add(scrapeInterval).Unix(),
		checkedAt2.Unix(), checkedAt2.Add(scrapeInterval).Unix(),
	)

	s.Clock.StepBy(scrapeInterval)
	// negative: scrape with flat topology returns invalid AZs
	resourceEntry = serviceInfo.Resources["capacity"]
	resourceEntry.Topology = liquid.FlatResourceTopology
	serviceInfo.Resources["capacity"] = resourceEntry
	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), errors.New("during resource scrape of project germany/berlin: resource: capacity: scrape with topology type: flat returned AZs: [az-one az-two]"))

	// negative: scrape with az-aware topology returns invalid any AZ
	resourceEntry.Topology = liquid.AZAwareResourceTopology
	serviceInfo.Resources["capacity"] = resourceEntry
	serviceUsageReport.Resources["capacity"] = &liquid.ResourceUsageReport{
		PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
			"any": {},
		},
	}
	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), errors.New("during resource scrape of project germany/dresden: resource: capacity: scrape with topology type: az-aware returned AZs: [any]"))

	s.Clock.StepBy(scrapeInterval)
	// negative: scrape with az-separated topology returns invalid AZs any and unknown
	resourceEntry.Topology = liquid.AZSeparatedResourceTopology
	serviceInfo.Resources["capacity"] = resourceEntry
	serviceUsageReport.Resources["capacity"] = &liquid.ResourceUsageReport{
		PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
			"unknown": {},
			"az-one":  {},
		},
	}
	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), errors.New("during resource scrape of project germany/berlin: resource: capacity: scrape with topology type: az-separated returned AZs: [az-one unknown]"))

	// negative: reject liquid initialization with invalid topologies
	resourceEntry.Topology = "invalidAZ1"
	serviceInfo.Resources["capacity"] = resourceEntry
	resourceEntry = serviceInfo.Resources["things"]
	resourceEntry.Topology = "invalidAZ2"
	serviceInfo.Resources["things"] = resourceEntry
	// CheckResourceTopologies is checked after the MockLiquidClient is returned in Init(), so we just check it manually
	mustFailT(t, plugins.CheckResourceTopologies(serviceInfo), errors.New("invalid topology: invalidAZ1 on resource: capacity\ninvalid topology: invalidAZ2 on resource: things"))

	// negative: multiple resources with mismatching topology to AZ responses
	resourceEntry.Topology = liquid.AZSeparatedResourceTopology
	serviceInfo.Resources["things"] = resourceEntry
	resourceEntry = serviceInfo.Resources["capacity"]
	resourceEntry.Topology = liquid.AZSeparatedResourceTopology
	serviceInfo.Resources["capacity"] = resourceEntry

	serviceUsageReport.Resources["capacity"] = &liquid.ResourceUsageReport{
		PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
			"unknown": {},
		},
	}
	serviceUsageReport.Resources["things"] = &liquid.ResourceUsageReport{
		PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
			"unknown": {},
		},
	}
	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), errors.New("during resource scrape of project germany/dresden: resource: capacity: scrape with topology type: az-separated returned AZs: [unknown]\nresource: things: scrape with topology type: az-separated returned AZs: [unknown]"))

	s.Clock.StepBy(scrapeInterval)
	// positive: empty topology is not checked at all
	resourceEntry = serviceInfo.Resources["things"]
	resourceEntry.Topology = ""
	serviceInfo.Resources["capacity"] = resourceEntry
	serviceUsageReport.Resources["things"] = &liquid.ResourceUsageReport{
		PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
			"az-one": {},
		},
	}
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
}
