// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"testing"
	"time"

	. "github.com/majewsky/gg/option"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/jobloop"

	"github.com/sapcc/limes/internal/collector"
	"github.com/sapcc/limes/internal/db"
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

func prepareDomainsAndProjectsForScrape(t *testing.T, s test.Setup) {
	// ScanDomains is required to create the entries in `domains`, `projects` and `project_services`
	timeZero := func() time.Time { return time.Unix(0, 0).UTC() }
	_, err := (&collector.Collector{Cluster: s.Cluster, DB: s.DB, MeasureTime: timeZero, AddJitter: test.NoJitter}).ScanDomains(s.Ctx, collector.ScanDomainsOpts{})
	if err != nil {
		t.Fatal(err)
	}
}

const (
	testScrapeBasicConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: static
			static_config:
				domains:
					- { name: germany, id: uuid-for-germany }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
						- { name: dresden, id: uuid-for-dresden, parent_id: uuid-for-berlin }
		liquids:
			unittest:
				area: testing
				# to check how they are merged with the ServiceInfo of the liquids
				rate_limits: 
					global:
						- name: xOtherRate
							limit:  5000
							window: 1s
		quota_distribution_configs:
			# this is only used to check that historical_usage is tracked
			- { resource: unittest/capacity, model: autogrow, autogrow: { growth_multiplier: 1.0, usage_data_retention_period: 48h } }
			- { resource: unittest/things, model: autogrow, autogrow: { growth_multiplier: 1.0, usage_data_retention_period: 48h } }
	`
)

func commonComplexScrapeTestSetup(t *testing.T) (s test.Setup, scrapeJob jobloop.Job, withLabel jobloop.Option, syncJob jobloop.Job, srvInfo liquid.ServiceInfo, serviceUsageReport liquid.ServiceUsageReport) {
	srvInfo = liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"capacity": {
				Unit:                liquid.UnitBytes,
				Topology:            liquid.AZAwareTopology,
				HasCapacity:         true,
				HasQuota:            true,
				NeedsResourceDemand: true,
			},
			"things": {
				Unit:        liquid.UnitNone,
				Topology:    liquid.AZAwareTopology,
				HasCapacity: false,
				HasQuota:    true,
			},
		},
		Rates: map[liquid.RateName]liquid.RateInfo{
			"firstrate":  {Topology: liquid.FlatTopology, HasUsage: true},
			"secondrate": {Unit: "KiB", Topology: liquid.FlatTopology, HasUsage: true},
		},
		UsageMetricFamilies: map[liquid.MetricName]liquid.MetricFamilyInfo{
			"limes_unittest_capacity_usage": {Type: liquid.MetricTypeGauge},
			"limes_unittest_things_usage":   {Type: liquid.MetricTypeGauge},
		},
	}
	s = test.NewSetup(t,
		test.WithConfig(testScrapeBasicConfigYAML),
		test.WithMockLiquidClient("unittest", srvInfo),
		test.WithLiquidConnections,
	)
	prepareDomainsAndProjectsForScrape(t, s)

	c := getCollector(t, s)
	scrapeJob = c.ScrapeJob(s.Registry)
	withLabel = jobloop.WithLabel("service_type", "unittest")
	syncJob = c.SyncQuotaToBackendJob(s.Registry)

	// for one of the projects, put some records in for rate limits, to check that
	// the scraper does not mess with those values
	// cluster_rate xOtherRate comes from the rate_limits config
	err := s.DB.Insert(&db.Rate{
		ServiceID:     1,
		Name:          "xAnotherRate",
		LiquidVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.DB.Insert(&db.ProjectRate{
		ProjectID: 2,
		RateID:    3,
		Limit:     Some[uint64](10),
		Window:    Some(1 * limesrates.WindowSeconds),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.DB.Insert(&db.ProjectRate{
		ProjectID: 1,
		RateID:    4,
		Limit:     Some[uint64](42),
		Window:    Some(2 * limesrates.WindowMinutes),
	})
	if err != nil {
		t.Fatal(err)
	}

	serviceUsageReport = liquid.ServiceUsageReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceUsageReport{
			"capacity": {
				Quota: Some[int64](100),
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
					"az-one": {
						Usage:         0,
						PhysicalUsage: Some[uint64](0),
					},
					"az-two": {
						Usage:         0,
						PhysicalUsage: Some[uint64](0),
					},
				},
			},
			"things": {
				Quota: Some[int64](42),
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
					"az-one": {
						Usage: 2,
						Subresources: []liquid.Subresource{
							{
								Name:  "index",
								Usage: Some[uint64](0),
							},
							{
								Name:  "index",
								Usage: Some[uint64](1),
							},
						},
					},
					"az-two": {
						Usage: 2,
						Subresources: []liquid.Subresource{
							{
								Name:  "index",
								Usage: Some[uint64](2),
							},
							{
								Name:  "index",
								Usage: Some[uint64](3),
							},
						},
					},
				},
			},
		},
		Metrics: map[liquid.MetricName][]liquid.Metric{
			"limes_unittest_capacity_usage": {{Value: 0}},
			"limes_unittest_things_usage":   {{Value: 4}},
		},
		Rates: map[liquid.RateName]*liquid.RateUsageReport{
			"firstrate": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZRateUsageReport{
					"any": {
						Usage: Some(big.NewInt(1024)),
					},
				},
			},
			"secondrate": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZRateUsageReport{
					"any": {
						Usage: Some(big.NewInt(2048)),
					},
				},
			},
		},
		SerializedState: []byte(`{"firstrate":1024,"secondrate":2048}`),
	}
	s.LiquidClients["unittest"].SetUsageReport(serviceUsageReport)
	return
}

func Test_ScrapeSuccess(t *testing.T) {
	s, job, withLabel, syncJob, _, serviceUsageReport := commonComplexScrapeTestSetup(t)

	// check that ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	// first Scrape should create the entries in `project_resources` with the
	// correct usage and backend quota values (and quota = 0 because no ACPQ has run yet)
	// and set `project_services.scraped_at` to the current time;
	// a desync should be noted, but we will not run syncJob until later in this test
	s.Clock.StepBy(collector.ScrapeInterval)
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel)) // twice because there are two projects

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, historical_usage) VALUES (1, 1, 1, 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, historical_usage) VALUES (10, 2, 5, 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, subresources, historical_usage) VALUES (11, 2, 6, 2, '[{"name":"index","usage":0},{"name":"index","usage":1}]', '{"t":[%[3]d],"v":[2]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, subresources, historical_usage) VALUES (12, 2, 7, 2, '[{"name":"index","usage":2},{"name":"index","usage":3}]', '{"t":[%[3]d],"v":[2]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, physical_usage, historical_usage) VALUES (2, 1, 2, 0, 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, physical_usage, historical_usage) VALUES (3, 1, 3, 0, 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, historical_usage) VALUES (4, 1, 5, 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, subresources, historical_usage) VALUES (5, 1, 6, 2, '[{"name":"index","usage":0},{"name":"index","usage":1}]', '{"t":[%[1]d],"v":[2]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, subresources, historical_usage) VALUES (6, 1, 7, 2, '[{"name":"index","usage":2},{"name":"index","usage":3}]', '{"t":[%[1]d],"v":[2]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, historical_usage) VALUES (7, 2, 1, 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, physical_usage, historical_usage) VALUES (8, 2, 2, 0, 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, physical_usage, historical_usage) VALUES (9, 2, 3, 0, 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_rates (id, project_id, rate_id, usage_as_bigint) VALUES (3, 1, 1, '1024');
		INSERT INTO project_rates (id, project_id, rate_id, usage_as_bigint) VALUES (4, 1, 2, '2048');
		INSERT INTO project_rates (id, project_id, rate_id, usage_as_bigint) VALUES (5, 2, 1, '1024');
		INSERT INTO project_rates (id, project_id, rate_id, usage_as_bigint) VALUES (6, 2, 2, '2048');
		INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (1, 1, 1, 0, 100);
		INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (2, 1, 2, 0, 42);
		INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (3, 2, 1, 0, 100);
		INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (4, 2, 2, 0, 42);
		UPDATE project_services SET scraped_at = %[1]d, stale = FALSE, scrape_duration_secs = 5, serialized_scrape_state = '{"firstrate":1024,"secondrate":2048}', serialized_metrics = '{"limes_unittest_capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"limes_unittest_things_usage":{"lk":null,"m":[{"v":4,"l":null}]}}', checked_at = %[1]d, next_scrape_at = %[2]d, quota_desynced_at = %[1]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET scraped_at = %[3]d, stale = FALSE, scrape_duration_secs = 5, serialized_scrape_state = '{"firstrate":1024,"secondrate":2048}', serialized_metrics = '{"limes_unittest_capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"limes_unittest_things_usage":{"lk":null,"m":[{"v":4,"l":null}]}}', checked_at = %[3]d, next_scrape_at = %[4]d, quota_desynced_at = %[3]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(collector.ScrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(collector.ScrapeInterval).Unix(),
	)
	firstScrapedAt1 := scrapedAt1
	firstScrapedAt2 := scrapedAt2

	// second Scrape should not change anything (not even the timestamps) since
	// less than 30 minutes have passed since the last Scrape("unittest")
	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), sql.ErrNoRows)
	tr.DBChanges().AssertEmpty()

	// change the data that is reported by the liquid
	s.Clock.StepBy(collector.ScrapeInterval)
	serviceUsageReport.Resources["capacity"].Quota = Some[int64](110)
	serviceUsageReport.Resources["things"].PerAZ["az-two"].Usage = 3
	serviceUsageReport.Resources["things"].PerAZ["az-two"].Subresources = append(serviceUsageReport.Resources["things"].PerAZ["az-two"].Subresources, liquid.Subresource{Name: "index", Usage: Some[uint64](4)})
	serviceUsageReport.Metrics["limes_unittest_things_usage"] = []liquid.Metric{{Value: 3}}
	// Scrape should pick up the changed resource data
	// (no quota sync should be requested since there is one requested already)
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET usage = 3, subresources = '[{"name":"index","usage":2},{"name":"index","usage":3},{"name":"index","usage":4}]', historical_usage = '{"t":[%[6]d,%[3]d],"v":[2,3]}' WHERE id = 12 AND project_id = 2 AND az_resource_id = 7;
		UPDATE project_az_resources SET usage = 3, subresources = '[{"name":"index","usage":2},{"name":"index","usage":3},{"name":"index","usage":4}]', historical_usage = '{"t":[%[5]d,%[1]d],"v":[2,3]}' WHERE id = 6 AND project_id = 1 AND az_resource_id = 7;
		UPDATE project_resources SET backend_quota = 110 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		UPDATE project_resources SET backend_quota = 110 WHERE id = 3 AND project_id = 2 AND resource_id = 1;
		UPDATE project_services SET scraped_at = %[1]d, serialized_metrics = '{"limes_unittest_capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"limes_unittest_things_usage":{"lk":null,"m":[{"v":3,"l":null}]}}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET scraped_at = %[3]d, serialized_metrics = '{"limes_unittest_capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"limes_unittest_things_usage":{"lk":null,"m":[{"v":3,"l":null}]}}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(collector.ScrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(collector.ScrapeInterval).Unix(),
		firstScrapedAt1.Unix(), firstScrapedAt2.Unix(),
	)

	// check the impact of setting the forbidden flag on a resource
	s.Clock.StepBy(collector.ScrapeInterval)
	serviceUsageReport.Resources["capacity"].Forbidden = true
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
			UPDATE project_resources SET forbidden = TRUE WHERE id = 1 AND project_id = 1 AND resource_id = 1;
			UPDATE project_resources SET forbidden = TRUE WHERE id = 3 AND project_id = 2 AND resource_id = 1;
			UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
			UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
		`,
		scrapedAt1.Unix(), scrapedAt1.Add(collector.ScrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(collector.ScrapeInterval).Unix(),
	)
	// revert the forbidden flag
	s.Clock.StepBy(collector.ScrapeInterval)
	serviceUsageReport.Resources["capacity"].Forbidden = false
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
			UPDATE project_resources SET forbidden = FALSE WHERE id = 1 AND project_id = 1 AND resource_id = 1;
			UPDATE project_resources SET forbidden = FALSE WHERE id = 3 AND project_id = 2 AND resource_id = 1;
			UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
			UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
		`,
		scrapedAt1.Unix(), scrapedAt1.Add(collector.ScrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(collector.ScrapeInterval).Unix(),
	)

	// set some new quota values and align the report values with it, so nothing changes when next Scrape happens
	serviceUsageReport.Resources["capacity"].Quota = Some[int64](20)
	serviceUsageReport.Resources["things"].Quota = Some[int64](13)
	_, err := s.DB.Exec(`UPDATE project_resources ps SET quota = $1 FROM resources cs WHERE ps.resource_id = cs.id AND cs.name = $2`, 20, "capacity")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`UPDATE project_resources ps SET quota = $1 FROM resources cs WHERE ps.resource_id = cs.id AND cs.name = $2`, 13, "things")
	if err != nil {
		t.Fatal(err)
	}
	tr.DBChanges().Ignore()

	// test SyncQuotaToBackendJob running and failing (this checks that it does
	// not get stuck on a failing project service and moves on to the other one
	// in the second attempt)
	s.LiquidClients["unittest"].SetQuotaError(errors.New("SetQuota failed as requested"))
	expectedErrorRx := regexp.MustCompile(`SetQuota failed as requested$`)
	mustFailLikeT(t, syncJob.ProcessOne(s.Ctx, withLabel), expectedErrorRx)
	mustFailLikeT(t, syncJob.ProcessOne(s.Ctx, withLabel), expectedErrorRx) // twice because there are two projects
	failedAt1 := s.Clock.Now().Add(-5 * time.Second)
	failedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET quota_desynced_at = %[1]d, quota_sync_duration_secs = 5 WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET quota_desynced_at = %[2]d, quota_sync_duration_secs = 5 WHERE id = 2 AND project_id = 2 AND service_id = 1;
	`,
		failedAt1.Add(30*time.Second).Unix(),
		failedAt2.Add(30*time.Second).Unix(),
	)

	// test SyncQuotaToBackendJob running successfully
	s.LiquidClients["unittest"].SetQuotaError(nil)
	mustT(t, syncJob.ProcessOne(s.Ctx, withLabel))
	mustT(t, syncJob.ProcessOne(s.Ctx, withLabel))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET backend_quota = 20 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		UPDATE project_resources SET backend_quota = 13 WHERE id = 2 AND project_id = 1 AND resource_id = 2;
		UPDATE project_resources SET backend_quota = 20 WHERE id = 3 AND project_id = 2 AND resource_id = 1;
		UPDATE project_resources SET backend_quota = 13 WHERE id = 4 AND project_id = 2 AND resource_id = 2;
		UPDATE project_services SET quota_desynced_at = NULL WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET quota_desynced_at = NULL WHERE id = 2 AND project_id = 2 AND service_id = 1;
	`)

	// test SyncQuotaToBackendJob not having anything to do
	mustFailT(t, syncJob.ProcessOne(s.Ctx, withLabel), sql.ErrNoRows)
	tr.DBChanges().AssertEmpty()

	// Scrape should show that the quota update was durable
	s.Clock.StepBy(collector.ScrapeInterval)
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(collector.ScrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(collector.ScrapeInterval).Unix(),
	)

	// set "capacity" to a non-zero usage to observe a non-zero usage
	s.Clock.StepBy(collector.ScrapeInterval)
	// note: there is currently no concistency check between the metrics and the actual resources
	serviceUsageReport.Resources["capacity"].PerAZ["az-one"].Usage = 20
	serviceUsageReport.Metrics["limes_unittest_capacity_usage"] = []liquid.Metric{{Value: 20}}
	serviceUsageReport.Resources["capacity"].PerAZ["az-one"].PhysicalUsage = Some[uint64](10)

	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET usage = 20, physical_usage = 10, historical_usage = '{"t":[%[5]d,%[1]d],"v":[0,20]}' WHERE id = 2 AND project_id = 1 AND az_resource_id = 2;
		UPDATE project_az_resources SET usage = 20, physical_usage = 10, historical_usage = '{"t":[%[6]d,%[3]d],"v":[0,20]}' WHERE id = 8 AND project_id = 2 AND az_resource_id = 2;
		UPDATE project_services SET scraped_at = %[1]d, serialized_metrics = '{"limes_unittest_capacity_usage":{"lk":null,"m":[{"v":20,"l":null}]},"limes_unittest_things_usage":{"lk":null,"m":[{"v":3,"l":null}]}}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET scraped_at = %[3]d, serialized_metrics = '{"limes_unittest_capacity_usage":{"lk":null,"m":[{"v":20,"l":null}]},"limes_unittest_things_usage":{"lk":null,"m":[{"v":3,"l":null}]}}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(collector.ScrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(collector.ScrapeInterval).Unix(),
		firstScrapedAt1.Unix(),
		firstScrapedAt2.Unix(),
	)

	// add some commitments in order to test the `limes_project_committed_per_az` metric below
	commitmentForOneYear, err := limesresources.ParseCommitmentDuration("1 year")
	mustT(t, err)
	now := s.Clock.Now()
	// AZResourceID = 2 has two commitments in status "confirmed" to test summing by status
	creationContext := db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate}
	buf, err := json.Marshal(creationContext)
	mustT(t, err)
	for idx, amount := range []uint64{7, 8} {
		mustT(t, s.DB.Insert(&db.ProjectCommitment{
			UUID:                liquid.CommitmentUUID(fmt.Sprintf("00000000-0000-0000-0000-%012d", idx+1)),
			ProjectID:           1,
			AZResourceID:        2,
			Amount:              amount,
			Duration:            commitmentForOneYear,
			CreatedAt:           now,
			CreatorUUID:         "dummy",
			CreatorName:         "dummy",
			ConfirmedAt:         Some(now),
			ExpiresAt:           commitmentForOneYear.AddTo(now),
			Status:              liquid.CommitmentStatusConfirmed,
			CreationContextJSON: buf,
		}))
	}
	// AZResourceID = 8 has two commitments in different statuses to test aggregation over different statuses
	mustT(t, s.DB.Insert(&db.ProjectCommitment{
		UUID:                "00000000-0000-0000-0000-000000000003",
		ProjectID:           2,
		AZResourceID:        2,
		Amount:              10,
		Duration:            commitmentForOneYear,
		CreatedAt:           now,
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ConfirmedAt:         Some(now),
		ExpiresAt:           commitmentForOneYear.AddTo(now),
		Status:              liquid.CommitmentStatusConfirmed,
		CreationContextJSON: buf,
	}))
	mustT(t, s.DB.Insert(&db.ProjectCommitment{
		UUID:                "00000000-0000-0000-0000-000000000004",
		ProjectID:           2,
		AZResourceID:        2,
		Amount:              10,
		Duration:            commitmentForOneYear,
		CreatedAt:           now,
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ConfirmBy:           Some(now),
		ExpiresAt:           commitmentForOneYear.AddTo(now),
		Status:              liquid.CommitmentStatusPending,
		CreationContextJSON: buf,
	}))
	tr.DBChanges().Ignore()

	// test that changes in rates are reflected in the db
	serviceUsageReport.Rates["firstrate"].PerAZ["any"].Usage = Some(big.NewInt(2048))
	serviceUsageReport.Rates["secondrate"].PerAZ["any"].Usage = Some(big.NewInt(4096))
	serviceUsageReport.SerializedState = []byte(`{"firstrate":2048,"secondrate":4096}`)
	s.LiquidClients["unittest"].SetUsageReport(serviceUsageReport)

	s.Clock.StepBy(collector.ScrapeInterval)
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	serviceUsageReport.Rates["firstrate"].PerAZ["any"].Usage = Some(big.NewInt(4096))
	serviceUsageReport.Rates["secondrate"].PerAZ["any"].Usage = Some(big.NewInt(8192))
	serviceUsageReport.SerializedState = []byte(`{"firstrate":4096,"secondrate":8192}`)
	s.LiquidClients["unittest"].SetUsageReport(serviceUsageReport)

	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_rates SET usage_as_bigint = '2048' WHERE id = 3 AND project_id = 1 AND rate_id = 1;
		UPDATE project_rates SET usage_as_bigint = '4096' WHERE id = 4 AND project_id = 1 AND rate_id = 2;
		UPDATE project_rates SET usage_as_bigint = '4096' WHERE id = 5 AND project_id = 2 AND rate_id = 1;
		UPDATE project_rates SET usage_as_bigint = '8192' WHERE id = 6 AND project_id = 2 AND rate_id = 2;
		UPDATE project_services SET scraped_at = %[1]d, serialized_scrape_state = '{"firstrate":2048,"secondrate":4096}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET scraped_at = %[3]d, serialized_scrape_state = '{"firstrate":4096,"secondrate":8192}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(collector.ScrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(collector.ScrapeInterval).Unix(),
	)

	// check that az='unknown' is skipped in metrics only when capacity=0
	_, err = s.DB.Exec("UPDATE az_resources SET raw_capacity = 1234 WHERE path = $1", "unittest/capacity/unknown")
	mustT(t, err)

	// check data metrics generated by this scraping pass
	registry := prometheus.NewPedanticRegistry()
	amc := &collector.AggregateMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(amc)
	umc := &collector.UsageCollectionMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(umc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": collector.ContentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/scrape_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	dmr := &collector.DataMetricsReporter{Cluster: s.Cluster, DB: s.DB, ReportZeroes: true}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": collector.ContentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/scrape_data_metrics.prom"),
	}.Check(t, dmr)

	// check data metrics with the skip_zero flag set
	dmr.ReportZeroes = false
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": collector.ContentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/scrape_data_metrics_skipzero.prom"),
	}.Check(t, dmr)
}

func Test_ScrapeFailure(t *testing.T) {
	s, job, withLabel, _, _, serviceUsageReport := commonComplexScrapeTestSetup(t)

	// we will see an expected ERROR during testing, do not make the test fail because of this
	expectedErrorRx := regexp.MustCompile(`^during scrape of project germany/(berlin|dresden): GetUsageReport failed as requested$`)

	// check that ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	// failing Scrape should create dummy records to ensure that the API finds
	// plausibly-structured data
	//
	// Note that this does *not* set quota_desynced_at. We would rather not
	// write any quotas while we cannot even get correct usage numbers.
	s.Clock.StepBy(collector.ScrapeInterval)
	s.LiquidClients["unittest"].SetUsageReportError(errors.New("GetUsageReport failed as requested"))
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx) // twice because there are two projects

	checkedAt1 := s.Clock.Now().Add(-5 * time.Second)
	checkedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (1, 1, 1, 0);
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (2, 1, 5, 0);
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (3, 2, 1, 0);
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (4, 2, 5, 0);
		INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (1, 1, 1, 0, -1);
		INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (2, 1, 2, 0, -1);
		INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (3, 2, 1, 0, -1);
		INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (4, 2, 2, 0, -1);
		UPDATE project_services SET scraped_at = 0, stale = FALSE, checked_at = %[1]d, scrape_error_message = 'GetUsageReport failed as requested', next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET scraped_at = 0, stale = FALSE, checked_at = %[3]d, scrape_error_message = 'GetUsageReport failed as requested', next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
	`,
		checkedAt1.Unix(), checkedAt1.Add(collector.RecheckInterval).Unix(),
		checkedAt2.Unix(), checkedAt2.Add(collector.RecheckInterval).Unix(),
	)

	// next Scrape should yield the same result
	s.Clock.StepBy(collector.ScrapeInterval)
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)

	checkedAt1 = s.Clock.Now().Add(-5 * time.Second)
	checkedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
	`,
		checkedAt1.Unix(), checkedAt1.Add(collector.RecheckInterval).Unix(),
		checkedAt2.Unix(), checkedAt2.Add(collector.RecheckInterval).Unix(),
	)

	// once the backend starts working, we start to see plausible data again
	s.Clock.StepBy(collector.ScrapeInterval)

	s.LiquidClients["unittest"].SetUsageReportError(nil)
	s.LiquidClients["unittest"].SetUsageReport(serviceUsageReport)

	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel)) // twice because there are two projects

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET historical_usage = '{"t":[%[1]d],"v":[0]}' WHERE id = 1 AND project_id = 1 AND az_resource_id = 1;
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, physical_usage, historical_usage) VALUES (10, 2, 3, 0, 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, subresources, historical_usage) VALUES (11, 2, 6, 2, '[{"name":"index","usage":0},{"name":"index","usage":1}]', '{"t":[%[3]d],"v":[2]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, subresources, historical_usage) VALUES (12, 2, 7, 2, '[{"name":"index","usage":2},{"name":"index","usage":3}]', '{"t":[%[3]d],"v":[2]}');
		UPDATE project_az_resources SET historical_usage = '{"t":[%[1]d],"v":[0]}' WHERE id = 2 AND project_id = 1 AND az_resource_id = 5;
		UPDATE project_az_resources SET historical_usage = '{"t":[%[3]d],"v":[0]}' WHERE id = 3 AND project_id = 2 AND az_resource_id = 1;
		UPDATE project_az_resources SET historical_usage = '{"t":[%[3]d],"v":[0]}' WHERE id = 4 AND project_id = 2 AND az_resource_id = 5;
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, physical_usage, historical_usage) VALUES (5, 1, 2, 0, 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, physical_usage, historical_usage) VALUES (6, 1, 3, 0, 0, '{"t":[%[1]d],"v":[0]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, subresources, historical_usage) VALUES (7, 1, 6, 2, '[{"name":"index","usage":0},{"name":"index","usage":1}]', '{"t":[%[1]d],"v":[2]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, subresources, historical_usage) VALUES (8, 1, 7, 2, '[{"name":"index","usage":2},{"name":"index","usage":3}]', '{"t":[%[1]d],"v":[2]}');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, physical_usage, historical_usage) VALUES (9, 2, 2, 0, 0, '{"t":[%[3]d],"v":[0]}');
		INSERT INTO project_rates (id, project_id, rate_id, usage_as_bigint) VALUES (3, 1, 1, '1024');
		INSERT INTO project_rates (id, project_id, rate_id, usage_as_bigint) VALUES (4, 1, 2, '2048');
		INSERT INTO project_rates (id, project_id, rate_id, usage_as_bigint) VALUES (5, 2, 1, '1024');
		INSERT INTO project_rates (id, project_id, rate_id, usage_as_bigint) VALUES (6, 2, 2, '2048');
		UPDATE project_resources SET backend_quota = 100 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		UPDATE project_resources SET backend_quota = 42 WHERE id = 2 AND project_id = 1 AND resource_id = 2;
		UPDATE project_resources SET backend_quota = 100 WHERE id = 3 AND project_id = 2 AND resource_id = 1;
		UPDATE project_resources SET backend_quota = 42 WHERE id = 4 AND project_id = 2 AND resource_id = 2;
		UPDATE project_services SET scraped_at = %[1]d, scrape_duration_secs = 5, serialized_scrape_state = '{"firstrate":1024,"secondrate":2048}', serialized_metrics = '{"limes_unittest_capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"limes_unittest_things_usage":{"lk":null,"m":[{"v":4,"l":null}]}}', checked_at = %[1]d, scrape_error_message = '', next_scrape_at = %[2]d, quota_desynced_at = %[1]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET scraped_at = %[3]d, scrape_duration_secs = 5, serialized_scrape_state = '{"firstrate":1024,"secondrate":2048}', serialized_metrics = '{"limes_unittest_capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"limes_unittest_things_usage":{"lk":null,"m":[{"v":4,"l":null}]}}', checked_at = %[3]d, scrape_error_message = '', next_scrape_at = %[4]d, quota_desynced_at = %[3]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(collector.ScrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(collector.ScrapeInterval).Unix(),
	)

	// backend fails again and we need to scrape because of the stale flag ->
	// touch neither scraped_at nor the existing resources (this also tests that a
	// failed check causes Scrape("unittest") to continue with the next resource afterwards)
	s.Clock.StepBy(collector.ScrapeInterval)
	s.LiquidClients["unittest"].SetUsageReportError(errors.New("GetUsageReport failed as requested"))
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx)
	mustFailLikeT(t, job.ProcessOne(s.Ctx, withLabel), expectedErrorRx) // twice because there are two projects

	checkedAt1 = s.Clock.Now().Add(-5 * time.Second)
	checkedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET checked_at = %[1]d, scrape_error_message = 'GetUsageReport failed as requested', next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET checked_at = %[3]d, scrape_error_message = 'GetUsageReport failed as requested', next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
	`,
		checkedAt1.Unix(), checkedAt1.Add(collector.RecheckInterval).Unix(),
		checkedAt2.Unix(), checkedAt2.Add(collector.RecheckInterval).Unix(),
	)
}

const (
	testNoopConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: static
			static_config:
				domains:
					- { name: germany, id: uuid-for-germany }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
		liquids:
			noop:
				area: testing
	`
)

func Test_ScrapeButNoResources(t *testing.T) {
	srvInfo := liquid.ServiceInfo{
		Version:   1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{},
	}
	s := test.NewSetup(t,
		test.WithConfig(testNoopConfigYAML),
		test.WithMockLiquidClient("noop", srvInfo),
		test.WithLiquidConnections,
	)
	prepareDomainsAndProjectsForScrape(t, s)
	initialTime := s.Clock.Now()

	// override some defaults we set in the MockLiquidClient
	s.LiquidClients["noop"].SetUsageReport(liquid.ServiceUsageReport{InfoVersion: 1})

	c := getCollector(t, s)
	job := c.ScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "noop")

	// check that Scrape() behaves properly when encountering a liquid with
	// no Resources() (in the wild, this can happen because some liquids
	// only have Rates())
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt := s.Clock.Now()
	_, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_services (id, project_id, service_id, scraped_at, scrape_duration_secs, serialized_metrics, checked_at, next_scrape_at) VALUES (1, 1, 1, %[2]d, 5, '{}', %[2]d, %[3]d);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'noop', %[1]d, 1);
	`,
		initialTime.Unix(), scrapedAt.Unix(), scrapedAt.Add(collector.ScrapeInterval).Unix(),
	)
}

////////////////////////////////////////////////////////////////////////////////
// test for empty UsageData

func Test_ScrapeReturnsNoUsageData(t *testing.T) {
	srvInfo := liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"things": {Unit: limes.UnitNone, HasQuota: true, Topology: liquid.AZAwareTopology},
		},
	}
	s := test.NewSetup(t,
		test.WithConfig(testNoopConfigYAML),
		test.WithMockLiquidClient("noop", srvInfo),
		test.WithLiquidConnections,
	)
	prepareDomainsAndProjectsForScrape(t, s)
	initialTime := s.Clock.Now()

	// override some defaults we set in the MockLiquidClient
	s.LiquidClients["noop"].SetUsageReport(liquid.ServiceUsageReport{InfoVersion: 1})

	c := getCollector(t, s)
	job := c.ScrapeJob(s.Registry)
	withLabel := jobloop.WithLabel("service_type", "noop")

	// check that Scrape() behaves properly when encountering a liquid with
	// no Resources() (in the wild, this can happen because some liquids
	// only have Rates())
	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), errors.New(`during scrape of project germany/berlin: received ServiceUsageReport is invalid: missing value for .Resources["things"]`))

	scrapedAt := s.Clock.Now()
	_, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (1, 1, 'any', 0, 'noop/things/any');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (2, 1, 'az-one', 0, 'noop/things/az-one');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (3, 1, 'az-two', 0, 'noop/things/az-two');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (4, 1, 'unknown', 0, 'noop/things/unknown');
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage) VALUES (1, 1, 1, 0);
		INSERT INTO project_resources (id, project_id, resource_id, quota, backend_quota) VALUES (1, 1, 1, 0, -1);
		INSERT INTO project_services (id, project_id, service_id, scraped_at, checked_at, scrape_error_message, next_scrape_at) VALUES (1, 1, 1, 0, %[2]d, 'received ServiceUsageReport is invalid: missing value for .Resources["things"]', %[3]d);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany');
		INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path) VALUES (1, 1, 'things', 1, 'az-aware', TRUE, 'noop/things');
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'noop', %[1]d, 1);
	`,
		initialTime.Unix(), scrapedAt.Unix(), scrapedAt.Add(collector.RecheckInterval).Unix(),
	)
}

func Test_TopologyScrapes(t *testing.T) {
	s, job, withLabel, syncJob, srvInfo, serviceUsageReport := commonComplexScrapeTestSetup(t)

	// use AZSeperated topology and adjust quota reporting accordingly
	resInfoCap := srvInfo.Resources["capacity"]
	resInfoThings := srvInfo.Resources["things"]
	resCap := serviceUsageReport.Resources["capacity"]
	resThings := serviceUsageReport.Resources["things"]

	resInfoCap.Topology = liquid.AZSeparatedTopology
	resInfoThings.Topology = liquid.AZSeparatedTopology
	srvInfo.Resources["capacity"] = resInfoCap
	srvInfo.Resources["things"] = resInfoThings

	resCap.Quota = None[int64]()
	resCap.PerAZ["az-one"].Quota = Some[int64](50)
	resCap.PerAZ["az-two"].Quota = Some[int64](50)
	resThings.Quota = None[int64]()
	resThings.PerAZ["az-one"].Quota = Some[int64](21)
	resThings.PerAZ["az-two"].Quota = Some[int64](21)
	serviceUsageReport.Resources["capacity"] = resCap
	serviceUsageReport.Resources["things"] = resThings

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	// positive: Sync az-separated quota values with the backend
	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, physical_usage, historical_usage, backend_quota) VALUES (1, 1, 2, 0, 0, '{"t":[%[1]d],"v":[0]}', 50);
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, physical_usage, historical_usage, backend_quota) VALUES (2, 1, 3, 0, 0, '{"t":[%[1]d],"v":[0]}', 50);
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, subresources, historical_usage, backend_quota) VALUES (3, 1, 6, 2, '[{"name":"index","usage":0},{"name":"index","usage":1}]', '{"t":[%[1]d],"v":[2]}', 21);
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, subresources, historical_usage, backend_quota) VALUES (4, 1, 7, 2, '[{"name":"index","usage":2},{"name":"index","usage":3}]', '{"t":[%[1]d],"v":[2]}', 21);
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, physical_usage, historical_usage, backend_quota) VALUES (5, 2, 2, 0, 0, '{"t":[%[3]d],"v":[0]}', 50);
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, physical_usage, historical_usage, backend_quota) VALUES (6, 2, 3, 0, 0, '{"t":[%[3]d],"v":[0]}', 50);
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, subresources, historical_usage, backend_quota) VALUES (7, 2, 6, 2, '[{"name":"index","usage":0},{"name":"index","usage":1}]', '{"t":[%[3]d],"v":[2]}', 21);
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, subresources, historical_usage, backend_quota) VALUES (8, 2, 7, 2, '[{"name":"index","usage":2},{"name":"index","usage":3}]', '{"t":[%[3]d],"v":[2]}', 21);
		INSERT INTO project_rates (id, project_id, rate_id, usage_as_bigint) VALUES (3, 1, 1, '1024');
		INSERT INTO project_rates (id, project_id, rate_id, usage_as_bigint) VALUES (4, 1, 2, '2048');
		INSERT INTO project_rates (id, project_id, rate_id, usage_as_bigint) VALUES (5, 2, 1, '1024');
		INSERT INTO project_rates (id, project_id, rate_id, usage_as_bigint) VALUES (6, 2, 2, '2048');
		INSERT INTO project_resources (id, project_id, resource_id) VALUES (1, 1, 1);
		INSERT INTO project_resources (id, project_id, resource_id) VALUES (2, 1, 2);
		INSERT INTO project_resources (id, project_id, resource_id) VALUES (3, 2, 1);
		INSERT INTO project_resources (id, project_id, resource_id) VALUES (4, 2, 2);
		UPDATE project_services SET scraped_at = %[1]d, stale = FALSE, scrape_duration_secs = 5, serialized_scrape_state = '{"firstrate":1024,"secondrate":2048}', serialized_metrics = '{"limes_unittest_capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"limes_unittest_things_usage":{"lk":null,"m":[{"v":4,"l":null}]}}', checked_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET scraped_at = %[3]d, stale = FALSE, scrape_duration_secs = 5, serialized_scrape_state = '{"firstrate":1024,"secondrate":2048}', serialized_metrics = '{"limes_unittest_capacity_usage":{"lk":null,"m":[{"v":0,"l":null}]},"limes_unittest_things_usage":{"lk":null,"m":[{"v":4,"l":null}]}}', checked_at = %[3]d, next_scrape_at = %[4]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
		`,
		scrapedAt1.Unix(), scrapedAt1.Add(collector.ScrapeInterval).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(collector.ScrapeInterval).Unix(),
	)

	// set some quota acpq values.
	_, err := s.DB.Exec(`UPDATE project_az_resources SET quota = $1 WHERE az_resource_id BETWEEN 2 AND 4`, 20)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`UPDATE project_az_resources SET quota = $1 WHERE az_resource_id BETWEEN 6 AND 8`, 13)
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
		UPDATE project_az_resources SET backend_quota = 20 WHERE id = 1 AND project_id = 1 AND az_resource_id = 2;
		UPDATE project_az_resources SET backend_quota = 20 WHERE id = 2 AND project_id = 1 AND az_resource_id = 3;
		UPDATE project_az_resources SET backend_quota = 13 WHERE id = 3 AND project_id = 1 AND az_resource_id = 6;
		UPDATE project_az_resources SET backend_quota = 13 WHERE id = 4 AND project_id = 1 AND az_resource_id = 7;
		UPDATE project_az_resources SET backend_quota = 20 WHERE id = 5 AND project_id = 2 AND az_resource_id = 2;
		UPDATE project_az_resources SET backend_quota = 20 WHERE id = 6 AND project_id = 2 AND az_resource_id = 3;
		UPDATE project_az_resources SET backend_quota = 13 WHERE id = 7 AND project_id = 2 AND az_resource_id = 6;
		UPDATE project_az_resources SET backend_quota = 13 WHERE id = 8 AND project_id = 2 AND az_resource_id = 7;
		UPDATE project_services SET quota_desynced_at = NULL, quota_sync_duration_secs = 5 WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET quota_desynced_at = NULL, quota_sync_duration_secs = 5 WHERE id = 2 AND project_id = 2 AND service_id = 1;
	`)

	s.Clock.StepBy(collector.ScrapeInterval)

	// topology of a resource changes. Reset AZ-separated backend_quota
	resInfoThings.Topology = liquid.AZAwareTopology
	srvInfo.Resources["things"] = resInfoThings
	resThings.Quota = Some[int64](42)
	resThings.PerAZ["az-one"].Quota = None[int64]()
	resThings.PerAZ["az-two"].Quota = None[int64]()
	// in reality, this would be an update of the liquid, so we bump the version that the liquid and the report return
	s.LiquidClients["unittest"].IncrementServiceInfoVersion()
	s.LiquidClients["unittest"].IncrementUsageReportInfoVersion()

	mustT(t, job.ProcessOne(s.Ctx, withLabel))
	mustT(t, job.ProcessOne(s.Ctx, withLabel))

	checkedAt1 := s.Clock.Now().Add(-5 * time.Second)
	checkedAt2 := s.Clock.Now()
	// note: cluster_rate "xAnotherRate" is orphaned - it is in the DB but not in the ServiceInfo and rate_limits, so the update now deletes it (incl. project references)
	tr.DBChanges().AssertEqualf(`
		DELETE FROM az_resources WHERE id = 1 AND resource_id = 1 AND az = 'any' AND path = 'unittest/capacity/any';
		UPDATE project_az_resources SET backend_quota = 50 WHERE id = 1 AND project_id = 1 AND az_resource_id = 2;
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, historical_usage) VALUES (10, 2, 5, 0, '{"t":[1830],"v":[0]}');
		UPDATE project_az_resources SET backend_quota = 50 WHERE id = 2 AND project_id = 1 AND az_resource_id = 3;
		UPDATE project_az_resources SET backend_quota = NULL WHERE id = 3 AND project_id = 1 AND az_resource_id = 6;
		UPDATE project_az_resources SET backend_quota = NULL WHERE id = 4 AND project_id = 1 AND az_resource_id = 7;
		UPDATE project_az_resources SET backend_quota = 50 WHERE id = 5 AND project_id = 2 AND az_resource_id = 2;
		UPDATE project_az_resources SET backend_quota = 50 WHERE id = 6 AND project_id = 2 AND az_resource_id = 3;
		UPDATE project_az_resources SET backend_quota = NULL WHERE id = 7 AND project_id = 2 AND az_resource_id = 6;
		UPDATE project_az_resources SET backend_quota = NULL WHERE id = 8 AND project_id = 2 AND az_resource_id = 7;
		INSERT INTO project_az_resources (id, project_id, az_resource_id, usage, historical_usage) VALUES (9, 1, 5, 0, '{"t":[1825],"v":[0]}');
		DELETE FROM project_rates WHERE id = 2 AND project_id = 1 AND rate_id = 4;
		UPDATE project_resources SET quota = 0, backend_quota = 42 WHERE id = 2 AND project_id = 1 AND resource_id = 2;
		UPDATE project_resources SET quota = 0, backend_quota = 42 WHERE id = 4 AND project_id = 2 AND resource_id = 2;
		UPDATE project_services SET scraped_at = %[1]d, checked_at = %[1]d, next_scrape_at = %[2]d, quota_desynced_at = %[1]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET scraped_at = %[3]d, checked_at = %[3]d, next_scrape_at = %[4]d, quota_desynced_at = %[3]d WHERE id = 2 AND project_id = 2 AND service_id = 1;
		UPDATE rates SET liquid_version = 2 WHERE id = 1 AND service_id = 1 AND name = 'firstrate';
		UPDATE rates SET liquid_version = 2 WHERE id = 2 AND service_id = 1 AND name = 'secondrate';
		UPDATE rates SET liquid_version = 2 WHERE id = 3 AND service_id = 1 AND name = 'xOtherRate';
		DELETE FROM rates WHERE id = 4 AND service_id = 1 AND name = 'xAnotherRate';
		UPDATE resources SET liquid_version = 2, topology = 'az-separated' WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'unittest/capacity';
		UPDATE resources SET liquid_version = 2 WHERE id = 2 AND service_id = 1 AND name = 'things' AND path = 'unittest/things';
		DELETE FROM services WHERE id = 1 AND type = 'unittest' AND liquid_version = 1;
		INSERT INTO services (id, type, next_scrape_at, liquid_version, usage_metric_families_json) VALUES (1, 'unittest', 0, 2, '{"limes_unittest_capacity_usage":{"type":"gauge","help":"","labelKeys":null},"limes_unittest_things_usage":{"type":"gauge","help":"","labelKeys":null}}');
	`,
		checkedAt1.Unix(), checkedAt1.Add(collector.ScrapeInterval).Unix(),
		checkedAt2.Unix(), checkedAt2.Add(collector.ScrapeInterval).Unix(),
	)

	s.Clock.StepBy(collector.ScrapeInterval)
	// negative: service info validation should fail with invalid AZs
	resInfoCap.Topology = "invalidAZ1"
	srvInfo.Resources["capacity"] = resInfoCap
	// in reality, this would be an update of the liquid, so we bump the version that the liquid and the report returns
	s.LiquidClients["unittest"].IncrementServiceInfoVersion()
	s.LiquidClients["unittest"].IncrementUsageReportInfoVersion()

	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), errors.New("during scrape of project germany/berlin: received ServiceInfo is invalid: .Resources[\"capacity\"] has invalid topology \"invalidAZ1\""))

	s.Clock.StepBy(collector.ScrapeInterval)
	// negative: service usage report validation should fail for mismatched topology and AZ reports
	resInfoCap.Topology = liquid.FlatTopology
	srvInfo.Resources["capacity"] = resInfoCap
	mustFailT(t, job.ProcessOne(s.Ctx, withLabel), errors.New("during scrape of project germany/dresden: received ServiceUsageReport is invalid: .Resources[\"capacity\"].PerAZ has entries for []liquid.AvailabilityZone{\"az-one\", \"az-two\"}, which is invalid for topology \"flat\" (expected entries for []liquid.AvailabilityZone{\"any\"}); .Resources[\"capacity\"] has no quota reported on resource level, which is invalid for HasQuota = true and topology \"flat\""))
}
