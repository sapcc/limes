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

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

func Test_ScanCapacity(t *testing.T) {
	test.ResetTime()
	dbm := test.InitDatabase(t, nil)

	cluster := &core.Cluster{
		QuotaPlugins: map[string]core.QuotaPlugin{
			"shared":    test.NewPlugin("shared"),
			"unshared":  test.NewPlugin("unshared"),
			"unshared2": test.NewPlugin("unshared2"),
		},
		CapacityPlugins: map[string]core.CapacityPlugin{
			"unittest": test.NewCapacityPlugin("unittest",
				//publish capacity for some known resources...
				"shared/things",
				//...and some nonexistent ones (these should be ignored by the scraper)
				"whatever/things", "shared/items",
			),
			"unittest2": test.NewCapacityPlugin("unittest2",
				//same as above: some known...
				"unshared/capacity",
				//...and some unknown resources
				"someother/capacity",
			),
		},
		Config: core.ClusterConfiguration{
			//overcommit should be reflected in capacity metrics
			ResourceBehaviors: []core.ResourceBehavior{{
				FullResourceNameRx: "unshared2/capacity",
				OvercommitFactor:   2.5,
			}},
		},
	}

	c := Collector{
		Cluster:   cluster,
		DB:        dbm,
		Plugin:    nil,
		LogError:  t.Errorf,
		TimeNow:   test.TimeNow,
		AddJitter: test.NoJitter,
	}

	//check baseline
	tr, tr0 := easypg.NewTracker(t, dbm.Db)
	tr0.AssertEmpty()

	//check that capacity records are created correctly (and that nonexistent
	//resources are ignored by the scraper)
	c.scanCapacity()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO cluster_capacitors (capacitor_id, scraped_at, scrape_duration_secs) VALUES ('unittest', 0, 1);
		INSERT INTO cluster_capacitors (capacitor_id, scraped_at, scrape_duration_secs) VALUES ('unittest2', 0, 1);
		INSERT INTO cluster_resources (service_id, name, capacity) VALUES (1, 'things', 42);
		INSERT INTO cluster_resources (service_id, name, capacity) VALUES (2, 'capacity', 42);
		INSERT INTO cluster_services (id, type, scraped_at) VALUES (1, 'shared', 0);
		INSERT INTO cluster_services (id, type, scraped_at) VALUES (2, 'unshared', 0);
	`)

	//insert some crap records
	err := dbm.Insert(&db.ClusterResource{
		ServiceID:   2,
		Name:        "unknown",
		RawCapacity: 100,
	})
	if err != nil {
		t.Error(err)
	}
	_, err = dbm.Exec(
		`DELETE FROM cluster_resources WHERE service_id = $1 AND name = $2`,
		1, "things",
	)
	if err != nil {
		t.Error(err)
	}

	//next scan should throw out the crap records and recreate the deleted ones;
	//also change the reported Capacity to see if updates are getting through
	cluster.CapacityPlugins["unittest"].(*test.CapacityPlugin).Capacity = 23
	c.scanCapacity()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = 5 WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = 5 WHERE capacitor_id = 'unittest2';
		UPDATE cluster_resources SET capacity = 23 WHERE service_id = 1 AND name = 'things';
		UPDATE cluster_services SET scraped_at = 5 WHERE id = 1 AND type = 'shared';
		UPDATE cluster_services SET scraped_at = 5 WHERE id = 2 AND type = 'unshared';
	`)

	//add a capacity plugin that reports subcapacities; check that subcapacities
	//are correctly written when creating a cluster_resources record
	subcapacityPlugin := test.NewCapacityPlugin("unittest4", "unshared/things")
	subcapacityPlugin.WithSubcapacities = true
	cluster.CapacityPlugins["unittest4"] = subcapacityPlugin
	c.scanCapacity()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = 10 WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = 10 WHERE capacitor_id = 'unittest2';
		INSERT INTO cluster_capacitors (capacitor_id, scraped_at, scrape_duration_secs, serialized_metrics) VALUES ('unittest4', 10, 1, '{"smaller_half":14,"larger_half":28}');
		INSERT INTO cluster_resources (service_id, name, capacity, subcapacities) VALUES (2, 'things', 42, '[{"smaller_half":14},{"larger_half":28}]');
		UPDATE cluster_services SET scraped_at = 10 WHERE id = 1 AND type = 'shared';
		UPDATE cluster_services SET scraped_at = 10 WHERE id = 2 AND type = 'unshared';
	`)

	//check that scraping correctly updates subcapacities on an existing record
	subcapacityPlugin.Capacity = 10
	c.scanCapacity()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = 17 WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = 17 WHERE capacitor_id = 'unittest2';
		UPDATE cluster_capacitors SET scraped_at = 17, serialized_metrics = '{"smaller_half":3,"larger_half":7}' WHERE capacitor_id = 'unittest4';
		UPDATE cluster_resources SET capacity = 10, subcapacities = '[{"smaller_half":3},{"larger_half":7}]' WHERE service_id = 2 AND name = 'things';
		UPDATE cluster_services SET scraped_at = 17 WHERE id = 1 AND type = 'shared';
		UPDATE cluster_services SET scraped_at = 17 WHERE id = 2 AND type = 'unshared';
	`)

	//add a capacity plugin that also reports capacity per availability zone; check that
	//these capacities are correctly written when creating a cluster_resources record
	azCapacityPlugin := test.NewCapacityPlugin("unittest5", "unshared2/things")
	azCapacityPlugin.WithAZCapData = true
	cluster.CapacityPlugins["unittest5"] = azCapacityPlugin
	c.scanCapacity()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = 24 WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = 24 WHERE capacitor_id = 'unittest2';
		UPDATE cluster_capacitors SET scraped_at = 24 WHERE capacitor_id = 'unittest4';
		INSERT INTO cluster_capacitors (capacitor_id, scraped_at, scrape_duration_secs) VALUES ('unittest5', 24, 1);
		INSERT INTO cluster_resources (service_id, name, capacity, capacity_per_az) VALUES (3, 'things', 42, '[{"name":"az-one","capacity":21,"usage":4},{"name":"az-two","capacity":21,"usage":4}]');
		UPDATE cluster_services SET scraped_at = 24 WHERE id = 1 AND type = 'shared';
		UPDATE cluster_services SET scraped_at = 24 WHERE id = 2 AND type = 'unshared';
		INSERT INTO cluster_services (id, type, scraped_at) VALUES (3, 'unshared2', 24);
	`)

	//check that scraping correctly updates the capacities on an existing record
	azCapacityPlugin.Capacity = 30
	c.scanCapacity()
	tr.DBChanges().AssertEqualf(`
		UPDATE cluster_capacitors SET scraped_at = 33 WHERE capacitor_id = 'unittest';
		UPDATE cluster_capacitors SET scraped_at = 33 WHERE capacitor_id = 'unittest2';
		UPDATE cluster_capacitors SET scraped_at = 33 WHERE capacitor_id = 'unittest4';
		UPDATE cluster_capacitors SET scraped_at = 33 WHERE capacitor_id = 'unittest5';
		UPDATE cluster_resources SET capacity = 30, capacity_per_az = '[{"name":"az-one","capacity":15,"usage":3},{"name":"az-two","capacity":15,"usage":3}]' WHERE service_id = 3 AND name = 'things';
		UPDATE cluster_services SET scraped_at = 33 WHERE id = 1 AND type = 'shared';
		UPDATE cluster_services SET scraped_at = 33 WHERE id = 2 AND type = 'unshared';
		UPDATE cluster_services SET scraped_at = 33 WHERE id = 3 AND type = 'unshared2';
	`)

	//check data metrics generated for these capacity data
	registry := prometheus.NewPedanticRegistry()
	dmc := &DataMetricsCollector{Cluster: cluster, DB: dbm}
	registry.MustRegister(dmc)
	pmc := &CapacityPluginMetricsCollector{Cluster: cluster, DB: dbm}
	registry.MustRegister(pmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/capacity_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
}
