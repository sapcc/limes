// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	. "github.com/majewsky/gg/option"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

const (
	testScanCapacityConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: static
			static_config:
				domains:
					- { name: germany, id: uuid-for-germany }
					- { name: france,id: uuid-for-france }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
						- { name: dresden, id: uuid-for-dresden, parent_id: uuid-for-berlin }
					uuid-for-france:
						- { name: paris, id: uuid-for-paris, parent_id: uuid-for-france}
		liquids:
			shared:
				area: shared
				liquid_service_type: %[1]s
			unshared:
				area: unshared
				liquid_service_type: %[2]s
	`

	testScanCapacitySingleLiquidConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: static
			static_config:
				domains:
					- { name: germany, id: uuid-for-germany }
					- { name: france,id: uuid-for-france }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
						- { name: dresden, id: uuid-for-dresden, parent_id: uuid-for-berlin }
					uuid-for-france:
						- { name: paris, id: uuid-for-paris, parent_id: uuid-for-france}
		liquids:
			shared:
				area: shared
				liquid_service_type: %[1]s
	`

	testScanCapacityWithCommitmentsConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: static
			static_config:
				domains:
					- { id: uuid-for-germany, name: germany }
				projects:
					germany:
						- { id: uuid-for-berlin,  name: berlin }
						- { id: uuid-for-dresden, name: dresden }
		liquids:
			first:
				area: first
				liquid_service_type: %[1]s
				commitment_behavior_per_resource: &commitment-on-capacity
					- key: capacity
						value:
							durations_per_domain: [{ key: '.*', value: [ '1 hour', '10 days' ] }]
			second:
				area: second
				liquid_service_type: %[2]s
				commitment_behavior_per_resource: *commitment-on-capacity
		resource_behavior:
			# test that overcommit factor is considered when confirming commitments
			- { resource: first/capacity, overcommit_factor: 10.0 }
			- resource: second/capacity
				identity_in_v1_api: service/resource
		quota_distribution_configs:
			# test automatic project quota calculation with non-default settings on */capacity resources
			- { resource: '.*/capacity', model: autogrow, autogrow: { growth_multiplier: 1.0, project_base_quota: 10, usage_data_retention_period: 1m } }
		mail_notifications:
			templates:
				confirmed_commitments:
					subject: "Your recent commitment confirmations"
					body: "Domain:{{ .DomainName }} Project:{{ .ProjectName }}{{ range .Commitments }} Creator:{{ .Commitment.CreatorName }} Amount:{{ .Commitment.Amount }} Duration:{{ .Commitment.Duration }} Date:{{ .DateString }} Service:{{ .Resource.ServiceType }} Resource:{{ .Resource.ResourceName }} AZ:{{ .Resource.AvailabilityZone }}{{ end }}"
	`
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
	mockLiquidClient, liquidServiceType := test.NewMockLiquidClient(srvInfo)
	mockLiquidClient2, liquidServiceType2 := test.NewMockLiquidClient(srvInfo2)
	s := test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(testScanCapacityConfigYAML, liquidServiceType, liquidServiceType2)),
		// cluster_services must be created as a baseline
		test.WithLiquidConnections,
	)

	c := getCollector(t, s)
	job := c.CapacityScrapeJob(s.Registry)
	insertTime := s.Clock.Now()

	capacityReport := liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"things": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"any": {
						Capacity: 42,
						Usage:    Some[uint64](8),
					},
				},
			},
		},
	}
	mockLiquidClient.SetCapacityReport(capacityReport)
	capacityReport2 := liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"capacity": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"any": {
						Capacity: 42,
						Usage:    Some[uint64](8),
					},
				},
			},
		},
	}
	mockLiquidClient2.SetCapacityReport(capacityReport2)

	// check baseline
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (1, 1, 'any', 0);
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (2, 2, 'any', 0);
		INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_capacity, has_quota) VALUES (1, 1, 'things', 1, 'flat', TRUE, TRUE);
		INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, has_quota) VALUES (2, 2, 'capacity', 1, 'B', 'flat', TRUE, TRUE);
		INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', %[1]d, 1);
		INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version) VALUES (2, 'unshared', %[1]d, 1);
	`, s.Clock.Now().Unix())

	// check that capacity records are created correctly (and that nonexistent
	// resources are ignored by the scraper)
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_az_resources SET raw_capacity = 42, usage = 8, last_nonzero_raw_capacity = 42 WHERE id = 1 AND resource_id = 1 AND az = 'any';
		UPDATE cluster_az_resources SET raw_capacity = 42, usage = 8, last_nonzero_raw_capacity = 42 WHERE id = 2 AND resource_id = 2 AND az = 'any';
		UPDATE cluster_services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = 905 WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
		UPDATE cluster_services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = 910 WHERE id = 2 AND type = 'unshared' AND liquid_version = 1;
	`, insertTime.Add(5*time.Second).Unix(), insertTime.Add(10*time.Second).Unix())

	// insert some crap records
	unknownRes := &db.ClusterResource{
		ServiceID:     2,
		Name:          "unknown",
		LiquidVersion: 1,
	}
	err := s.DB.Insert(unknownRes)
	if err != nil {
		t.Error(err)
	}
	err = s.DB.Insert(&db.ClusterAZResource{
		ResourceID:       unknownRes.ID,
		AvailabilityZone: liquid.AvailabilityZoneAny,
		RawCapacity:      100,
		Usage:            Some[uint64](50),
	})
	if err != nil {
		t.Error(err)
	}
	_, err = s.DB.Exec(
		`DELETE FROM cluster_resources WHERE service_id = $1 AND name = $2`,
		1, "things",
	)
	if err != nil {
		t.Error(err)
	}
	capacityReport.Resources["things"].PerAZ["any"].Capacity = 23
	capacityReport.Resources["things"].PerAZ["any"].Usage = Some[uint64](4)
	tr.DBChanges().Ignore()

	// if we don't bump the version, we will observe that for "things" nothing happens (as it is unknown
	// to the database) and for "unknown" there is no value
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
		UPDATE cluster_services SET scraped_at = %d, next_scrape_at = %d WHERE id = 2 AND type = 'unshared' AND liquid_version = 1;
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
	)

	// now we bump the version, so that the services and resources are reconciled
	mockLiquidClient.IncrementServiceInfoVersion()
	mockLiquidClient.IncrementCapacityReportInfoVersion()
	mockLiquidClient2.IncrementServiceInfoVersion()
	mockLiquidClient2.IncrementCapacityReportInfoVersion()
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		DELETE FROM cluster_az_resources WHERE id = 3 AND resource_id = 3 AND az = 'any';
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity, usage, last_nonzero_raw_capacity) VALUES (4, 4, 'any', 23, 4, 23);
		UPDATE cluster_resources SET liquid_version = 2 WHERE id = 2 AND service_id = 2 AND name = 'capacity';
		DELETE FROM cluster_resources WHERE id = 3 AND service_id = 2 AND name = 'unknown';
		INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_capacity, has_quota) VALUES (4, 1, 'things', 2, 'flat', TRUE, TRUE);
		DELETE FROM cluster_services WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
		INSERT INTO cluster_services (id, type, scraped_at, scrape_duration_secs, serialized_metrics, next_scrape_at, liquid_version) VALUES (1, 'shared', %d, 5, '{}', %d, 2);
		DELETE FROM cluster_services WHERE id = 2 AND type = 'unshared' AND liquid_version = 1;
		INSERT INTO cluster_services (id, type, scraped_at, scrape_duration_secs, serialized_metrics, next_scrape_at, liquid_version) VALUES (2, 'unshared', %d, 5, '{}', %d, 2);
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
	)

	dmr := &DataMetricsReporter{Cluster: s.Cluster, DB: s.DB, ReportZeroes: true}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": contentTypeForPrometheusMetrics},
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
	mockLiquidClient, liquidServiceType := test.NewMockLiquidClient(srvInfo)
	s := test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(testScanCapacitySingleLiquidConfigYAML, liquidServiceType)),
		// cluster_services must be created as a baseline
		test.WithLiquidConnections,
	)

	c := getCollector(t, s)
	job := c.CapacityScrapeJob(s.Registry)

	// check baseline
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (1, 1, 'any', 0);
		INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_capacity, has_quota) VALUES (1, 1, 'things', 1, 'flat', TRUE, TRUE);
		INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version, capacity_metric_families_json) VALUES (1, 'shared', %[1]d, 1, '{"limes_unittest_capacity_larger_half":{"type":"gauge","help":"","labelKeys":null},"limes_unittest_capacity_smaller_half":{"type":"gauge","help":"","labelKeys":null}}');
	`, s.Clock.Now().Unix())

	// check that scraping correctly updates subcapacities on an existing record
	buf := must.Return(json.Marshal(map[string]any{"az": "az-one"}))
	buf2 := must.Return(json.Marshal(map[string]any{"az": "az-two"}))
	capacityReport := liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"things": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"any": {
						Capacity: 42,
						Subcapacities: []liquid.Subcapacity{
							{
								Name:       "smaller_half",
								Capacity:   7,
								Attributes: json.RawMessage(buf),
							},
							{
								Name:       "larger_half",
								Capacity:   14,
								Attributes: json.RawMessage(buf),
							},
							{
								Name:       "smaller_half",
								Capacity:   7,
								Attributes: json.RawMessage(buf2),
							},
							{
								Name:       "larger_half",
								Capacity:   14,
								Attributes: json.RawMessage(buf2),
							},
						},
					},
				},
			},
		},
		Metrics: map[liquid.MetricName][]liquid.Metric{
			"limes_unittest_capacity_smaller_half": {{Value: 3}},
			"limes_unittest_capacity_larger_half":  {{Value: 7}},
		},
	}
	mockLiquidClient.SetCapacityReport(capacityReport)
	setClusterCapacitorsStale(t, s)
	s.Clock.StepBy(5 * time.Minute) // to force a capacitor consistency check to run
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_az_resources SET raw_capacity = 42, subcapacities = '[{"name":"smaller_half","capacity":7,"attributes":{"az":"az-one"}},{"name":"larger_half","capacity":14,"attributes":{"az":"az-one"}},{"name":"smaller_half","capacity":7,"attributes":{"az":"az-two"}},{"name":"larger_half","capacity":14,"attributes":{"az":"az-two"}}]', last_nonzero_raw_capacity = 42 WHERE id = 1 AND resource_id = 1 AND az = 'any';
		UPDATE cluster_services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{"limes_unittest_capacity_larger_half":{"lk":null,"m":[{"v":7,"l":null}]},"limes_unittest_capacity_smaller_half":{"lk":null,"m":[{"v":3,"l":null}]}}', next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		scrapedAt.Unix(), scrapedAt.Add(15*time.Minute).Unix(),
	)

	// check that scraping correctly updates subcapacities on an existing record
	capacityReport.Resources["things"].PerAZ["any"].Capacity = 10
	capacityReport.Resources["things"].PerAZ["any"].Subcapacities = []liquid.Subcapacity{
		{
			Name:       "smaller_half",
			Capacity:   1,
			Attributes: json.RawMessage(buf),
		},
		{
			Name:       "larger_half",
			Capacity:   4,
			Attributes: json.RawMessage(buf),
		},
		{
			Name:       "smaller_half",
			Capacity:   1,
			Attributes: json.RawMessage(buf2),
		},
		{
			Name:       "larger_half",
			Capacity:   4,
			Attributes: json.RawMessage(buf2),
		},
	}
	mockLiquidClient.SetCapacityReport(capacityReport)
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_az_resources SET raw_capacity = 10, subcapacities = '[{"name":"smaller_half","capacity":1,"attributes":{"az":"az-one"}},{"name":"larger_half","capacity":4,"attributes":{"az":"az-one"}},{"name":"smaller_half","capacity":1,"attributes":{"az":"az-two"}},{"name":"larger_half","capacity":4,"attributes":{"az":"az-two"}}]', last_nonzero_raw_capacity = 10 WHERE id = 1 AND resource_id = 1 AND az = 'any';
		UPDATE cluster_services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		scrapedAt.Unix(), scrapedAt.Add(15*time.Minute).Unix(),
	)

	// check data metrics generated for these capacity data
	registry := prometheus.NewPedanticRegistry()
	pmc := &CapacityCollectionMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(pmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": contentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/capacity_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	dmr := &DataMetricsReporter{Cluster: s.Cluster, DB: s.DB, ReportZeroes: true}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": contentTypeForPrometheusMetrics},
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
	mockLiquidClient, liquidServiceType := test.NewMockLiquidClient(srvInfo)
	s := test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(testScanCapacitySingleLiquidConfigYAML, liquidServiceType)),
		// cluster_services must be created as a baseline
		test.WithLiquidConnections,
	)

	c := getCollector(t, s)
	job := c.CapacityScrapeJob(s.Registry)

	// check baseline
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (1, 1, 'any', 0);
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (2, 1, 'az-one', 0);
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (3, 1, 'az-two', 0);
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (4, 1, 'unknown', 0);
		INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_capacity, has_quota) VALUES (1, 1, 'things', 1, 'az-aware', TRUE, TRUE);
		INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', %[1]d, 1);
	`, s.Clock.Now().Unix())

	capacityReport := liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"things": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": {
						Capacity: 21,
						Usage:    Some[uint64](4),
					},
					"az-two": {
						Capacity: 21,
						Usage:    Some[uint64](4),
					},
				},
			},
		},
	}
	mockLiquidClient.SetCapacityReport(capacityReport)
	setClusterCapacitorsStale(t, s)
	s.Clock.StepBy(5 * time.Minute) // to force a capacitor consistency check to run
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_az_resources SET raw_capacity = 21, usage = 4, last_nonzero_raw_capacity = 21 WHERE id = 2 AND resource_id = 1 AND az = 'az-one';
		UPDATE cluster_az_resources SET raw_capacity = 21, usage = 4, last_nonzero_raw_capacity = 21 WHERE id = 3 AND resource_id = 1 AND az = 'az-two';
		UPDATE cluster_services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		scrapedAt.Unix(), scrapedAt.Add(15*time.Minute).Unix(),
	)

	// check that scraping correctly updates the capacities on an existing record
	capacityReport.Resources["things"].PerAZ["az-one"].Capacity = 15
	capacityReport.Resources["things"].PerAZ["az-one"].Usage = Some[uint64](3)
	capacityReport.Resources["things"].PerAZ["az-two"].Capacity = 15
	capacityReport.Resources["things"].PerAZ["az-two"].Usage = Some[uint64](3)
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_az_resources SET raw_capacity = 15, usage = 3, last_nonzero_raw_capacity = 15 WHERE id = 2 AND resource_id = 1 AND az = 'az-one';
		UPDATE cluster_az_resources SET raw_capacity = 15, usage = 3, last_nonzero_raw_capacity = 15 WHERE id = 3 AND resource_id = 1 AND az = 'az-two';
		UPDATE cluster_services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		scrapedAt.Unix(), scrapedAt.Add(15*time.Minute).Unix(),
	)

	dmr := &DataMetricsReporter{Cluster: s.Cluster, DB: s.DB, ReportZeroes: true}
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: http.StatusOK,
		ExpectHeader: map[string]string{"Content-Type": contentTypeForPrometheusMetrics},
		ExpectBody:   assert.FixtureFile("fixtures/capacity_data_metrics_azaware.prom"),
	}.Check(t, dmr)

	// check that removing a LiquidConnection does nothing special (will be auto-removed later)
	delete(s.Cluster.LiquidConnections, "unittest")
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))
	scrapedAt = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`, scrapedAt.Unix(), scrapedAt.Add(15*time.Minute).Unix())
}

func TestScanCapacityReportsZeroValues(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo()
	mockLiquidClient, liquidServiceType := test.NewMockLiquidClient(srvInfo)
	s := test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(testScanCapacitySingleLiquidConfigYAML, liquidServiceType)),
		// cluster_services must be created as a baseline
		test.WithLiquidConnections,
	)

	c := getCollector(t, s)
	job := c.CapacityScrapeJob(s.Registry)

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	// when the capacity report shows zero capacity and usage...
	res := srvInfo.Resources["things"]
	res.HasCapacity = true
	srvInfo.Resources["things"] = res
	zeroReport := liquid.ServiceCapacityReport{
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
	}
	mockLiquidClient.SetCapacityReport(zeroReport)

	// ...scrape will record those values faithfully and not set "last_nonzero_raw_capacity"
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_az_resources SET usage = 0 WHERE id = 2 AND resource_id = 1 AND az = 'az-one';
		UPDATE cluster_az_resources SET usage = 0 WHERE id = 3 AND resource_id = 1 AND az = 'az-two';
		UPDATE cluster_az_resources SET usage = 0 WHERE id = 5 AND resource_id = 2 AND az = 'any';
		UPDATE cluster_services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)

	// when the capacity report shows non-zero capacity and usage...
	mockLiquidClient.SetCapacityReport(liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"capacity": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": {Capacity: 10, Usage: Some[uint64](5)},
					"az-two": {Capacity: 10, Usage: Some[uint64](5)},
				},
			},
			"things": {
				PerAZ: liquid.InAnyAZ(liquid.AZResourceCapacityReport{Capacity: 20, Usage: Some[uint64](10)}),
			},
		},
	})

	// ...scrape will record those values and set "last_nonzero_raw_capacity" because a non-zero value was observed
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_az_resources SET raw_capacity = 10, usage = 5, last_nonzero_raw_capacity = 10 WHERE id = 2 AND resource_id = 1 AND az = 'az-one';
		UPDATE cluster_az_resources SET raw_capacity = 10, usage = 5, last_nonzero_raw_capacity = 10 WHERE id = 3 AND resource_id = 1 AND az = 'az-two';
		UPDATE cluster_az_resources SET raw_capacity = 20, usage = 10, last_nonzero_raw_capacity = 20 WHERE id = 5 AND resource_id = 2 AND az = 'any';
		UPDATE cluster_services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)

	// when the capacity report once again shows zero capacity and usage afterwards...
	mockLiquidClient.SetCapacityReport(zeroReport)

	// ...scrape will record those values and, once again, leave "last_nonzero_raw_capacity" untouched
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_az_resources SET raw_capacity = 0, usage = 0 WHERE id = 2 AND resource_id = 1 AND az = 'az-one';
		UPDATE cluster_az_resources SET raw_capacity = 0, usage = 0 WHERE id = 3 AND resource_id = 1 AND az = 'az-two';
		UPDATE cluster_az_resources SET raw_capacity = 0, usage = 0 WHERE id = 5 AND resource_id = 2 AND az = 'any';
		UPDATE cluster_services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)
}

func setClusterCapacitorsStale(t *testing.T, s test.Setup) {
	t.Helper()
	_, err := s.DB.Exec(`UPDATE cluster_services SET next_scrape_at = $1`, s.Clock.Now())
	mustT(t, err)
}

func Test_ScanCapacityButNoResources(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo()
	mockLiquidClient, liquidServiceType := test.NewMockLiquidClient(srvInfo)
	s := test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(testScanCapacitySingleLiquidConfigYAML, liquidServiceType)),
		// cluster_services must be created as a baseline
		test.WithLiquidConnections,
	)

	c := getCollector(t, s)
	job := c.CapacityScrapeJob(s.Registry)

	// check baseline
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (1, 1, 'any', 0);
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (2, 1, 'az-one', 0);
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (3, 1, 'az-two', 0);
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (4, 1, 'unknown', 0);
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (5, 2, 'any', 0);
		INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota) VALUES (1, 1, 'capacity', 1, 'B', 'az-aware'%[1]s%[1]s%[1]s);
		INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (2, 1, 'things', 1, 'flat', TRUE);
		INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', %[2]d, 1);
	`,
		", TRUE", // TODO: remove this workaround once dupword in golangci-lint has been updated to not trip on "TRUE, TRUE, TRUE" in an SQL literal
		s.Clock.Now().Unix(),
	)

	// adjust the capacity report to not show any resources
	// this is a state which should not happen in production - it leads to a logged error
	res := srvInfo.Resources["capacity"]
	res.HasCapacity = false
	srvInfo.Resources["capacity"] = res
	mockLiquidClient.SetCapacityReport(liquid.ServiceCapacityReport{
		InfoVersion: 1,
	})

	// check that the capacitor runs, but does not touch cluster_resources and cluster_az_resources
	// since it does not report for anything (this used to fail because we generated a syntactically
	// invalid WHERE clause when matching zero resources)
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))

	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)

	// rerun also works
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))

	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_services SET scraped_at = %[1]d, next_scrape_at = %[2]d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)

	// now we bump the version, so that the services and resources are reconciled
	mockLiquidClient.IncrementServiceInfoVersion()
	mockLiquidClient.IncrementCapacityReportInfoVersion()
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))

	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_resources SET liquid_version = 2, has_capacity = FALSE WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE cluster_resources SET liquid_version = 2 WHERE id = 2 AND service_id = 1 AND name = 'things';
		DELETE FROM cluster_services WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
		INSERT INTO cluster_services (id, type, scraped_at, scrape_duration_secs, serialized_metrics, next_scrape_at, liquid_version) VALUES (1, 'shared', %[1]d, 5, '{}', %[2]d, 2);
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)
}

func Test_ScanManualCapacity(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo()
	testScanCapacityManualConfigYAML := testScanCapacitySingleLiquidConfigYAML + `
				fixed_capacity_values:
					things: 1000000`
	mockLiquidClient, liquidServiceType := test.NewMockLiquidClient(srvInfo)
	s := test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(testScanCapacityManualConfigYAML, liquidServiceType)),
		test.WithLiquidConnections,
	)

	c := getCollector(t, s)
	job := c.CapacityScrapeJob(s.Registry)

	// check baseline
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (1, 1, 'any', 0);
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (2, 1, 'az-one', 0);
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (3, 1, 'az-two', 0);
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (4, 1, 'unknown', 0);
		INSERT INTO cluster_az_resources (id, resource_id, az, raw_capacity) VALUES (5, 2, 'any', 0);
		INSERT INTO cluster_resources (id, service_id, name, liquid_version, unit, topology, has_capacity, needs_resource_demand, has_quota) VALUES (1, 1, 'capacity', 1, 'B', 'az-aware'%[1]s%[1]s%[1]s);
		INSERT INTO cluster_resources (id, service_id, name, liquid_version, topology, has_quota) VALUES (2, 1, 'things', 1, 'flat', TRUE);
		INSERT INTO cluster_services (id, type, next_scrape_at, liquid_version) VALUES (1, 'shared', %[2]d, 1);
	`,
		", TRUE", // TODO: remove this workaround once dupword in golangci-lint has been updated to not trip on "TRUE, TRUE, TRUE" in an SQL literal
		s.Clock.Now().Unix(),
	)

	// adjust the capacity report to not show any capacity
	// this is a state which should not happen in production - it leads to a logged error
	res := srvInfo.Resources["capacity"]
	res.HasCapacity = false
	srvInfo.Resources["capacity"] = res
	mockLiquidClient.SetCapacityReport(liquid.ServiceCapacityReport{
		InfoVersion: 1,
	})

	// normal resource are not written, but the manual resource is
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))

	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_az_resources SET raw_capacity = 1000000, last_nonzero_raw_capacity = 1000000 WHERE id = 5 AND resource_id = 2 AND az = 'any';
		UPDATE cluster_services SET scraped_at = %d, scrape_duration_secs = 5, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)

	// now we bump the version, so that the services and resources are reconciled
	mockLiquidClient.IncrementServiceInfoVersion()
	mockLiquidClient.IncrementCapacityReportInfoVersion()
	setClusterCapacitorsStale(t, s)
	mustT(t, job.ProcessOne(s.Ctx))

	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_resources SET liquid_version = 2, has_capacity = FALSE WHERE id = 1 AND service_id = 1 AND name = 'capacity';
		UPDATE cluster_resources SET liquid_version = 2 WHERE id = 2 AND service_id = 1 AND name = 'things';
		DELETE FROM cluster_services WHERE id = 1 AND type = 'shared' AND liquid_version = 1;
		INSERT INTO cluster_services (id, type, scraped_at, scrape_duration_secs, serialized_metrics, next_scrape_at, liquid_version) VALUES (1, 'shared', %d, 5, '{}', %d, 2);
	`,
		s.Clock.Now().Unix(), s.Clock.Now().Add(15*time.Minute).Unix(),
	)
}

func CommonScanCapacityWithCommitmentsSetup(t *testing.T) (
	s test.Setup, scrapeJob jobloop.Job,
	firstLiquidClient *test.MockLiquidClient, secondLiquidClient *test.MockLiquidClient,
	firstCapacityReport liquid.ServiceCapacityReport, secondCapacityReport liquid.ServiceCapacityReport,
	firstServiceInfo liquid.ServiceInfo, secondServiceInfo liquid.ServiceInfo,
) {

	firstServiceInfo = liquid.ServiceInfo{
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
				Topology:    liquid.AZAwareTopology,
				HasCapacity: true,
				HasQuota:    true,
			},
		},
	}
	secondServiceInfo = liquid.ServiceInfo{
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
				Topology:    liquid.AZAwareTopology,
				HasCapacity: true,
				HasQuota:    true,
			},
		},
	}
	firstLiquidClient, liquidServiceType := test.NewMockLiquidClient(firstServiceInfo)
	secondLiquidClient, liquidServiceType2 := test.NewMockLiquidClient(secondServiceInfo)
	s = test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(testScanCapacityWithCommitmentsConfigYAML, liquidServiceType, liquidServiceType2)),
		test.WithDBFixtureFile("fixtures/capacity_scrape_with_commitments.sql"),
		test.WithLiquidConnections,
	)
	c := getCollector(t, s)
	scrapeJob = c.CapacityScrapeJob(s.Registry)

	firstCapacityReport = liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"capacity": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": {
						Capacity: 42,
						Usage:    Some[uint64](8),
					},
					"az-two": {
						Capacity: 42,
						Usage:    Some[uint64](8),
					},
				},
			},
			"things": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": {
						Capacity: 42,
						Usage:    Some[uint64](8),
					},
					"az-two": {
						Capacity: 42,
						Usage:    Some[uint64](8),
					},
				},
			},
		},
	}
	firstLiquidClient.SetCapacityReport(firstCapacityReport)
	secondCapacityReport = liquid.ServiceCapacityReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceCapacityReport{
			"capacity": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": {
						Capacity: 23,
						Usage:    Some[uint64](4),
					},
					"az-two": {
						Capacity: 23,
						Usage:    Some[uint64](4),
					},
				},
			},
			"things": {
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport{
					"az-one": {
						Capacity: 23,
						Usage:    Some[uint64](4),
					},
					"az-two": {
						Capacity: 23,
						Usage:    Some[uint64](4),
					},
				},
			},
		},
	}
	secondLiquidClient.SetCapacityReport(secondCapacityReport)

	return
}

func Test_ScanCapacityWithCommitments(t *testing.T) {
	s, job, firstLiquidClient, secondLiquidClient, firstCapacityReport, secondCapacityReport, firstServiceInfo, secondServiceInfo := CommonScanCapacityWithCommitmentsSetup(t)

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	// in each of the test steps below, the timestamp updates on cluster_services will always be the same
	timestampUpdates := func(initMetrics bool) string {
		scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
		scrapedAt2 := s.Clock.Now()
		if !initMetrics {
			return strings.TrimSpace(fmt.Sprintf(`
				UPDATE cluster_services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'first' AND liquid_version = 1;
				UPDATE cluster_services SET scraped_at = %d, next_scrape_at = %d WHERE id = 2 AND type = 'second' AND liquid_version = 1;
			`,
				scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
				scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
			))
		}
		return strings.TrimSpace(fmt.Sprintf(`
				UPDATE cluster_services SET scraped_at = %d, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 1 AND type = 'first' AND liquid_version = 1;
				UPDATE cluster_services SET scraped_at = %d, serialized_metrics = '{}', next_scrape_at = %d WHERE id = 2 AND type = 'second' AND liquid_version = 1;
			`,
			scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
			scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
		))
	}

	// first run should create the cluster_resources and cluster_az_resources, but
	// not confirm any commitments because they all start with `confirm_by > now`
	//
	// however, there will be a lot of quota changes because we run
	// ApplyComputedProjectQuota() for the first time
	//
	// Note that the "things" resources are not explicitly set up in the
	// quota_distribution_configs test section. The automatic behavior amounts to
	// pretty much just setting `quota = usage`, i.e. `quota = 0` in this case.
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	desyncedAt1 := s.Clock.Now().Add(-5 * time.Second)
	desyncedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`%s
		UPDATE project_az_resources_v2 SET quota = 0 WHERE id = 1 AND project_id = 1 AND az_resource_id = 1;
		UPDATE project_az_resources_v2 SET quota = 1 WHERE id = 10 AND project_id = 1 AND az_resource_id = 12;
		UPDATE project_az_resources_v2 SET quota = 8 WHERE id = 11 AND project_id = 2 AND az_resource_id = 4;
		UPDATE project_az_resources_v2 SET quota = 1 WHERE id = 12 AND project_id = 2 AND az_resource_id = 5;
		UPDATE project_az_resources_v2 SET quota = 1 WHERE id = 13 AND project_id = 2 AND az_resource_id = 6;
		UPDATE project_az_resources_v2 SET quota = 8 WHERE id = 14 AND project_id = 2 AND az_resource_id = 10;
		UPDATE project_az_resources_v2 SET quota = 1 WHERE id = 15 AND project_id = 2 AND az_resource_id = 11;
		UPDATE project_az_resources_v2 SET quota = 1 WHERE id = 16 AND project_id = 2 AND az_resource_id = 12;
		UPDATE project_az_resources_v2 SET quota = 0 WHERE id = 2 AND project_id = 1 AND az_resource_id = 7;
		UPDATE project_az_resources_v2 SET quota = 0 WHERE id = 3 AND project_id = 2 AND az_resource_id = 1;
		UPDATE project_az_resources_v2 SET quota = 0 WHERE id = 4 AND project_id = 2 AND az_resource_id = 7;
		UPDATE project_az_resources_v2 SET quota = 0 WHERE id = 5 AND project_id = 1 AND az_resource_id = 4;
		UPDATE project_az_resources_v2 SET quota = 1 WHERE id = 6 AND project_id = 1 AND az_resource_id = 5;
		UPDATE project_az_resources_v2 SET quota = 250 WHERE id = 7 AND project_id = 1 AND az_resource_id = 6;
		UPDATE project_az_resources_v2 SET quota = 8 WHERE id = 8 AND project_id = 1 AND az_resource_id = 10;
		UPDATE project_az_resources_v2 SET quota = 1 WHERE id = 9 AND project_id = 1 AND az_resource_id = 11;
		UPDATE project_resources_v2 SET quota = 0 WHERE id = 1 AND project_id = 1 AND resource_id = 1;
		UPDATE project_resources_v2 SET quota = 251 WHERE id = 2 AND project_id = 1 AND resource_id = 2;
		UPDATE project_resources_v2 SET quota = 0 WHERE id = 3 AND project_id = 1 AND resource_id = 3;
		UPDATE project_resources_v2 SET quota = 10 WHERE id = 4 AND project_id = 1 AND resource_id = 4;
		UPDATE project_resources_v2 SET quota = 0 WHERE id = 5 AND project_id = 2 AND resource_id = 1;
		UPDATE project_resources_v2 SET quota = 10 WHERE id = 6 AND project_id = 2 AND resource_id = 2;
		UPDATE project_resources_v2 SET quota = 0 WHERE id = 7 AND project_id = 2 AND resource_id = 3;
		UPDATE project_resources_v2 SET quota = 10 WHERE id = 8 AND project_id = 2 AND resource_id = 4;
		UPDATE project_services_v2 SET quota_desynced_at = %[2]d WHERE id = 1 AND project_id = 1 AND service_id = 1;
		UPDATE project_services_v2 SET quota_desynced_at = %[3]d WHERE id = 2 AND project_id = 1 AND service_id = 2;
		UPDATE project_services_v2 SET quota_desynced_at = %[2]d WHERE id = 3 AND project_id = 2 AND service_id = 1;
		UPDATE project_services_v2 SET quota_desynced_at = %[3]d WHERE id = 4 AND project_id = 2 AND service_id = 2;
	`, timestampUpdates(true), desyncedAt1.Unix(), desyncedAt2.Unix())

	// day 1: test that confirmation works at all
	//
	// The confirmed commitment is for first/capacity in berlin az-one (amount = 10).
	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	tr.DBChanges().AssertEqualf(`%s
		UPDATE project_az_resources_v2 SET quota = 10 WHERE id = 6 AND project_id = 1 AND az_resource_id = 5;
		UPDATE project_commitments_v2 SET state = 'active', confirmed_at = %d WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;
		UPDATE project_resources_v2 SET quota = 260 WHERE id = 2 AND project_id = 1 AND resource_id = 2;
	`, timestampUpdates(false), scrapedAt1.Unix())

	// day 2: test that confirmation considers the resource's capacity overcommit factor
	//
	// The confirmed commitment (ID=2) is for first/capacity in berlin az-one (amount = 100).
	// A similar commitment (ID=3) for second/capacity is not confirmed because of missing capacity.
	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	tr.DBChanges().AssertEqualf(`%s
		UPDATE project_az_resources_v2 SET quota = 110 WHERE id = 6 AND project_id = 1 AND az_resource_id = 5;
		UPDATE project_commitments_v2 SET state = 'active', confirmed_at = %d WHERE id = 2 AND uuid = '00000000-0000-0000-0000-000000000002' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'pending' WHERE id = 3 AND uuid = '00000000-0000-0000-0000-000000000003' AND transfer_token = NULL;
		UPDATE project_resources_v2 SET quota = 360 WHERE id = 2 AND project_id = 1 AND resource_id = 2;
	`, timestampUpdates(false), scrapedAt1.Unix())

	// day 3: test confirmation order with several commitments, on second/capacity in az-one
	//
	// The previously not confirmed commitment (ID=3) does not block confirmation of smaller confirmable commitments.
	// Only two of three commitments are confirmed. The third commitment exhausts the available capacity.
	// The two commitments that are confirmed (ID=4 and ID=5) have a lower created_at than the unconfirmed one (ID=6).
	// This is because we want to ensure the "first come, first serve" rule.
	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`%s
		UPDATE project_az_resources_v2 SET quota = 0 WHERE id = 14 AND project_id = 2 AND az_resource_id = 10;
		UPDATE project_az_resources_v2 SET quota = 20 WHERE id = 15 AND project_id = 2 AND az_resource_id = 11;
		UPDATE project_commitments_v2 SET state = 'active', confirmed_at = %d WHERE id = 4 AND uuid = '00000000-0000-0000-0000-000000000004' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'active', confirmed_at = %d WHERE id = 5 AND uuid = '00000000-0000-0000-0000-000000000005' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'pending' WHERE id = 6 AND uuid = '00000000-0000-0000-0000-000000000006' AND transfer_token = NULL;
		UPDATE project_resources_v2 SET quota = 21 WHERE id = 8 AND project_id = 2 AND resource_id = 4;
	`, timestampUpdates(false), scrapedAt2.Unix(), scrapedAt2.Unix())

	// day 4: test how confirmation interacts with existing usage, on first/capacity in az-two
	//
	// Both dresden (ID=7) and berlin (ID=8) are asking for an amount of 300 to be committed, on a total capacity of 420.
	// But because berlin has an existing usage of 250, dresden is denied (even though it asked first) and berlin is confirmed.
	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt1 = s.Clock.Now().Add(-5 * time.Second)
	tr.DBChanges().AssertEqualf(`%s
		UPDATE project_az_resources_v2 SET quota = 300 WHERE id = 7 AND project_id = 1 AND az_resource_id = 6;
		UPDATE project_commitments_v2 SET state = 'pending' WHERE id = 7 AND uuid = '00000000-0000-0000-0000-000000000007' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'active', confirmed_at = %d WHERE id = 8 AND uuid = '00000000-0000-0000-0000-000000000008' AND transfer_token = NULL;
		UPDATE project_resources_v2 SET quota = 410 WHERE id = 2 AND project_id = 1 AND resource_id = 2;
	`, timestampUpdates(false), scrapedAt1.Unix())

	// day 5: test commitments that cannot be confirmed until the previous commitment expires, on second/capacity in az-one
	//
	// The first commitment (ID=9 in berlin) is confirmed because no other commitments are confirmed yet.
	// The second commitment (ID=10 in dresden) is much smaller (only 1 larger than project usage),
	// but cannot be confirmed because ID=9 grabbed any and all unused capacity.
	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`%s
		UPDATE project_az_resources_v2 SET quota = 22 WHERE id = 10 AND project_id = 1 AND az_resource_id = 12;
		UPDATE project_az_resources_v2 SET quota = 0 WHERE id = 8 AND project_id = 1 AND az_resource_id = 10;
		UPDATE project_commitments_v2 SET state = 'pending' WHERE id = 10 AND uuid = '00000000-0000-0000-0000-000000000010' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'active', confirmed_at = %d WHERE id = 9 AND uuid = '00000000-0000-0000-0000-000000000009' AND transfer_token = NULL;
		UPDATE project_resources_v2 SET quota = 23 WHERE id = 4 AND project_id = 1 AND resource_id = 4;
	`, timestampUpdates(false), scrapedAt2.Unix())

	// ...Once ID=9 expires an hour later, ID=10 can be confirmed.
	s.Clock.StepBy(1 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`%s
		UPDATE project_az_resources_v2 SET quota = 1 WHERE id = 10 AND project_id = 1 AND az_resource_id = 12;
		UPDATE project_az_resources_v2 SET quota = 2 WHERE id = 16 AND project_id = 2 AND az_resource_id = 12;
		UPDATE project_az_resources_v2 SET quota = 8 WHERE id = 8 AND project_id = 1 AND az_resource_id = 10;
		UPDATE project_commitments_v2 SET state = 'active', confirmed_at = %d WHERE id = 10 AND uuid = '00000000-0000-0000-0000-000000000010' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'expired' WHERE id = 9 AND uuid = '00000000-0000-0000-0000-000000000009' AND transfer_token = NULL;
		UPDATE project_resources_v2 SET quota = 10 WHERE id = 4 AND project_id = 1 AND resource_id = 4;
		UPDATE project_resources_v2 SET quota = 22 WHERE id = 8 AND project_id = 2 AND resource_id = 4;
	`, timestampUpdates(false), scrapedAt2.Unix())

	// test GetGlobalResourceDemand (this is not used by any of our mock liquids,
	// but we can just call it directly to see that it works)
	bc := datamodel.NewCapacityScrapeBackchannel(s.Cluster, s.DB)
	expectedDemandsByService := map[db.ServiceType]map[liquid.ResourceName]map[liquid.AvailabilityZone]liquid.ResourceDemandInAZ{
		"first": {
			"capacity": {
				"az-one":                   {Usage: 2, UnusedCommitments: 109, PendingCommitments: 0},
				"az-two":                   {Usage: 251, UnusedCommitments: 50, PendingCommitments: 300},
				liquid.AvailabilityZoneAny: {Usage: 0, UnusedCommitments: 0, PendingCommitments: 0},
			},
			"things": {
				liquid.AvailabilityZoneAny: {Usage: 0, UnusedCommitments: 0, PendingCommitments: 0},
			},
		},
		"second": {
			"capacity": {
				"az-one":                   {Usage: 2, UnusedCommitments: 19, PendingCommitments: 110},
				"az-two":                   {Usage: 2, UnusedCommitments: 1, PendingCommitments: 0},
				liquid.AvailabilityZoneAny: {Usage: 0, UnusedCommitments: 0, PendingCommitments: 0},
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

	// now we let almost all commitments expire, so that we can test the cluster_az_resources_project_commitments_trigger
	// all are expired, 10 remains active
	s.Clock.StepBy(9 * 24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))
	tr.DBChanges().AssertEqualf(`%s
		UPDATE project_az_resources_v2 SET quota = 7 WHERE id = 14 AND project_id = 2 AND az_resource_id = 10;
		UPDATE project_az_resources_v2 SET quota = 1 WHERE id = 15 AND project_id = 2 AND az_resource_id = 11;
		UPDATE project_az_resources_v2 SET quota = 1 WHERE id = 6 AND project_id = 1 AND az_resource_id = 5;
		UPDATE project_az_resources_v2 SET quota = 250 WHERE id = 7 AND project_id = 1 AND az_resource_id = 6;
		UPDATE project_commitments_v2 SET state = 'expired' WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'expired' WHERE id = 2 AND uuid = '00000000-0000-0000-0000-000000000002' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'expired' WHERE id = 3 AND uuid = '00000000-0000-0000-0000-000000000003' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'expired' WHERE id = 4 AND uuid = '00000000-0000-0000-0000-000000000004' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'expired' WHERE id = 5 AND uuid = '00000000-0000-0000-0000-000000000005' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'expired' WHERE id = 6 AND uuid = '00000000-0000-0000-0000-000000000006' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'expired' WHERE id = 7 AND uuid = '00000000-0000-0000-0000-000000000007' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'expired' WHERE id = 8 AND uuid = '00000000-0000-0000-0000-000000000008' AND transfer_token = NULL;
		UPDATE project_resources_v2 SET quota = 251 WHERE id = 2 AND project_id = 1 AND resource_id = 2;
		UPDATE project_resources_v2 SET quota = 10 WHERE id = 8 AND project_id = 2 AND resource_id = 4;
	`, timestampUpdates(false))

	// we remove first/capacity, which does not have any active commitments. The trigger removes the expired commitments.
	delete(firstCapacityReport.Resources, "capacity")
	firstCapacityReport.InfoVersion = 2
	firstLiquidClient.SetCapacityReport(firstCapacityReport)
	delete(firstServiceInfo.Resources, "capacity")
	firstServiceInfo.Version = 2
	firstLiquidClient.SetServiceInfo(firstServiceInfo)

	s.Clock.StepBy(1 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))
	tr.DBChanges().AssertEqual(`
		DELETE FROM cluster_az_resources WHERE id = 14 AND resource_id = 2 AND az = 'unknown';
		DELETE FROM cluster_az_resources WHERE id = 4 AND resource_id = 2 AND az = 'any';
		DELETE FROM cluster_az_resources WHERE id = 5 AND resource_id = 2 AND az = 'az-one';
		DELETE FROM cluster_az_resources WHERE id = 6 AND resource_id = 2 AND az = 'az-two';
		UPDATE cluster_resources SET liquid_version = 2 WHERE id = 1 AND service_id = 1 AND name = 'things';
		DELETE FROM cluster_resources WHERE id = 2 AND service_id = 1 AND name = 'capacity';
		DELETE FROM cluster_services WHERE id = 1 AND type = 'first' AND liquid_version = 1;
		INSERT INTO cluster_services (id, type, scraped_at, scrape_duration_secs, serialized_metrics, next_scrape_at, liquid_version) VALUES (1, 'first', 1216885, 5, '{}', 1217785, 2);
		UPDATE cluster_services SET scraped_at = 1216890, next_scrape_at = 1217790 WHERE id = 2 AND type = 'second' AND liquid_version = 1;
		DELETE FROM project_az_resources_v2 WHERE id = 11 AND project_id = 2 AND az_resource_id = 4;
		DELETE FROM project_az_resources_v2 WHERE id = 12 AND project_id = 2 AND az_resource_id = 5;
		DELETE FROM project_az_resources_v2 WHERE id = 13 AND project_id = 2 AND az_resource_id = 6;
		DELETE FROM project_az_resources_v2 WHERE id = 5 AND project_id = 1 AND az_resource_id = 4;
		DELETE FROM project_az_resources_v2 WHERE id = 6 AND project_id = 1 AND az_resource_id = 5;
		DELETE FROM project_az_resources_v2 WHERE id = 7 AND project_id = 1 AND az_resource_id = 6;
		DELETE FROM project_commitments_v2 WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;
		DELETE FROM project_commitments_v2 WHERE id = 2 AND uuid = '00000000-0000-0000-0000-000000000002' AND transfer_token = NULL;
		DELETE FROM project_commitments_v2 WHERE id = 7 AND uuid = '00000000-0000-0000-0000-000000000007' AND transfer_token = NULL;
		DELETE FROM project_commitments_v2 WHERE id = 8 AND uuid = '00000000-0000-0000-0000-000000000008' AND transfer_token = NULL;
		DELETE FROM project_resources_v2 WHERE id = 2 AND project_id = 1 AND resource_id = 2;
		DELETE FROM project_resources_v2 WHERE id = 6 AND project_id = 2 AND resource_id = 2;
	`)

	// now we try to remove second/capacity, which has an active commitment. Hence, it will fail on SaveServiceInfoToDB
	delete(secondCapacityReport.Resources, "capacity")
	secondCapacityReport.InfoVersion = 2
	secondLiquidClient.SetCapacityReport(secondCapacityReport)
	delete(secondServiceInfo.Resources, "capacity")
	secondServiceInfo.Version = 2
	secondLiquidClient.SetServiceInfo(secondServiceInfo)

	s.Clock.StepBy(1 * time.Hour)
	mustFailT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)), errors.New(sqlext.SimplifyWhitespace(
		`failed in iteration 2: while scraping clusterService 2: could not delete db.ClusterResource record with key capacity:
		pq: update or delete on table "cluster_az_resources" violates foreign key constraint "project_commitments_v2_az_resource_id_fkey" on table "project_commitments_v2"
		(additional error while updating DB: pq: update or delete on table "cluster_services" violates foreign key constraint "cluster_resources_service_id_liquid_version_fkey" on table "cluster_resources"`)))
}

func TestScanCapacityWithMailNotification(t *testing.T) {
	s, job, _, _, _, _, _, _ := CommonScanCapacityWithCommitmentsSetup(t)

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	// in each of the test steps below, the timestamp updates on cluster_services will always be the same
	timestampUpdates := func() string {
		scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
		scrapedAt2 := s.Clock.Now()
		return strings.TrimSpace(fmt.Sprintf(`
					UPDATE cluster_services SET scraped_at = %d, next_scrape_at = %d WHERE id = 1 AND type = 'first' AND liquid_version = 1;
					UPDATE cluster_services SET scraped_at = %d, next_scrape_at = %d WHERE id = 2 AND type = 'second' AND liquid_version = 1;
				`,
			scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
			scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
		))
	}

	tr.DBChanges().Ignore()

	// day 1: schedule two mails for different projects
	// (Commitment ID: 1) Confirmed commitment for first/capacity in berlin az-one (amount = 10).
	_, err := s.DB.Exec("UPDATE project_commitments_v2 SET notify_on_confirm=true WHERE id=1;")
	if err != nil {
		t.Fatal(err)
	}
	// (Commitment ID: 11) Confirmed commitment for second/capacity in dresden az-one (amount = 1).
	_, err = s.DB.Exec(`
			INSERT INTO project_commitments_v2
			(id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, confirm_by, duration, expires_at, state, notify_on_confirm, creation_context_json)
			VALUES(11, '00000000-0000-0000-0000-000000000011', 2, 11, 1, $1, 'dummy', 'dummy', $2, '2 days', $3, 'planned', true, '{}'::jsonb)`, s.Clock.Now(), s.Clock.Now().Add(12*time.Hour), s.Clock.Now().Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`%s
		UPDATE project_az_resources_v2 SET quota = 10 WHERE id = 6 AND project_id = 1 AND az_resource_id = 5;
		UPDATE project_commitments_v2 SET state = 'active', confirmed_at = 86415, notify_on_confirm = TRUE WHERE id = 1 AND uuid = '00000000-0000-0000-0000-000000000001' AND transfer_token = NULL;
		INSERT INTO project_commitments_v2 (id, uuid, project_id, az_resource_id, state, amount, duration, created_at, creator_uuid, creator_name, confirm_by, confirmed_at, expires_at, notify_on_confirm, creation_context_json) VALUES (11, '00000000-0000-0000-0000-000000000011', 2, 11, 'active', 1, '2 days', 10, 'dummy', 'dummy', 43210, 86420, 172810, TRUE, '{}');
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (1, 1, 'Your recent commitment confirmations', 'Domain:germany Project:berlin Creator:dummy Amount:10 Duration:10 days Date:1970-01-02 Service:first Resource:capacity AZ:az-one', %[2]d);
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (2, 2, 'Your recent commitment confirmations', 'Domain:germany Project:dresden Creator:dummy Amount:1 Duration:2 days Date:1970-01-02 Service:service Resource:resource AZ:az-one', %[3]d);
		UPDATE project_resources_v2 SET quota = 260 WHERE id = 2 AND project_id = 1 AND resource_id = 2;
	`, timestampUpdates(), scrapedAt1.Unix(), scrapedAt2.Unix())

	// day 2: schedule one mail with two commitments for the same project.
	// (Commitment IDs: 12, 13) Confirmed commitment for second/capacity in dresden az-one (amount = 1).
	_, err = s.DB.Exec(`
			INSERT INTO project_commitments_v2
			(id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state, notify_on_confirm, creation_context_json)
			VALUES(12, '00000000-0000-0000-0000-000000000012', 2, 11, 1, $1, 'dummy', 'dummy', '2 days', $2, 'pending', true, '{}'::jsonb)`, s.Clock.Now(), s.Clock.Now().Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(`
			INSERT INTO project_commitments_v2
			(id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state, notify_on_confirm, creation_context_json)
			VALUES(13, '00000000-0000-0000-0000-000000000013', 2, 11, 1, $1, 'dummy', 'dummy', '2 days', $2, 'pending', true, '{}'::jsonb)`, s.Clock.Now(), s.Clock.Now().Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	s.Clock.StepBy(24 * time.Hour)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.LiquidConnections)))
	scrapedAt2 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`%s
		UPDATE project_az_resources_v2 SET quota = 7 WHERE id = 14 AND project_id = 2 AND az_resource_id = 10;
		UPDATE project_az_resources_v2 SET quota = 2 WHERE id = 15 AND project_id = 2 AND az_resource_id = 11;
		UPDATE project_az_resources_v2 SET quota = 110 WHERE id = 6 AND project_id = 1 AND az_resource_id = 5;
		UPDATE project_commitments_v2 SET state = 'expired' WHERE id = 11 AND uuid = '00000000-0000-0000-0000-000000000011' AND transfer_token = NULL;
		INSERT INTO project_commitments_v2 (id, uuid, project_id, az_resource_id, state, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, notify_on_confirm, creation_context_json) VALUES (12, '00000000-0000-0000-0000-000000000012', 2, 11, 'active', 1, '2 days', 86420, 'dummy', 'dummy', 172830, 259220, TRUE, '{}');
		INSERT INTO project_commitments_v2 (id, uuid, project_id, az_resource_id, state, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, notify_on_confirm, creation_context_json) VALUES (13, '00000000-0000-0000-0000-000000000013', 2, 11, 'active', 1, '2 days', 86420, 'dummy', 'dummy', 172830, 259220, TRUE, '{}');
		UPDATE project_commitments_v2 SET state = 'active', confirmed_at = 172825 WHERE id = 2 AND uuid = '00000000-0000-0000-0000-000000000002' AND transfer_token = NULL;
		UPDATE project_commitments_v2 SET state = 'pending' WHERE id = 3 AND uuid = '00000000-0000-0000-0000-000000000003' AND transfer_token = NULL;
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (3, 2, 'Your recent commitment confirmations', 'Domain:germany Project:dresden Creator:dummy Amount:1 Duration:2 days Date:1970-01-03 Service:service Resource:resource AZ:az-one Creator:dummy Amount:1 Duration:2 days Date:1970-01-03 Service:service Resource:resource AZ:az-one', %d);
		UPDATE project_resources_v2 SET quota = 360 WHERE id = 2 AND project_id = 1 AND resource_id = 2;
	`, timestampUpdates(), scrapedAt2.Unix())
}
