// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/majewsky/gg/option"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/collector"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

const (
	testScanCapacityConfigJSON = `{
		"availability_zones": ["az-one", "az-two"],
		"discovery": {
			"method": "static",
			"static_config": {
				"domains": [
					{"name": "germany", "id": "uuid-for-germany"},
					{"name": "france", "id": "uuid-for-france"}
				],
				"projects": {
					"uuid-for-germany": [
						{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"},
						{"name": "dresden", "id": "uuid-for-dresden", "parent_id": "uuid-for-berlin"}
					],
					"uuid-for-france": [
						{"name": "paris", "id": "uuid-for-paris", "parent_id": "uuid-for-france"}
					]
				}
			}
		},
		"liquids": {
			"shared": {"area": "shared"},
			"unshared": {"area": "unshared"}
		}
	}`

	testScanCapacitySingleLiquidConfigJSON = `{
		"availability_zones": ["az-one", "az-two"],
		"discovery": {
			"method": "static",
			"static_config": {
				"domains": [
					{"name": "germany", "id": "uuid-for-germany"},
					{"name": "france", "id": "uuid-for-france"}
				],
				"projects": {
					"uuid-for-germany": [
						{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"},
						{"name": "dresden", "id": "uuid-for-dresden", "parent_id": "uuid-for-berlin"}
					],
					"uuid-for-france": [
						{"name": "paris", "id": "uuid-for-paris", "parent_id": "uuid-for-france"}
					]
				}
			}
		},
		"liquids": {
			"shared": {"area": "shared"}
		}
	}`
)

func Test_ScanCapacity(t *testing.T) {
	srvInfo := liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"things": {
				Unit:        liquid.UnitNone,
				Topology:    liquid.FlatTopology,
				HasCapacity: true,
				HasQuota:    true,
			},
		},
	}
	srvInfo2 := liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"capacity": {
				Unit:        liquid.UnitBytes,
				Topology:    liquid.FlatTopology,
				HasCapacity: true,
				HasQuota:    true,
			},
		},
	}
	s := test.NewSetup(t,
		test.WithConfig(testScanCapacityConfigJSON),
		test.WithMockLiquidClient("shared", srvInfo),
		test.WithMockLiquidClient("unshared", srvInfo2),
		// services must be created as a baseline
		test.WithLiquidConnections,
	)

	job := s.Collector.CapacityScrapeJob(s.Registry)
	insertTime := s.Clock.Now()

	s.LiquidClients["shared"].CapacityReport.Set(liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"things": {
				PerAZ: liquid.InAnyAZ(liquid.AZResourceCapacityReport{
					Capacity: 42,
					Usage:    Some[uint64](8),
				}),
			},
		},
	})
	s.LiquidClients["unshared"].CapacityReport.Set(liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"capacity": {
				PerAZ: liquid.InAnyAZ(liquid.AZResourceCapacityReport{
					Capacity: 42,
					Usage:    Some[uint64](8),
				}),
			},
		},
	})

	// check baseline
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (1, 1, 'any', 0, 'shared/things/any');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (2, 2, 'any', 0, 'unshared/capacity/any');
		INSERT INTO resources (id, service_id, name, liquid_version, topology, has_capacity, has_quota, path) VALUES (1, 1, 'things', 1, 'flat', TRUE, TRUE, 'shared/things');
		INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, has_quota, path) VALUES (2, 2, 'capacity', 1, 'B', 'flat', TRUE, TRUE, 'unshared/capacity');
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', %[1]d, 1);
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (2, 'unshared', %[1]d, 1);
	`, s.Clock.Now().Unix())

	// check that capacity records are created correctly (and that nonexistent
	// resources are ignored by the scraper)
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))
	tr.DBChanges().AssertEqualf(`
		UPDATE az_resources SET raw_capacity = 42, usage = 8, last_nonzero_raw_capacity = 42 WHERE id = 1 AND resource_id = 1 AND az = 'any' AND path = 'shared/things/any';
		UPDATE az_resources SET raw_capacity = 42, usage = 8, last_nonzero_raw_capacity = 42 WHERE id = 2 AND resource_id = 2 AND az = 'any' AND path = 'unshared/capacity/any';
		UPDATE services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = 905 WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
		UPDATE services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = 910 WHERE id = 2 AND type = 'unshared' AND liquid_version = 1;
	`, insertTime.Add(5*time.Second).Unix(), insertTime.Add(10*time.Second).Unix())

	// insert some crap records
	unknownRes := &db.Resource{
		ServiceID:     2,
		Name:          "unknown",
		Path:          "unshared/unknown",
		LiquidVersion: 1,
	}
	s.MustDBInsert(unknownRes)
	s.MustDBInsert(&db.AZResource{
		ResourceID:       unknownRes.ID,
		AvailabilityZone: liquid.AvailabilityZoneAny,
		Path:             "unshared/unknown/" + string(liquid.AvailabilityZoneAny),
		RawCapacity:      100,
		Usage:            Some[uint64](50),
	})
	s.MustDBExec(
		`DELETE FROM resources WHERE service_id = $1 AND name = $2`,
		1, "things",
	)
	s.LiquidClients["shared"].CapacityReport.Modify(func(report *liquid.ServiceCapacityReport) {
		report.Resources["things"].PerAZ["any"].Capacity = 23
		report.Resources["things"].PerAZ["any"].Usage = Some[uint64](4)
	})
	tr.DBChanges().Ignore()

	// if we don't bump the version, we will observe that for "things" nothing happens (as it is unknown
	// to the database) and for "unknown" there is no value
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
		UPDATE services SET scraped_at = %d, next_scrape_at = %d WHERE id = 2 AND type = 'unshared' AND liquid_version = 1;
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
	)

	// now we bump the version, so that the services and resources are reconciled
	s.LiquidClients["shared"].IncrementServiceInfoVersion()
	s.LiquidClients["shared"].IncrementCapacityReportInfoVersion()
	s.LiquidClients["unshared"].IncrementServiceInfoVersion()
	s.LiquidClients["unshared"].IncrementCapacityReportInfoVersion()
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		DELETE FROM az_resources WHERE id = 3 AND resource_id = 3 AND az = 'any' AND path = 'unshared/unknown/any';
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity, path) VALUES (4, 4, 'any', 23, 4, 23, 'shared/things/any');
		UPDATE resources SET liquid_version = 2 WHERE id = 2 AND service_id = 2 AND name = 'capacity' AND path = 'unshared/capacity';
		DELETE FROM resources WHERE id = 3 AND service_id = 2 AND name = 'unknown' AND path = 'unshared/unknown';
		INSERT INTO resources (id, service_id, name, liquid_version, topology, has_capacity, has_quota, path) VALUES (4, 1, 'things', 2, 'flat', TRUE, TRUE, 'shared/things');
		DELETE FROM services WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
		INSERT INTO services (id, type, scraped_at, scrape_duration_secs, serialized_metrics, next_scrape_at, liquid_version) VALUES (1, 'shared', %d, 5, '{}', %d, 2);
		DELETE FROM services WHERE id = 2 AND type = 'unshared' AND liquid_version = 1;
		INSERT INTO services (id, type, scraped_at, scrape_duration_secs, serialized_metrics, next_scrape_at, liquid_version) VALUES (2, 'unshared', %d, 5, '{}', %d, 2);
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
	)

	dmr := &collector.DataMetricsReporter{Cluster: s.Cluster, DB: s.DB, ReportZeroes: true}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": collector.ContentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/capacity_data_metrics.prom"),
	}.Check(t, dmr)
}

func Test_ScanCapacityWithSubcapacities(t *testing.T) {
	srvInfo := liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"things": {
				Unit:        liquid.UnitNone,
				Topology:    liquid.FlatTopology,
				HasCapacity: true,
				HasQuota:    true,
			},
		},
		CapacityMetricFamilies: map[liquid.MetricName]liquid.MetricFamilyInfo{
			"limes_unittest_capacity_smaller_half": {Type: liquid.MetricTypeGauge},
			"limes_unittest_capacity_larger_half":  {Type: liquid.MetricTypeGauge},
		},
	}
	s := test.NewSetup(t,
		test.WithConfig(testScanCapacitySingleLiquidConfigJSON),
		test.WithMockLiquidClient("shared", srvInfo),
		// services must be created as a baseline
		test.WithLiquidConnections,
	)

	job := s.Collector.CapacityScrapeJob(s.Registry)

	// check baseline
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (1, 1, 'any', 0, 'shared/things/any');
		INSERT INTO resources (id, service_id, name, liquid_version, topology, has_capacity, has_quota, path) VALUES (1, 1, 'things', 1, 'flat', TRUE, TRUE, 'shared/things');
		INSERT INTO services (id, type, next_scrape_at, liquid_version, capacity_metric_families_json) VALUES (1, 'shared', %[1]d, 1, '{"limes_unittest_capacity_larger_half":{"type":"gauge","help":"","labelKeys":null},"limes_unittest_capacity_smaller_half":{"type":"gauge","help":"","labelKeys":null}}');
	`, s.Clock.Now().Unix())

	// check that scraping correctly updates subcapacities on an existing record
	buf := must.Return(json.Marshal(map[string]any{"az": "az-one"}))
	buf2 := must.Return(json.Marshal(map[string]any{"az": "az-two"}))
	s.LiquidClients["shared"].CapacityReport.Set(liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"things": {
				PerAZ: liquid.InAnyAZ(liquid.AZResourceCapacityReport{
					Capacity: 42,
					Subcapacities: []liquid.Subcapacity{
						{Name: "smaller_half", Capacity: 7, Attributes: json.RawMessage(buf)},
						{Name: "larger_half", Capacity: 14, Attributes: json.RawMessage(buf)},
						{Name: "smaller_half", Capacity: 7, Attributes: json.RawMessage(buf2)},
						{Name: "larger_half", Capacity: 14, Attributes: json.RawMessage(buf2)},
					},
				}),
			},
		},
		Metrics: map[liquid.MetricName][]liquid.Metric{
			"limes_unittest_capacity_smaller_half": {{Value: 3}},
			"limes_unittest_capacity_larger_half":  {{Value: 7}},
		},
	})
	setClusterCapacitorsStale(t, s)
	s.Clock.StepBy(5 * time.Minute) // to force a capacitor consistency check to run
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE az_resources SET raw_capacity = 42, subcapacities = '[{"name":"smaller_half","capacity":7,"attributes":{"az":"az-one"}},{"name":"larger_half","capacity":14,"attributes":{"az":"az-one"}},{"name":"smaller_half","capacity":7,"attributes":{"az":"az-two"}},{"name":"larger_half","capacity":14,"attributes":{"az":"az-two"}}]', last_nonzero_raw_capacity = 42 WHERE id = 1 AND resource_id = 1 AND az = 'any' AND path = 'shared/things/any';
		UPDATE services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{"limes_unittest_capacity_larger_half":{"lk":null,"m":[{"v":7,"l":null}]},"limes_unittest_capacity_smaller_half":{"lk":null,"m":[{"v":3,"l":null}]}}', next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		scrapedAt.Unix(), scrapedAt.Add(15*time.Minute).Unix(),
	)

	// check that scraping correctly updates subcapacities on an existing record
	s.LiquidClients["shared"].CapacityReport.Modify(func(report *liquid.ServiceCapacityReport) {
		report.Resources["things"].PerAZ["any"].Capacity = 10
		report.Resources["things"].PerAZ["any"].Subcapacities = []liquid.Subcapacity{
			{Name: "smaller_half", Capacity: 1, Attributes: json.RawMessage(buf)},
			{Name: "larger_half", Capacity: 4, Attributes: json.RawMessage(buf)},
			{Name: "smaller_half", Capacity: 1, Attributes: json.RawMessage(buf2)},
			{Name: "larger_half", Capacity: 4, Attributes: json.RawMessage(buf2)},
		}
	})
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE az_resources SET raw_capacity = 10, subcapacities = '[{"name":"smaller_half","capacity":1,"attributes":{"az":"az-one"}},{"name":"larger_half","capacity":4,"attributes":{"az":"az-one"}},{"name":"smaller_half","capacity":1,"attributes":{"az":"az-two"}},{"name":"larger_half","capacity":4,"attributes":{"az":"az-two"}}]', last_nonzero_raw_capacity = 10 WHERE id = 1 AND resource_id = 1 AND az = 'any' AND path = 'shared/things/any';
		UPDATE services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		scrapedAt.Unix(), scrapedAt.Add(15*time.Minute).Unix(),
	)

	// check data metrics generated for these capacity data
	registry := prometheus.NewPedanticRegistry()
	pmc := &collector.CapacityCollectionMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(pmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": collector.ContentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/capacity_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	dmr := &collector.DataMetricsReporter{Cluster: s.Cluster, DB: s.DB, ReportZeroes: true}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": collector.ContentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/capacity_data_metrics_single.prom"),
	}.Check(t, dmr)
}

func Test_ScanCapacityAZAware(t *testing.T) {
	srvInfo := liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"things": {
				Unit:        liquid.UnitNone,
				Topology:    liquid.AZAwareTopology,
				HasCapacity: true,
				HasQuota:    true,
			},
		},
	}
	s := test.NewSetup(t,
		test.WithConfig(testScanCapacitySingleLiquidConfigJSON),
		test.WithMockLiquidClient("shared", srvInfo),
		// services must be created as a baseline
		test.WithLiquidConnections,
	)

	job := s.Collector.CapacityScrapeJob(s.Registry)

	// check baseline
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (1, 1, 'any', 0, 'shared/things/any');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (2, 1, 'az-one', 0, 'shared/things/az-one');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (3, 1, 'az-two', 0, 'shared/things/az-two');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (4, 1, 'unknown', 0, 'shared/things/unknown');
		INSERT INTO resources (id, service_id, name, liquid_version, topology, has_capacity, has_quota, path) VALUES (1, 1, 'things', 1, 'az-aware', TRUE, TRUE, 'shared/things');
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', %[1]d, 1);
	`, s.Clock.Now().Unix())

	s.LiquidClients["shared"].CapacityReport.Set(liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"things": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": {Capacity: 21, Usage: Some[uint64](4)},
					"az-two": {Capacity: 21, Usage: Some[uint64](4)},
				},
			},
		},
	})
	setClusterCapacitorsStale(t, s)
	s.Clock.StepBy(5 * time.Minute) // to force a capacitor consistency check to run
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE az_resources SET raw_capacity = 21, usage = 4, last_nonzero_raw_capacity = 21 WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'shared/things/az-one';
		UPDATE az_resources SET raw_capacity = 21, usage = 4, last_nonzero_raw_capacity = 21 WHERE id = 3 AND resource_id = 1 AND az = 'az-two' AND path = 'shared/things/az-two';
		UPDATE services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		scrapedAt.Unix(), scrapedAt.Add(15*time.Minute).Unix(),
	)

	// check that scraping correctly updates the capacities on an existing record
	s.LiquidClients["shared"].CapacityReport.Modify(func(report *liquid.ServiceCapacityReport) {
		report.Resources["things"].PerAZ["az-one"].Capacity = 15
		report.Resources["things"].PerAZ["az-one"].Usage = Some[uint64](3)
		report.Resources["things"].PerAZ["az-two"].Capacity = 15
		report.Resources["things"].PerAZ["az-two"].Usage = Some[uint64](3)
	})
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE az_resources SET raw_capacity = 15, usage = 3, last_nonzero_raw_capacity = 15 WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'shared/things/az-one';
		UPDATE az_resources SET raw_capacity = 15, usage = 3, last_nonzero_raw_capacity = 15 WHERE id = 3 AND resource_id = 1 AND az = 'az-two' AND path = 'shared/things/az-two';
		UPDATE services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		scrapedAt.Unix(), scrapedAt.Add(15*time.Minute).Unix(),
	)

	dmr := &collector.DataMetricsReporter{Cluster: s.Cluster, DB: s.DB, ReportZeroes: true}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": collector.ContentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/capacity_data_metrics_azaware.prom"),
	}.Check(t, dmr)

	// check that removing a LiquidConnection does nothing special (will be auto-removed later)
	delete(s.Cluster.LiquidConnections, "unittest")
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))
	scrapedAt = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`, scrapedAt.Unix(), scrapedAt.Add(15*time.Minute).Unix())
}

func TestScanCapacityReportsZeroValues(t *testing.T) {
	// setup both "capacity" and "things" with HasCapacity = true
	srvInfo := test.DefaultLiquidServiceInfo()
	res := srvInfo.Resources["things"]
	res.HasCapacity = true
	srvInfo.Resources["things"] = res

	s := test.NewSetup(t,
		test.WithConfig(testScanCapacitySingleLiquidConfigJSON),
		test.WithMockLiquidClient("shared", srvInfo),
		// services must be created as a baseline
		test.WithLiquidConnections,
	)

	job := s.Collector.CapacityScrapeJob(s.Registry)

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	// when the capacity report shows zero capacity and usage...
	s.LiquidClients["shared"].CapacityReport.Set(liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"capacity": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": {Capacity: 0, Usage: Some[uint64](0)},
					"az-two": {Capacity: 0, Usage: Some[uint64](0)},
				},
			},
			"things": {
				PerAZ: liquid.InAnyAZ(liquid.AZResourceCapacityReport{Capacity: 0, Usage: Some[uint64](0)}),
			},
		},
	})

	// ...scrape will record those values faithfully and not set "last_nonzero_raw_capacity"
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE az_resources SET usage = 0 WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'shared/capacity/az-one';
		UPDATE az_resources SET usage = 0 WHERE id = 3 AND resource_id = 1 AND az = 'az-two' AND path = 'shared/capacity/az-two';
		UPDATE az_resources SET usage = 0 WHERE id = 5 AND resource_id = 2 AND az = 'any' AND path = 'shared/things/any';
		UPDATE services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)

	// when the capacity report shows non-zero capacity and usage...
	s.LiquidClients["shared"].CapacityReport.Modify(func(report *liquid.ServiceCapacityReport) {
		report.Resources["capacity"].PerAZ["az-one"] = &liquid.AZResourceCapacityReport{Capacity: 10, Usage: Some[uint64](5)}
		report.Resources["capacity"].PerAZ["az-two"] = &liquid.AZResourceCapacityReport{Capacity: 10, Usage: Some[uint64](5)}
		report.Resources["things"].PerAZ = liquid.InAnyAZ(liquid.AZResourceCapacityReport{Capacity: 20, Usage: Some[uint64](10)})
	})

	// ...scrape will record those values and set "last_nonzero_raw_capacity" because a non-zero value was observed
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE az_resources SET raw_capacity = 10, usage = 5, last_nonzero_raw_capacity = 10 WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'shared/capacity/az-one';
		UPDATE az_resources SET raw_capacity = 10, usage = 5, last_nonzero_raw_capacity = 10 WHERE id = 3 AND resource_id = 1 AND az = 'az-two' AND path = 'shared/capacity/az-two';
		UPDATE az_resources SET raw_capacity = 20, usage = 10, last_nonzero_raw_capacity = 20 WHERE id = 5 AND resource_id = 2 AND az = 'any' AND path = 'shared/things/any';
		UPDATE services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)

	// when the capacity report once again shows zero capacity and usage afterwards...
	s.LiquidClients["shared"].CapacityReport.Modify(func(report *liquid.ServiceCapacityReport) {
		report.Resources["capacity"].PerAZ["az-one"] = &liquid.AZResourceCapacityReport{Capacity: 0, Usage: Some[uint64](0)}
		report.Resources["capacity"].PerAZ["az-two"] = &liquid.AZResourceCapacityReport{Capacity: 0, Usage: Some[uint64](0)}
		report.Resources["things"].PerAZ = liquid.InAnyAZ(liquid.AZResourceCapacityReport{Capacity: 0, Usage: Some[uint64](0)})
	})

	// ...scrape will record those values and, once again, leave "last_nonzero_raw_capacity" untouched
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE az_resources SET raw_capacity = 0, usage = 0 WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'shared/capacity/az-one';
		UPDATE az_resources SET raw_capacity = 0, usage = 0 WHERE id = 3 AND resource_id = 1 AND az = 'az-two' AND path = 'shared/capacity/az-two';
		UPDATE az_resources SET raw_capacity = 0, usage = 0 WHERE id = 5 AND resource_id = 2 AND az = 'any' AND path = 'shared/things/any';
		UPDATE services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)
}

func Test_ScanCapacityUnknownAZVanishes(t *testing.T) {
	// setup just "capacity"
	srvInfo := test.DefaultLiquidServiceInfo()

	s := test.NewSetup(t,
		test.WithConfig(testScanCapacitySingleLiquidConfigYAML),
		test.WithMockLiquidClient("shared", srvInfo),
		// services must be created as a baseline
		test.WithLiquidConnections,
	)

	job := s.Collector.CapacityScrapeJob(s.Registry)

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	// we setup a capacity report with an AZ "unknown" which will later vanish
	s.LiquidClients["shared"].CapacityReport.Set(liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"capacity": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one":  {Capacity: 4, Usage: Some[uint64](0)},
					"az-two":  {Capacity: 5, Usage: Some[uint64](0)},
					"unknown": {Capacity: 6, Usage: Some[uint64](0)},
				},
			},
		},
	})

	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE az_resources SET raw_capacity = 4, usage = 0, last_nonzero_raw_capacity = 4 WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'shared/capacity/az-one';
		UPDATE az_resources SET raw_capacity = 5, usage = 0, last_nonzero_raw_capacity = 5 WHERE id = 3 AND resource_id = 1 AND az = 'az-two' AND path = 'shared/capacity/az-two';
		UPDATE az_resources SET raw_capacity = 6, usage = 0, last_nonzero_raw_capacity = 6 WHERE id = 4 AND resource_id = 1 AND az = 'unknown' AND path = 'shared/capacity/unknown';
		UPDATE services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)

	// the unknown availability zone can vanish, when e.g. a bareMetal capacity receives the proper AZ information
	// this is simulated by the next step
	s.LiquidClients["shared"].CapacityReport.Set(liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"capacity": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": {Capacity: 10, Usage: Some[uint64](0)},
					"az-two": {Capacity: 5, Usage: Some[uint64](0)},
				},
			},
		},
	})

	// we expect capacity=0 and usage=NULL
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE az_resources SET raw_capacity = 10, last_nonzero_raw_capacity = 10 WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'shared/capacity/az-one';
		UPDATE az_resources SET raw_capacity = 0, usage = NULL WHERE id = 4 AND resource_id = 1 AND az = 'unknown' AND path = 'shared/capacity/unknown';
		UPDATE services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)
}

func setClusterCapacitorsStale(t *testing.T, s test.Setup) {
	t.Helper()
	s.MustDBExec(`UPDATE services SET next_scrape_at = $1`, s.Clock.Now())
}

func Test_ScanCapacityButNoResources(t *testing.T) {
	// test ScanCapacity on a LIQUID with no resources
	s := test.NewSetup(t,
		test.WithConfig(testScanCapacitySingleLiquidConfigJSON),
		test.WithMockLiquidClient("shared", liquid.ServiceInfo{Version: 1, Resources: nil}),
		// services must be created as a baseline
		test.WithLiquidConnections,
	)

	job := s.Collector.CapacityScrapeJob(s.Registry)

	// check baseline
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', %[1]d, 1);
	`,
		s.Clock.Now().Unix(),
	)

	// since ServiceInfo does not declare resources, the capacity report is also empty
	s.LiquidClients["shared"].CapacityReport.Set(liquid.ServiceCapacityReport{
		InfoVersion: 1,
	})

	// check that the capacitor runs, and does not touch resources and az_resources
	// since it does not report for anything (this used to fail because we generated a syntactically
	// invalid WHERE clause when matching zero resources)
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))

	tr.DBChanges().AssertEqualf(`
		UPDATE services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)

	// rerun also works
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))

	tr.DBChanges().AssertEqualf(`
		UPDATE services SET scraped_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)

	// now we bump the version, so that the services and resources are reconciled
	s.LiquidClients["shared"].IncrementServiceInfoVersion()
	s.LiquidClients["shared"].IncrementCapacityReportInfoVersion()
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))

	tr.DBChanges().AssertEqualf(`
		DELETE FROM services WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
		INSERT INTO services (id, type, scraped_at, scrape_duration_secs, serialized_metrics, next_scrape_at, liquid_version) VALUES (1, 'shared', %[1]d, 5, '{}', %[2]d, 2);
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)
}

func Test_ScanManualCapacity(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo()
	testScanCapacityManualConfigJSON := testScanCapacitySingleLiquidConfigJSON[:len(testScanCapacitySingleLiquidConfigJSON)-1] + `,
		"liquids": {
			"shared": {
				"area": "shared",
				"fixed_capacity_values": {
					"things": 1000000
				}
			}
		}
	}`
	s := test.NewSetup(t,
		test.WithConfig(testScanCapacityManualConfigJSON),
		test.WithMockLiquidClient("shared", srvInfo),
		test.WithLiquidConnections,
	)

	job := s.Collector.CapacityScrapeJob(s.Registry)

	// check baseline
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (1, 1, 'any', 0, 'shared/capacity/any');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (2, 1, 'az-one', 0, 'shared/capacity/az-one');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (3, 1, 'az-two', 0, 'shared/capacity/az-two');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (4, 1, 'unknown', 0, 'shared/capacity/unknown');
		INSERT INTO az_resources (id, resource_id, az, raw_capacity, path) VALUES (5, 2, 'any', 0, 'shared/things/any');
		INSERT INTO resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota, path) VALUES (1, 1, 'capacity', 1, 'B', 'az-aware', TRUE, TRUE, TRUE, 'shared/capacity');
		INSERT INTO resources (id, service_id, name, liquid_version, topology, has_quota, path) VALUES (2, 1, 'things', 1, 'flat', TRUE, 'shared/things');
		INSERT INTO services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', %[1]d, 1);
	`,
		s.Clock.Now().Unix(),
	)

	// since "capacity" has HasCapacity = true, it must show capacity here;
	// but "things" has HasCapacity = false, so it must not
	s.LiquidClients["shared"].CapacityReport.Set(liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"capacity": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": {Capacity: 42, Usage: Some[uint64](8)},
					"az-two": {Capacity: 42, Usage: Some[uint64](8)},
				},
			},
		},
	})

	// capacity scrape writes both the LIQUID-based and the manual capacity value
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))

	tr.DBChanges().AssertEqualf(`
		UPDATE az_resources SET raw_capacity = 42, usage = 8, last_nonzero_raw_capacity = 42 WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'shared/capacity/az-one';
		UPDATE az_resources SET raw_capacity = 42, usage = 8, last_nonzero_raw_capacity = 42 WHERE id = 3 AND resource_id = 1 AND az = 'az-two' AND path = 'shared/capacity/az-two';
		UPDATE az_resources SET raw_capacity = 1000000, last_nonzero_raw_capacity = 1000000 WHERE id = 5 AND resource_id = 2 AND az = 'any' AND path = 'shared/things/any';
		UPDATE services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)
}

func commonScanCapacityWithCommitmentsSetup() (srvInfo liquid.ServiceInfo, firstCapacityReport, secondCapacityReport liquid.ServiceCapacityReport) {
	srvInfo = liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"capacity": {
				Unit:        liquid.UnitNone,
				Topology:    liquid.AZAwareTopology,
				HasCapacity: true,
				HasQuota:    true,
			},
			"things": {
				Unit:        liquid.UnitNone,
				Topology:    liquid.FlatTopology,
				HasCapacity: true,
				HasQuota:    true,
			},
		},
	}

	azReportForFirst := liquid.AZResourceCapacityReport{Capacity: 42, Usage: Some[uint64](8)}
	firstCapacityReport = liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"capacity": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": &azReportForFirst,
					"az-two": &azReportForFirst,
				},
			},
			"things": {PerAZ: liquid.InAnyAZ(azReportForFirst)},
		},
	}

	azReportForSecond := liquid.AZResourceCapacityReport{Capacity: 23, Usage: Some[uint64](4)}
	secondCapacityReport = firstCapacityReport.Clone()
	secondCapacityReport.Resources["capacity"].PerAZ["az-one"] = &azReportForSecond
	secondCapacityReport.Resources["capacity"].PerAZ["az-two"] = &azReportForSecond
	secondCapacityReport.Resources["things"].PerAZ[liquid.AvailabilityZoneAny] = &azReportForSecond

	return
}

func Test_ScanCapacityWithCommitments(t *testing.T) {
	srvInfo, firstCapacityReport, secondCapacityReport := commonScanCapacityWithCommitmentsSetup()
	s := test.NewSetup(t,
		test.WithConfig(`{
			"availability_zones": ["az-one", "az-two"],
			"discovery": {
				"method": "static",
				"static_config": {
					"domains": [{"name": "germany", "id": "uuid-for-germany"}],
					"projects": {
						"uuid-for-germany": [
							{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"},
							{"name": "dresden", "id": "uuid-for-dresden", "parent_id": "uuid-for-berlin"}
						]
					}
				}
			},
			"liquids": {
				"first": {
					"area": "first",
					"commitment_behavior_per_resource": [
						{"key": "capacity", "value": {"durations_per_domain": [{"key": ".*", "value": ["1 hour", "10 days"]}]}}
					]
				},
				"second": {
					"area": "second",
					"commitment_behavior_per_resource": [
						{"key": "capacity", "value": {"durations_per_domain": [{"key": ".*", "value": ["1 hour", "10 days"]}]}}
					]
				}
			},
			"resource_behavior": [
				// test that overcommit factor is considered when confirming commitments
				{"resource": "first/capacity", "overcommit_factor": 10.0}
			],
			"quota_distribution_configs": [
				// test automatic project quota calculation with non-default settings on */capacity resources
				{"resource": ".*/capacity", "model": "autogrow", "autogrow": {"growth_multiplier": 1.0, "project_base_quota": 10, "usage_data_retention_period": "1m"}}
			]
		}`),
		test.WithMockLiquidClient("first", srvInfo),
		test.WithMockLiquidClient("second", srvInfo),
		test.WithLiquidConnections,
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
	)
	s.LiquidClients["first"].CapacityReport.Set(firstCapacityReport)
	s.LiquidClients["second"].CapacityReport.Set(secondCapacityReport)
	job := s.Collector.CapacityScrapeJob(s.Registry)

	// fill `services` and `az_resources` as though a previous capacity scrape has already taken place,
	// so that tr.DBChanges() below concentrates on the relevant parts
	s.MustDBExec(`UPDATE services SET scraped_at = $1, scrape_duration_secs = 5`, s.Clock.Now())
	query := `UPDATE az_resources SET raw_capacity = $1, last_nonzero_raw_capacity = $1, usage = $2 WHERE path = $3`
	s.MustDBExec(query, 42, 8, "first/capacity/az-one")
	s.MustDBExec(query, 42, 8, "first/capacity/az-two")
	s.MustDBExec(query, 42, 8, "first/things/any")
	s.MustDBExec(query, 23, 4, "second/capacity/az-one")
	s.MustDBExec(query, 23, 4, "second/capacity/az-two")
	s.MustDBExec(query, 23, 4, "second/things/any")

	// fill `project_az_resources` with some usage data
	// (we want to see how commitment confirmation reacts to existing usage)
	berlin := s.GetProjectID("berlin")
	dresden := s.GetProjectID("dresden")
	firstCapacityAZOne := s.GetAZResourceID("first", "capacity", "az-one")
	firstCapacityAZTwo := s.GetAZResourceID("first", "capacity", "az-two")
	secondCapacityAZOne := s.GetAZResourceID("second", "capacity", "az-one")
	secondCapacityAZTwo := s.GetAZResourceID("second", "capacity", "az-two")

	query = `UPDATE project_az_resources SET usage = $1 WHERE az_resource_id = $2`
	s.MustDBExec(query, 1, firstCapacityAZOne)
	s.MustDBExec(query, 1, firstCapacityAZTwo)
	s.MustDBExec(query, 1, secondCapacityAZOne)
	s.MustDBExec(query, 1, secondCapacityAZTwo)
	query = `UPDATE project_az_resources SET usage = $1 WHERE project_id = $2 AND az_resource_id = $3`
	s.MustDBExec(query, 250, berlin, firstCapacityAZTwo)

	// fill `project_commitments` with several commitments that each have their confirm_by staggered in amounts of days;
	// below, we will step through those days
	committedForTenDays := must.Return(limesresources.ParseCommitmentDuration("10 days"))
	committedForOneHour := must.Return(limesresources.ParseCommitmentDuration("1 hour"))
	const oneDay = 24 * time.Hour

	add := func(c db.ProjectCommitment) {
		t.Helper()
		c.CreatorUUID = "dummy"
		c.CreatorName = "dummy"
		c.CreationContextJSON = json.RawMessage(`{}`)
		c.ExpiresAt = c.Duration.AddTo(c.ConfirmBy.UnwrapOr(c.CreatedAt))
		c.Status = liquid.CommitmentStatusPlanned
		s.MustDBInsert(&c)
	}

	// day 1: just a boring commitment that easily fits in the available capacity
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000001",
		ProjectID:    berlin,
		AZResourceID: firstCapacityAZOne,
		Amount:       10,
		CreatedAt:    s.Clock.Now(),
		ConfirmBy:    Some(s.Clock.Now().Add(1 * oneDay)),
		Duration:     committedForTenDays,
	})

	// day 2: very large commitments that exceed the raw capacity; only the one on "first" works because that service has a large overcommit factor
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000002",
		ProjectID:    berlin,
		AZResourceID: firstCapacityAZOne,
		Amount:       100,
		CreatedAt:    s.Clock.Now(),
		ConfirmBy:    Some(s.Clock.Now().Add(2 * oneDay)),
		Duration:     committedForTenDays,
	})
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000003",
		ProjectID:    berlin,
		AZResourceID: secondCapacityAZOne,
		Amount:       100,
		CreatedAt:    s.Clock.Now(),
		ConfirmBy:    Some(s.Clock.Now().Add(2 * oneDay)),
		Duration:     committedForTenDays,
	})

	// day 3: a bunch of small commitments with different timestamps, to test confirmation order in two ways:
	//        1. ID=3 does not block these commitments even though it is on the same resource and AZ
	//        2. we cannot confirm all of these; which ones are confirmed demonstrates the order of consideration
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000004",
		ProjectID:    dresden,
		AZResourceID: secondCapacityAZOne,
		Amount:       10,
		CreatedAt:    s.Clock.Now().Add(1 * time.Second),
		ConfirmBy:    Some(s.Clock.Now().Add(3*oneDay + 3*time.Second)),
		Duration:     committedForTenDays,
	})
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000005",
		ProjectID:    dresden,
		AZResourceID: secondCapacityAZOne,
		Amount:       10,
		CreatedAt:    s.Clock.Now().Add(2 * time.Second),
		ConfirmBy:    Some(s.Clock.Now().Add(3*oneDay + 2*time.Second)),
		Duration:     committedForTenDays,
	})
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000006",
		ProjectID:    dresden,
		AZResourceID: secondCapacityAZOne,
		Amount:       10,
		CreatedAt:    s.Clock.Now().Add(3 * time.Second),
		ConfirmBy:    Some(s.Clock.Now().Add(3*oneDay + 1*time.Second)),
		Duration:     committedForTenDays,
	})

	// day 4: test confirmation that is (or is not) blocked by existing usage in other projects
	// (on a capacity of 420, there is already 250 usage in berlin, so only berlin can confirm a commitment for amount = 300, even though dresden asked first)
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000007",
		ProjectID:    dresden,
		AZResourceID: firstCapacityAZTwo,
		Amount:       300,
		CreatedAt:    s.Clock.Now().Add(1 * time.Second),
		ConfirmBy:    Some(s.Clock.Now().Add(4 * oneDay)),
		Duration:     committedForTenDays,
	})
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000008",
		ProjectID:    berlin,
		AZResourceID: firstCapacityAZTwo,
		Amount:       300,
		CreatedAt:    s.Clock.Now().Add(2 * time.Second),
		ConfirmBy:    Some(s.Clock.Now().Add(4 * oneDay)),
		Duration:     committedForTenDays,
	})

	// day 5: test commitments that cannot be confirmed until the previous commitment expires
	// (ID=9 is confirmed, and then ID=10 cannot be confirmed until ID=9 expires because ID=9 blocks absolutely all available capacity in that resource and AZ)
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000009",
		ProjectID:    berlin,
		AZResourceID: secondCapacityAZTwo,
		Amount:       22,
		CreatedAt:    s.Clock.Now().Add(1 * time.Second),
		ConfirmBy:    Some(s.Clock.Now().Add(5 * oneDay)),
		Duration:     committedForOneHour,
	})
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000010",
		ProjectID:    dresden,
		AZResourceID: secondCapacityAZTwo,
		Amount:       2,
		CreatedAt:    s.Clock.Now().Add(2 * time.Second),
		ConfirmBy:    Some(s.Clock.Now().Add(5 * oneDay)),
		Duration:     committedForTenDays,
	})

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	// in each of the test steps below, the timestamp updates on services will always be the same
	timestampUpdates := func(initMetrics bool) string {
		scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
		scrapedAt2 := s.Clock.Now()
		if !initMetrics {
			return strings.TrimSpace(fmt.Sprintf(`
				UPDATE services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'first' AND liquid_version = 1;
				UPDATE services SET scraped_at = %d, next_scrape_at = %d WHERE id = 2 AND type = 'second' AND liquid_version = 1;
			`,
				scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
				scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
			))
		}
		return strings.TrimSpace(fmt.Sprintf(`
				UPDATE services SET scraped_at = %d, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 1 AND type = 'first' AND liquid_version = 1;
				UPDATE services SET scraped_at = %d, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 2 AND type = 'second' AND liquid_version = 1;
			`,
			scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
			scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
		))
	}

	// first run should not confirm any commitments because they all start with `confirm_by > now`;
	// however, we run ApplyComputedProjectQuota() for the first time, so quota will be filled based on usage
	//   - on "capacity", usage[az] = 1 and baseQuota = 10 and growthMultiplier = 1 leads to quota[az] = 1 and quota[any] = 8
	//   - on "things", usage = 0 and baseQuota = 0 and growthMultiplier = 1 leads to quota = 0
	//
	// Note that the "things" resources are not explicitly set up in the
	// quota_distribution_configs test section. The automatic behavior amounts to
	// pretty much just setting `quota = usage`, i.e. `quota = 0` in this case.
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	desyncedAt1 := s.Clock.Now().Add(-5 * time.Second)
	desyncedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET quota = 0 WHERE id = 1 AND project_id = 1 AND az_resource_id = 1;
		UPDATE project_az_resources SET quota = 0 WHERE id = 10 AND project_id = 1 AND az_resource_id = 10;
		UPDATE project_az_resources SET quota = 8 WHERE id = 11 AND project_id = 2 AND az_resource_id = 1;
		UPDATE project_az_resources SET quota = 1 WHERE id = 12 AND project_id = 2 AND az_resource_id = 2;
		UPDATE project_az_resources SET quota = 1 WHERE id = 13 AND project_id = 2 AND az_resource_id = 3;
		UPDATE project_az_resources SET quota = 0 WHERE id = 14 AND project_id = 2 AND az_resource_id = 4;
		UPDATE project_az_resources SET quota = 0 WHERE id = 15 AND project_id = 2 AND az_resource_id = 5;
		UPDATE project_az_resources SET quota = 8 WHERE id = 16 AND project_id = 2 AND az_resource_id = 6;
		UPDATE project_az_resources SET quota = 1 WHERE id = 17 AND project_id = 2 AND az_resource_id = 7;
		UPDATE project_az_resources SET quota = 1 WHERE id = 18 AND project_id = 2 AND az_resource_id = 8;
		UPDATE project_az_resources SET quota = 0 WHERE id = 19 AND project_id = 2 AND az_resource_id = 9;
		UPDATE project_az_resources SET quota = 1 WHERE id = 2 AND project_id = 1 AND az_resource_id = 2;
		UPDATE project_az_resources SET quota = 0 WHERE id = 20 AND project_id = 2 AND az_resource_id = 10;
		UPDATE project_az_resources SET quota = 250 WHERE id = 3 AND project_id = 1 AND az_resource_id = 3;
		UPDATE project_az_resources SET quota = 0 WHERE id = 4 AND project_id = 1 AND az_resource_id = 4;
		UPDATE project_az_resources SET quota = 0 WHERE id = 5 AND project_id = 1 AND az_resource_id = 5;
		UPDATE project_az_resources SET quota = 8 WHERE id = 6 AND project_id = 1 AND az_resource_id = 6;
		UPDATE project_az_resources SET quota = 1 WHERE id = 7 AND project_id = 1 AND az_resource_id = 7;
		UPDATE project_az_resources SET quota = 1 WHERE id = 8 AND project_id = 1 AND az_resource_id = 8;
		UPDATE project_az_resources SET quota = 0 WHERE id = 9 AND project_id = 1 AND az_resource_id = 9;
		UPDATE project_resources SET quota = 251 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		UPDATE project_resources SET quota = 10 WHERE id = 3 AND project_id = 1 AND resource_id = 3;
		UPDATE project_resources SET quota = 10 WHERE id = 5 AND project_id = 2 AND resource_id = 1;
		UPDATE project_resources SET quota = 10 WHERE id = 7 AND project_id = 2 AND resource_id = 3;
		UPDATE project_services SET quota_desynced_at = %[1]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET quota_desynced_at = %[2]d WHERE id = 2 AND project_id = 1 AND service_id = 2;
		UPDATE project_services SET quota_desynced_at = %[1]d WHERE id = 3 AND project_id = 2 AND service_id = 1;
		UPDATE project_services SET quota_desynced_at = %[2]d WHERE id = 4 AND project_id = 2 AND service_id = 2;
		%s
	`, desyncedAt1.Unix(), desyncedAt2.Unix(), timestampUpdates(true))

	// day 1: test that confirmation works at all
	//
	// The confirmed commitment is for first/capacity in berlin az-one (amount = 10).
	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET quota = 10 WHERE id = 2 AND project_id = 1 AND az_resource_id = 2;
		UPDATE project_commitments SET status = 'confirmed', confirmed_at = %d WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;
		UPDATE project_resources SET quota = 260 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		%s
	`, scrapedAt1.Unix(), timestampUpdates(false))

	// day 2: test that confirmation considers the resource's capacity overcommit factor
	//
	// The confirmed commitment (ID=2) is for first/capacity in berlin az-one (amount = 100).
	// A similar commitment (ID=3) for second/capacity is not confirmed because of missing capacity.
	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET quota = 110 WHERE id = 2 AND project_id = 1 AND az_resource_id = 2;
		UPDATE project_commitments SET status = 'confirmed', confirmed_at = %d WHERE id = 2 AND uuid = '00000000-0000-0000-0000-000000000002' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'pending' WHERE id = 3 AND uuid = '00000000-0000-0000-0000-000000000003' AND transfer_token = NULL;
		UPDATE project_resources SET quota = 360 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		%s
	`, scrapedAt1.Unix(), timestampUpdates(false))

	// day 3: test confirmation order with several commitments, on second/capacity in az-one
	//
	// The previously not confirmed commitment (ID=3) does not block confirmation of smaller confirmable commitments.
	// Only two of three commitments are confirmed. The third commitment exhausts the available capacity.
	// The two commitments that are confirmed (ID=4 and ID=5) have a lower created_at than the unconfirmed one (ID=6).
	// This is because we want to ensure the "first come, first serve" rule.
	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET quota = 0 WHERE id = 16 AND project_id = 2 AND az_resource_id = 6;
		UPDATE project_az_resources SET quota = 20 WHERE id = 17 AND project_id = 2 AND az_resource_id = 7;
		UPDATE project_commitments SET status = 'confirmed', confirmed_at = %d WHERE id = 4 AND uuid = '00000000-0000-0000-0000-000000000004' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'confirmed', confirmed_at = %d WHERE id = 5 AND uuid = '00000000-0000-0000-0000-000000000005' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'pending' WHERE id = 6 AND uuid = '00000000-0000-0000-0000-000000000006' AND transfer_token = NULL;
		UPDATE project_resources SET quota = 21 WHERE id = 7 AND project_id = 2 AND resource_id = 3;
		%s
	`, scrapedAt2.Unix(), scrapedAt2.Unix(), timestampUpdates(false))

	// day 4: test how confirmation interacts with existing usage, on first/capacity in az-two
	//
	// Both dresden (ID=7) and berlin (ID=8) are asking for an amount of 300 to be committed, on a total capacity of 420.
	// But because berlin has an existing usage of 250, dresden is denied (even though it asked first) and berlin is confirmed.
	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET quota = 300 WHERE id = 3 AND project_id = 1 AND az_resource_id = 3;
		UPDATE project_commitments SET status = 'pending' WHERE id = 7 AND uuid = '00000000-0000-0000-0000-000000000007' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'confirmed', confirmed_at = %d WHERE id = 8 AND uuid = '00000000-0000-0000-0000-000000000008' AND transfer_token = NULL;
		UPDATE project_resources SET quota = 410 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		%s
	`, scrapedAt1.Unix(), timestampUpdates(false))

	// day 5: test commitments that cannot be confirmed until the previous commitment expires, on second/capacity in az-one
	//
	// The first commitment (ID=9 in berlin) is confirmed because no other commitments are confirmed yet.
	// The second commitment (ID=10 in dresden) is much smaller (only 1 larger than project usage),
	// but cannot be confirmed because ID=9 grabbed any and all unused capacity.
	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET quota = 0 WHERE id = 6 AND project_id = 1 AND az_resource_id = 6;
		UPDATE project_az_resources SET quota = 22 WHERE id = 8 AND project_id = 1 AND az_resource_id = 8;
		UPDATE project_commitments SET status = 'pending' WHERE id = 10 AND uuid = '00000000-0000-0000-0000-000000000010' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'confirmed', confirmed_at = %d WHERE id = 9 AND uuid = '00000000-0000-0000-0000-000000000009' AND transfer_token = NULL;
		UPDATE project_resources SET quota = 23 WHERE id = 3 AND project_id = 1 AND resource_id = 3;
		%s
	`, scrapedAt2.Unix(), timestampUpdates(false))

	// ...Once ID=9 expires an hour later, ID=10 can be confirmed.
	s.Clock.StepBy(1 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET quota = 2 WHERE id = 18 AND project_id = 2 AND az_resource_id = 8;
		UPDATE project_az_resources SET quota = 8 WHERE id = 6 AND project_id = 1 AND az_resource_id = 6;
		UPDATE project_az_resources SET quota = 1 WHERE id = 8 AND project_id = 1 AND az_resource_id = 8;
		UPDATE project_commitments SET status = 'confirmed', confirmed_at = %d WHERE id = 10 AND uuid = '00000000-0000-0000-0000-000000000010' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'expired' WHERE id = 9 AND uuid = '00000000-0000-0000-0000-000000000009' AND transfer_token = NULL;
		UPDATE project_resources SET quota = 10 WHERE id = 3 AND project_id = 1 AND resource_id = 3;
		UPDATE project_resources SET quota = 22 WHERE id = 7 AND project_id = 2 AND resource_id = 3;
		%s
	`, scrapedAt2.Unix(), timestampUpdates(false))

	// test GetGlobalResourceDemand (this is not used by any of our mock liquids,
	// but we can just call it directly to see that it works)
	bc := datamodel.NewCapacityScrapeBackchannel(s.Cluster, s.DB)
	expectedDemandsByService := map[db.ServiceType]map[liquid.ResourceName]map[liquid.AvailabilityZone]liquid.ResourceDemandInAZ{
		"first": {
			"capacity": {
				"az-one": {Usage: 2, UnusedCommitments: 109, PendingCommitments: 0},
				"az-two": {Usage: 251, UnusedCommitments: 50, PendingCommitments: 300},
			},
			"things": {
				liquid.AvailabilityZoneAny: {Usage: 0, UnusedCommitments: 0, PendingCommitments: 0},
			},
		},
		"second": {
			"capacity": {
				"az-one": {Usage: 2, UnusedCommitments: 19, PendingCommitments: 110},
				"az-two": {Usage: 2, UnusedCommitments: 1, PendingCommitments: 0},
			},
			"things": {
				liquid.AvailabilityZoneAny: {Usage: 0, UnusedCommitments: 0, PendingCommitments: 0},
			},
		},
	}
	for serviceType, expectedDemandsByResource := range expectedDemandsByService {
		for resourceName, expectedDemands := range expectedDemandsByResource {
			actualDemands, err := bc.GetResourceDemand(serviceType, resourceName)
			mustT(t, err)
			desc := fmt.Sprintf("GetGlobalResourceDemand for %s/%s", serviceType, resourceName)
			assert.DeepEqual(t, desc, actualDemands.PerAZ, expectedDemands)
		}
	}

	// now we let almost all commitments expire, so that we can test the az_resources_project_commitments_trigger
	// all are expired, 10 remains active
	s.Clock.StepBy(9 * 24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET quota = 7 WHERE id = 16 AND project_id = 2 AND az_resource_id = 6;
		UPDATE project_az_resources SET quota = 1 WHERE id = 17 AND project_id = 2 AND az_resource_id = 7;
		UPDATE project_az_resources SET quota = 1 WHERE id = 2 AND project_id = 1 AND az_resource_id = 2;
		UPDATE project_az_resources SET quota = 250 WHERE id = 3 AND project_id = 1 AND az_resource_id = 3;
		UPDATE project_commitments SET status = 'expired' WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'expired' WHERE id = 2 AND uuid = '00000000-0000-0000-0000-000000000002' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'expired' WHERE id = 3 AND uuid = '00000000-0000-0000-0000-000000000003' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'expired' WHERE id = 4 AND uuid = '00000000-0000-0000-0000-000000000004' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'expired' WHERE id = 5 AND uuid = '00000000-0000-0000-0000-000000000005' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'expired' WHERE id = 6 AND uuid = '00000000-0000-0000-0000-000000000006' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'expired' WHERE id = 7 AND uuid = '00000000-0000-0000-0000-000000000007' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'expired' WHERE id = 8 AND uuid = '00000000-0000-0000-0000-000000000008' AND transfer_token = NULL;
		UPDATE project_resources SET quota = 251 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		UPDATE project_resources SET quota = 10 WHERE id = 7 AND project_id = 2 AND resource_id = 3;
		%s
	`, timestampUpdates(false))

	// we remove first/capacity, which does not have any active commitments. The trigger removes the expired commitments.
	s.LiquidClients["first"].CapacityReport.Modify(func(report *liquid.ServiceCapacityReport) {
		delete(report.Resources, "capacity")
		report.InfoVersion = 2
	})
	s.LiquidClients["first"].ServiceInfo.Modify(func(info *liquid.ServiceInfo) {
		delete(info.Resources, "capacity")
		info.Version = 2
	})

	s.Clock.StepBy(1 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))
	tr.DBChanges().AssertEqual(`
		DELETE FROM az_resources WHERE id = 1 AND resource_id = 1 AND az = 'any' AND path = 'first/capacity/any';
		DELETE FROM az_resources WHERE id = 2 AND resource_id = 1 AND az = 'az-one' AND path = 'first/capacity/az-one';
		DELETE FROM az_resources WHERE id = 3 AND resource_id = 1 AND az = 'az-two' AND path = 'first/capacity/az-two';
		DELETE FROM az_resources WHERE id = 4 AND resource_id = 1 AND az = 'unknown' AND path = 'first/capacity/unknown';
		DELETE FROM project_az_resources WHERE id = 1 AND project_id = 1 AND az_resource_id = 1;
		DELETE FROM project_az_resources WHERE id = 11 AND project_id = 2 AND az_resource_id = 1;
		DELETE FROM project_az_resources WHERE id = 12 AND project_id = 2 AND az_resource_id = 2;
		DELETE FROM project_az_resources WHERE id = 13 AND project_id = 2 AND az_resource_id = 3;
		DELETE FROM project_az_resources WHERE id = 14 AND project_id = 2 AND az_resource_id = 4;
		DELETE FROM project_az_resources WHERE id = 2 AND project_id = 1 AND az_resource_id = 2;
		DELETE FROM project_az_resources WHERE id = 3 AND project_id = 1 AND az_resource_id = 3;
		DELETE FROM project_az_resources WHERE id = 4 AND project_id = 1 AND az_resource_id = 4;
		DELETE FROM project_commitments WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;
		DELETE FROM project_commitments WHERE id = 2 AND uuid = '00000000-0000-0000-0000-000000000002' AND transfer_token = NULL;
		DELETE FROM project_commitments WHERE id = 7 AND uuid = '00000000-0000-0000-0000-000000000007' AND transfer_token = NULL;
		DELETE FROM project_commitments WHERE id = 8 AND uuid = '00000000-0000-0000-0000-000000000008' AND transfer_token = NULL;
		DELETE FROM project_resources WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		DELETE FROM project_resources WHERE id = 5 AND project_id = 2 AND resource_id = 1;
		DELETE FROM resources WHERE id = 1 AND service_id = 1 AND name = 'capacity' AND path = 'first/capacity';
		UPDATE resources SET liquid_version = 2 WHERE id = 2 AND service_id = 1 AND name = 'things' AND path = 'first/things';
		DELETE FROM services WHERE id = 1 AND type = 'first' AND liquid_version = 1;
		INSERT INTO services (id, type, scraped_at, scrape_duration_secs, serialized_metrics, next_scrape_at, liquid_version) VALUES (1, 'first', 1216885, 5, '{}', 1217785, 2);
		UPDATE services SET scraped_at = 1216890, next_scrape_at = 1217790 WHERE id = 2 AND type = 'second' AND liquid_version = 1;
	`)

	// now we try to remove second/capacity, which has an active commitment. Hence, it will fail on SaveServiceInfoToDB
	s.LiquidClients["second"].CapacityReport.Modify(func(report *liquid.ServiceCapacityReport) {
		delete(report.Resources, "capacity")
		report.InfoVersion = 2
	})
	s.LiquidClients["second"].ServiceInfo.Modify(func(info *liquid.ServiceInfo) {
		delete(info.Resources, "capacity")
		info.Version = 2
	})

	s.Clock.StepBy(1 * time.Hour)
	err := jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections))
	assert.ErrEqual(t, err, regexp.MustCompile(
		// the error is that ON DELETE CASCADE on services -> resources is stopped by ON DELETE RESTRICT on resources -> commitments;
		// we do not match the specific phrasing of the PostgreSQL error since it may change between versions
		`^failed in iteration 2: while scraping service 2: could not delete db.Resource record with key capacity:.*"project_commitments_az_resource_id_fkey"`,
	))
}

func TestScanCapacityWithMailNotification(t *testing.T) {
	srvInfo, firstCapacityReport, secondCapacityReport := commonScanCapacityWithCommitmentsSetup()

	s := test.NewSetup(t,
		test.WithConfig(`{
			"availability_zones": ["az-one", "az-two"],
			"discovery": {
				"method": "static",
				"static_config": {
					"domains": [{"name": "germany", "id": "uuid-for-germany"}],
					"projects": {
						"uuid-for-germany": [
							{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"},
							{"name": "dresden", "id": "uuid-for-dresden", "parent_id": "uuid-for-berlin"}
						]
					}
				}
			},
			"liquids": {
				"first": {
					"area": "first",
					"commitment_behavior_per_resource": [
						{"key": "capacity", "value": {"durations_per_domain": [{"key": ".*", "value": ["1 hour", "10 days"]}]}}
					]
				},
				"second": {
					"area": "second",
					"commitment_behavior_per_resource": [
						{"key": "capacity", "value": {"durations_per_domain": [{"key": ".*", "value": ["1 hour", "10 days"]}]}}
					]
				}
			},
			"resource_behavior": [
				{"resource": "second/capacity", "identity_in_v1_api": "service/resource"}
			],
			"mail_notifications": {
				"templates": {
					"confirmed_commitments": {
						"subject": "Your recent commitment confirmations",
						"body": "Domain:{{ .DomainName }} Project:{{ .ProjectName }}{{ range .Commitments }} Creator:{{ .Commitment.CreatorName }} Amount:{{ .Commitment.Amount }} Duration:{{ .Commitment.Duration }} Date:{{ .DateString }} Service:{{ .Resource.ServiceType }} Resource:{{ .Resource.ResourceName }} AZ:{{ .Resource.AvailabilityZone }}{{ end }}"
					}
				}
			}
		}`),
		test.WithMockLiquidClient("first", srvInfo),
		test.WithMockLiquidClient("second", srvInfo),
		test.WithLiquidConnections,
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
	)
	s.LiquidClients["first"].CapacityReport.Set(firstCapacityReport)
	s.LiquidClients["second"].CapacityReport.Set(secondCapacityReport)
	job := s.Collector.CapacityScrapeJob(s.Registry)

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	// in each of the test steps below, the timestamp updates on services will always be the same
	timestampUpdates := func() string {
		scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
		scrapedAt2 := s.Clock.Now()
		return strings.TrimSpace(fmt.Sprintf(`
					UPDATE services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'first' AND liquid_version = 1;
					UPDATE services SET scraped_at = %d, next_scrape_at = %d WHERE id = 2 AND type = 'second' AND liquid_version = 1;
				`,
			scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
			scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
		))
	}

	// day 1: confirm two commitments in different projects -> one mail will be scheduled per project
	committedForTwoDays := must.Return(limesresources.ParseCommitmentDuration("2 days"))
	committedForTenDays := must.Return(limesresources.ParseCommitmentDuration("10 days"))
	s.MustDBInsert(&db.ProjectCommitment{
		UUID:                "00000000-0000-0000-0000-000000000001",
		ProjectID:           s.GetProjectID("berlin"),
		AZResourceID:        s.GetAZResourceID("first", "capacity", "az-one"),
		Amount:              10,
		CreatedAt:           time.Unix(0, 0),
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ConfirmBy:           Some(s.Clock.Now()),
		Duration:            committedForTenDays,
		ExpiresAt:           committedForTenDays.AddTo(s.Clock.Now()),
		Status:              liquid.CommitmentStatusPlanned,
		CreationContextJSON: json.RawMessage(`{}`),
		NotifyOnConfirm:     true,
	})
	s.MustDBInsert(&db.ProjectCommitment{
		UUID:                "00000000-0000-0000-0000-000000000002",
		ProjectID:           s.GetProjectID("dresden"),
		AZResourceID:        s.GetAZResourceID("second", "capacity", "az-one"),
		Amount:              1,
		CreatedAt:           s.Clock.Now(),
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ConfirmBy:           Some(s.Clock.Now().Add(12 * time.Hour)),
		Duration:            committedForTwoDays,
		ExpiresAt:           committedForTwoDays.AddTo(s.Clock.Now()),
		Status:              liquid.CommitmentStatusPlanned,
		CreationContextJSON: json.RawMessage(`{}`),
		NotifyOnConfirm:     true,
	})
	tr.DBChanges().Ignore()

	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET quota = 1 WHERE id = 17 AND project_id = 2 AND az_resource_id = 7;
		UPDATE project_az_resources SET quota = 10 WHERE id = 2 AND project_id = 1 AND az_resource_id = 2;
		UPDATE project_commitments SET status = 'confirmed', confirmed_at = %[1]d WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'confirmed', confirmed_at = %[2]d WHERE id = 2 AND uuid = '00000000-0000-0000-0000-000000000002' AND transfer_token = NULL;
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (1, 1, 'Your recent commitment confirmations', 'Domain:germany Project:berlin Creator:dummy Amount:10 Duration:10 days Date:1970-01-02 Service:first Resource:capacity AZ:az-one', %[1]d);
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (2, 2, 'Your recent commitment confirmations', 'Domain:germany Project:dresden Creator:dummy Amount:1 Duration:2 days Date:1970-01-02 Service:service Resource:resource AZ:az-one', %[2]d);
		UPDATE project_resources SET quota = 10 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		UPDATE project_resources SET quota = 1 WHERE id = 7 AND project_id = 2 AND resource_id = 3;
		UPDATE project_services SET quota_desynced_at = %[1]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services SET quota_desynced_at = %[2]d WHERE id = 4 AND project_id = 2 AND service_id = 2;
		%s
	`, scrapedAt1.Unix(), scrapedAt2.Unix(), timestampUpdates())

	// day 2: confirm two commitments in the same project -> only one mail will be scheduled regarding both confirmations
	for _, uuid := range []liquid.CommitmentUUID{"00000000-0000-0000-0000-000000000003", "00000000-0000-0000-0000-000000000004"} {
		s.MustDBInsert(&db.ProjectCommitment{
			UUID:                uuid,
			ProjectID:           s.GetProjectID("dresden"),
			AZResourceID:        s.GetAZResourceID("second", "capacity", "az-one"),
			Amount:              1,
			CreatedAt:           s.Clock.Now(),
			CreatorUUID:         "dummy",
			CreatorName:         "dummy",
			ConfirmBy:           Some(s.Clock.Now()),
			Duration:            committedForTwoDays,
			ExpiresAt:           committedForTwoDays.AddTo(s.Clock.Now()),
			Status:              liquid.CommitmentStatusPlanned,
			CreationContextJSON: json.RawMessage(`{}`),
			NotifyOnConfirm:     true,
		})
	}
	tr.DBChanges().Ignore()

	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_az_resources SET quota = 2 WHERE id = 17 AND project_id = 2 AND az_resource_id = 7;
		UPDATE project_commitments SET status = 'expired' WHERE id = 2 AND uuid = '00000000-0000-0000-0000-000000000002' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'confirmed', confirmed_at = %[1]d WHERE id = 3 AND uuid = '00000000-0000-0000-0000-000000000003' AND transfer_token = NULL;
		UPDATE project_commitments SET status = 'confirmed', confirmed_at = %[1]d WHERE id = 4 AND uuid = '00000000-0000-0000-0000-000000000004' AND transfer_token = NULL;
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (3, 2, 'Your recent commitment confirmations', 'Domain:germany Project:dresden Creator:dummy Amount:1 Duration:2 days Date:1970-01-03 Service:service Resource:resource AZ:az-one Creator:dummy Amount:1 Duration:2 days Date:1970-01-03 Service:service Resource:resource AZ:az-one', %[1]d);
		UPDATE project_resources SET quota = 2 WHERE id = 7 AND project_id = 2 AND resource_id = 3;
		%[2]s
	`, scrapedAt2.Unix(), timestampUpdates())
}
