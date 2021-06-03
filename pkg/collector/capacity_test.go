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
	"math"
	"regexp"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/test"
)

func Test_ScanCapacity(t *testing.T) {
	test.ResetTime()
	test.InitDatabase(t, nil)

	cluster := &core.Cluster{
		ID:              "west",
		IsServiceShared: map[string]bool{"shared": true},
		ServiceTypes:    []string{"shared", "unshared", "unshared2"},
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
		Config: &core.ClusterConfiguration{
			Auth: &core.AuthParameters{},
			//overcommit should be reflected in capacity metrics
			ResourceBehaviors: []*core.ResourceBehaviorConfiguration{{
				Compiled: core.ResourceBehavior{
					FullResourceNameRx: regexp.MustCompile("^unshared2/capacity$"),
					OvercommitFactor:   2.5,
					MaxBurstMultiplier: limes.BurstingMultiplier(math.Inf(+1)),
				},
			}},
		},
	}

	c := Collector{
		Cluster:  cluster,
		Plugin:   nil,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
	}

	//check that capacity records are created correctly (and that nonexistent
	//resources are ignored by the scraper)
	c.scanCapacity()
	test.AssertDBContent(t, "fixtures/scancapacity1.sql")

	//insert some crap records
	err := db.DB.Insert(&db.ClusterResource{
		ServiceID:   2,
		Name:        "unknown",
		RawCapacity: 100,
	})
	if err != nil {
		t.Error(err)
	}
	_, err = db.DB.Exec(
		`DELETE FROM cluster_resources WHERE service_id = $1 AND name = $2`,
		1, "things",
	)
	if err != nil {
		t.Error(err)
	}
	test.AssertDBContent(t, "fixtures/scancapacity2.sql")

	//next scan should throw out the crap records and recreate the deleted ones;
	//also change the reported Capacity to see if updates are getting through
	cluster.CapacityPlugins["unittest"].(*test.CapacityPlugin).Capacity = 23
	c.scanCapacity()
	test.AssertDBContent(t, "fixtures/scancapacity3.sql")

	//add a capacity plugin that reports subcapacities; check that subcapacities
	//are correctly written when creating a cluster_resources record
	subcapacityPlugin := test.NewCapacityPlugin("unittest4", "unshared/things")
	subcapacityPlugin.WithSubcapacities = true
	cluster.CapacityPlugins["unittest4"] = subcapacityPlugin
	c.scanCapacity()
	test.AssertDBContent(t, "fixtures/scancapacity5.sql")

	//check that scraping correctly updates subcapacities on an existing record
	subcapacityPlugin.Capacity = 10
	c.scanCapacity()
	test.AssertDBContent(t, "fixtures/scancapacity6.sql")

	//add a capacity plugin that also reports capacity per availability zone; check that
	//these capacities are correctly written when creating a cluster_resources record
	azCapacityPlugin := test.NewCapacityPlugin("unittest5", "unshared2/things")
	azCapacityPlugin.WithAZCapData = true
	cluster.CapacityPlugins["unittest5"] = azCapacityPlugin
	c.scanCapacity()
	test.AssertDBContent(t, "fixtures/scancapacity7.sql")

	//check that scraping correctly updates the capacities on an existing record
	azCapacityPlugin.Capacity = 30
	c.scanCapacity()
	test.AssertDBContent(t, "fixtures/scancapacity8.sql")

	//check data metrics generated for these capacity data
	registry := prometheus.NewPedanticRegistry()
	dmc := &DataMetricsCollector{Cluster: cluster}
	registry.MustRegister(dmc)
	pmc := &CapacityPluginMetricsCollector{Cluster: cluster}
	registry.MustRegister(pmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/capacity_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
}
