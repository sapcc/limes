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
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/jobloop"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/plugins"
)

const (
	testScanCapacityConfigYAML = `
		discovery:
			method: --test-static
		services:
			- service_type: shared
				type: --test-generic
			- service_type: unshared
				type: --test-generic
			- service_type: unshared2
				type: --test-generic
		capacitors:
		- id: unittest
			type: --test-static
			params:
				capacity: 42
				resources:
					# publish capacity for some known resources...
					- shared/things
					# ...and some nonexistent ones (these should be ignored by the scraper)
					- whatever/things
					- shared/items
		- id: unittest2
			type: --test-static
			params:
				capacity: 42
				resources:
					# same as above: some known...
					- unshared/capacity
					# ...and some unknown resources
					- someother/capacity
		resource_behavior:
			# overcommit should be reflected in capacity metrics
			- { resource: unshared2/capacity, overcommit_factor: 2.5 }
	`
)

func Test_ScanCapacity(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testScanCapacityConfigYAML),
	)
	test.ResetTime()

	c := Collector{
		Cluster:   s.Cluster,
		DB:        s.DB,
		LogError:  t.Errorf,
		TimeNow:   test.TimeNow,
		AddJitter: test.NoJitter,
	}
	job := c.CapacityScrapeJob(s.Registry)

	//cluster_services must be created as a baseline (this is usually done by the CheckConsistencyJob)
	for _, serviceType := range s.Cluster.ServiceTypesInAlphabeticalOrder() {
		err := s.DB.Insert(&db.ClusterService{Type: serviceType})
		mustT(t, err)
	}

	//check baseline
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.AssertEqualf(`
		INSERT INTO cluster_services (id, type) VALUES (1, 'shared');
		INSERT INTO cluster_services (id, type) VALUES (2, 'unshared');
		INSERT INTO cluster_services (id, type) VALUES (3, 'unshared2');
	`)

	//check that capacity records are created correctly (and that nonexistent
	//resources are ignored by the scraper)
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.CapacityPlugins)))
	tr.DBChanges().AssertEqualf(`
		INSERT INTO cluster_capacitors (capacitor_id, scraped_at, scrape_duration_secs, next_scrape_at) VALUES ('unittest', 1, 1, 901);
		INSERT INTO cluster_capacitors (capacitor_id, scraped_at, scrape_duration_secs, next_scrape_at) VALUES ('unittest2', 3, 1, 903);
		INSERT INTO cluster_resources (service_id, name, capacity, capacitor_id) VALUES (1, 'things', 42, 'unittest');
		INSERT INTO cluster_resources (service_id, name, capacity, capacitor_id) VALUES (2, 'capacity', 42, 'unittest2');
	`)

	//insert some crap records
	err := s.DB.Insert(&db.ClusterResource{
		ServiceID:   2,
		Name:        "unknown",
		RawCapacity: 100,
		CapacitorID: "unittest2",
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

	//next scan should throw out the crap records and recreate the deleted ones;
	//also change the reported Capacity to see if updates are getting through
	s.Cluster.CapacityPlugins["unittest"].(*plugins.StaticCapacityPlugin).Capacity = 23
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.CapacityPlugins)))
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = 5, next_scrape_at = 905 WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = 7, next_scrape_at = 907 WHERE capacitor_id = 'unittest2';
		UPDATE cluster_resources SET capacity = 23 WHERE service_id = 1 AND name = 'things';
	`)

	//move the clock forward by 300 seconds (the capacitor add step only triggers every five minutes)
	//TODO: I hate this clock
	for step := 1; step <= 300; step++ {
		_ = test.TimeNow()
	}

	//add a capacity plugin that reports subcapacities; check that subcapacities
	//are correctly written when creating a cluster_resources record
	pluginConfig := `
		id: unittest4
		type: --test-static
		params:
			capacity: 42
			resources: [ unshared/things ]
			with_subcapacities: true
	`
	subcapacityPlugin := s.AddCapacityPlugin(t, pluginConfig).(*plugins.StaticCapacityPlugin) //nolint:errcheck
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.CapacityPlugins)))
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = 309, next_scrape_at = 1209 WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = 311, next_scrape_at = 1211 WHERE capacitor_id = 'unittest2';
		INSERT INTO cluster_capacitors (capacitor_id, scraped_at, scrape_duration_secs, serialized_metrics, next_scrape_at) VALUES ('unittest4', 313, 1, '{"smaller_half":14,"larger_half":28}', 1213);
		INSERT INTO cluster_resources (service_id, name, capacity, subcapacities, capacitor_id) VALUES (2, 'things', 42, '[{"smaller_half":14},{"larger_half":28}]', 'unittest4');
	`)

	//check that scraping correctly updates subcapacities on an existing record
	subcapacityPlugin.Capacity = 10
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.CapacityPlugins)))
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = 315, next_scrape_at = 1215 WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = 317, next_scrape_at = 1217 WHERE capacitor_id = 'unittest2';
		UPDATE cluster_capacitors SET scraped_at = 319, serialized_metrics = '{"smaller_half":3,"larger_half":7}', next_scrape_at = 1219 WHERE capacitor_id = 'unittest4';
		UPDATE cluster_resources SET capacity = 10, subcapacities = '[{"smaller_half":3},{"larger_half":7}]' WHERE service_id = 2 AND name = 'things';
	`)

	//move the clock forward by 300 seconds (the capacitor add step only triggers every five minutes)
	//TODO: I hate this clock
	for step := 1; step <= 300; step++ {
		_ = test.TimeNow()
	}

	//add a capacity plugin that also reports capacity per availability zone; check that
	//these capacities are correctly written when creating a cluster_resources record
	pluginConfig = `
		id: unittest5
		type: --test-static
		params:
			capacity: 42
			resources: [ unshared2/things ]
			with_capacity_per_az: true
	`
	azCapacityPlugin := s.AddCapacityPlugin(t, pluginConfig).(*plugins.StaticCapacityPlugin) //nolint:errcheck
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.CapacityPlugins)))
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = 621, next_scrape_at = 1521 WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = 623, next_scrape_at = 1523 WHERE capacitor_id = 'unittest2';
		UPDATE cluster_capacitors SET scraped_at = 625, next_scrape_at = 1525 WHERE capacitor_id = 'unittest4';
		INSERT INTO cluster_capacitors (capacitor_id, scraped_at, scrape_duration_secs, next_scrape_at) VALUES ('unittest5', 627, 1, 1527);
		INSERT INTO cluster_resources (service_id, name, capacity, capacity_per_az, capacitor_id) VALUES (3, 'things', 42, '[{"name":"az-one","capacity":21,"usage":4},{"name":"az-two","capacity":21,"usage":4}]', 'unittest5');
	`)

	//check that scraping correctly updates the capacities on an existing record
	azCapacityPlugin.Capacity = 30
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.CapacityPlugins)))
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = 629, next_scrape_at = 1529 WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = 631, next_scrape_at = 1531 WHERE capacitor_id = 'unittest2';
		UPDATE cluster_capacitors SET scraped_at = 633, next_scrape_at = 1533 WHERE capacitor_id = 'unittest4';
		UPDATE cluster_capacitors SET scraped_at = 635, next_scrape_at = 1535 WHERE capacitor_id = 'unittest5';
		UPDATE cluster_resources SET capacity = 30, capacity_per_az = '[{"name":"az-one","capacity":15,"usage":3},{"name":"az-two","capacity":15,"usage":3}]' WHERE service_id = 3 AND name = 'things';
	`)

	//check data metrics generated for these capacity data
	registry := prometheus.NewPedanticRegistry()
	dmc := &DataMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(dmc)
	pmc := &CapacityPluginMetricsCollector{Cluster: s.Cluster, DB: s.DB}
	registry.MustRegister(pmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/capacity_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	//check that removing a capacitor removes its associated resources
	delete(s.Cluster.CapacityPlugins, "unittest5")
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.CapacityPlugins)+1)) //+1 to account for the deleted capacitor
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = 637, next_scrape_at = 1537 WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = 639, next_scrape_at = 1539 WHERE capacitor_id = 'unittest2';
		UPDATE cluster_capacitors SET scraped_at = 641, next_scrape_at = 1541 WHERE capacitor_id = 'unittest4';
		DELETE FROM cluster_capacitors WHERE capacitor_id = 'unittest5';
		DELETE FROM cluster_resources WHERE service_id = 3 AND name = 'things';
	`)
}

func setClusterCapacitorsStale(t *testing.T, s test.Setup) {
	//NOTE: This is built to not use `test.TimeNow()`, because using this function shifts the time around.
	//TODO: I hate this clock
	t.Helper()
	_, err := s.DB.Exec(`UPDATE cluster_capacitors SET next_scrape_at = (SELECT MAX(scraped_at) FROM cluster_capacitors)`)
	mustT(t, err)
}
