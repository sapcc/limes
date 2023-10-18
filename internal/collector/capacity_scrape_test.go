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
	"time"

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
		availability_zones: [ az-one, az-two ]
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

	c := getCollector(t, s)
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
		INSERT INTO cluster_capacitors (capacitor_id, scraped_at, scrape_duration_secs, next_scrape_at) VALUES ('unittest', 5, 5, 905);
		INSERT INTO cluster_capacitors (capacitor_id, scraped_at, scrape_duration_secs, next_scrape_at) VALUES ('unittest2', 10, 5, 910);
		INSERT INTO cluster_resources (id, capacitor_id, service_id, name, capacity) VALUES (1, 'unittest', 1, 'things', 42);
		INSERT INTO cluster_resources (id, capacitor_id, service_id, name, capacity) VALUES (2, 'unittest2', 2, 'capacity', 42);
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

	scrapedAt1 := s.Clock.Now().Add(-5 * time.Second)
	scrapedAt2 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest2';
		DELETE FROM cluster_resources WHERE id = 1 AND service_id = 1 AND name = 'things';
		INSERT INTO cluster_resources (id, capacitor_id, service_id, name, capacity) VALUES (4, 'unittest', 1, 'things', 23);
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
	)

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
	s.Clock.StepBy(5 * time.Minute) //to force a capacitor consistency check to run
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.CapacityPlugins)))

	scrapedAt1 = s.Clock.Now().Add(-10 * time.Second)
	scrapedAt2 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt4 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest2';
		INSERT INTO cluster_capacitors (capacitor_id, scraped_at, scrape_duration_secs, serialized_metrics, next_scrape_at) VALUES ('unittest4', %d, 5, '{"smaller_half":14,"larger_half":28}', %d);
		INSERT INTO cluster_resources (id, capacitor_id, service_id, name, capacity, subcapacities) VALUES (5, 'unittest4', 2, 'things', 42, '[{"az":"az-one","smaller_half":7},{"az":"az-one","larger_half":14},{"az":"az-two","smaller_half":7},{"az":"az-two","larger_half":14}]');
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
		scrapedAt4.Unix(), scrapedAt4.Add(15*time.Minute).Unix(),
	)

	//check that scraping correctly updates subcapacities on an existing record
	subcapacityPlugin.Capacity = 10
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.CapacityPlugins)))

	scrapedAt1 = s.Clock.Now().Add(-10 * time.Second)
	scrapedAt2 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt4 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest2';
		UPDATE cluster_capacitors SET scraped_at = %d, serialized_metrics = '{"smaller_half":3,"larger_half":7}', next_scrape_at = %d WHERE capacitor_id = 'unittest4';
		UPDATE cluster_resources SET capacity = 10, subcapacities = '[{"az":"az-one","smaller_half":1},{"az":"az-one","larger_half":4},{"az":"az-two","smaller_half":1},{"az":"az-two","larger_half":4}]' WHERE id = 5 AND service_id = 2 AND name = 'things';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
		scrapedAt4.Unix(), scrapedAt4.Add(15*time.Minute).Unix(),
	)

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
	s.Clock.StepBy(5 * time.Minute) //to force a capacitor consistency check to run
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.CapacityPlugins)))

	scrapedAt1 = s.Clock.Now().Add(-15 * time.Second)
	scrapedAt2 = s.Clock.Now().Add(-10 * time.Second)
	scrapedAt4 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt5 := s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest2';
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest4';
		INSERT INTO cluster_capacitors (capacitor_id, scraped_at, scrape_duration_secs, next_scrape_at) VALUES ('unittest5', %d, 5, %d);
		INSERT INTO cluster_resources (id, capacitor_id, service_id, name, capacity, capacity_per_az) VALUES (6, 'unittest5', 3, 'things', 42, '[{"name":"az-one","capacity":21,"usage":4},{"name":"az-two","capacity":21,"usage":4}]');
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
		scrapedAt4.Unix(), scrapedAt4.Add(15*time.Minute).Unix(),
		scrapedAt5.Unix(), scrapedAt5.Add(15*time.Minute).Unix(),
	)

	//check that scraping correctly updates the capacities on an existing record
	azCapacityPlugin.Capacity = 30
	setClusterCapacitorsStale(t, s)
	mustT(t, jobloop.ProcessMany(job, s.Ctx, len(s.Cluster.CapacityPlugins)))

	scrapedAt1 = s.Clock.Now().Add(-15 * time.Second)
	scrapedAt2 = s.Clock.Now().Add(-10 * time.Second)
	scrapedAt4 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt5 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest2';
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest4';
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest5';
		UPDATE cluster_resources SET capacity = 30, capacity_per_az = '[{"name":"az-one","capacity":15,"usage":3},{"name":"az-two","capacity":15,"usage":3}]' WHERE id = 6 AND service_id = 3 AND name = 'things';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
		scrapedAt4.Unix(), scrapedAt4.Add(15*time.Minute).Unix(),
		scrapedAt5.Unix(), scrapedAt5.Add(15*time.Minute).Unix(),
	)

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

	scrapedAt1 = s.Clock.Now().Add(-10 * time.Second)
	scrapedAt2 = s.Clock.Now().Add(-5 * time.Second)
	scrapedAt4 = s.Clock.Now()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest2';
		UPDATE cluster_capacitors SET scraped_at = %d, next_scrape_at = %d WHERE capacitor_id = 'unittest4';
		DELETE FROM cluster_capacitors WHERE capacitor_id = 'unittest5';
		DELETE FROM cluster_resources WHERE id = 6 AND service_id = 3 AND name = 'things';
	`,
		scrapedAt1.Unix(), scrapedAt1.Add(15*time.Minute).Unix(),
		scrapedAt2.Unix(), scrapedAt2.Add(15*time.Minute).Unix(),
		scrapedAt4.Unix(), scrapedAt4.Add(15*time.Minute).Unix(),
	)
}

func setClusterCapacitorsStale(t *testing.T, s test.Setup) {
	//NOTE: This is built to not use `test.TimeNow()`, because using this function shifts the time around.
	t.Helper()
	_, err := s.DB.Exec(`UPDATE cluster_capacitors SET next_scrape_at = $1`, s.Clock.Now())
	mustT(t, err)
}
