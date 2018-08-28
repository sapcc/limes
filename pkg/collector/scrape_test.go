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
	"errors"
	"sort"
	"testing"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/test"
)

func p2u64(x uint64) *uint64 {
	return &x
}

func prepareScrapeTest(t *testing.T, quotaPlugins ...limes.QuotaPlugin) *limes.Cluster {
	test.ResetTime()
	test.InitDatabase(t)

	cluster := &limes.Cluster{
		ID:              "west",
		IsServiceShared: map[string]bool{},
		DiscoveryPlugin: test.NewDiscoveryPlugin(),
		QuotaPlugins:    map[string]limes.QuotaPlugin{},
		CapacityPlugins: map[string]limes.CapacityPlugin{},
		Config:          &limes.ClusterConfiguration{Auth: &limes.AuthParameters{}},
	}
	for _, plugin := range quotaPlugins {
		info := plugin.ServiceInfo()
		cluster.ServiceTypes = append(cluster.ServiceTypes, info.Type)
		cluster.QuotaPlugins[info.Type] = plugin
	}
	sort.Strings(cluster.ServiceTypes)

	//one domain and one project is enough
	discovery := cluster.DiscoveryPlugin.(*test.DiscoveryPlugin)
	domain1 := discovery.StaticDomains[0]
	project1 := discovery.StaticProjects[domain1.UUID][0]

	discovery.StaticDomains = discovery.StaticDomains[0:1]
	discovery.StaticProjects = map[string][]limes.KeystoneProject{
		domain1.UUID: discovery.StaticProjects[domain1.UUID][0:1],
	}

	//ScanDomains is required to create the entries in `domains`, `domain_services`
	_, err := ScanDomains(cluster, ScanDomainsOpts{})
	if err != nil {
		t.Fatal(err)
	}

	//setup a quota constraint for the project that we're scraping (this is only used by Test_Scrape())
	//
	//NOTE: This is set only *after* ScanDomains has run, in order to exercise
	//the code path in Scrape() that applies constraints when first creating
	//project_resources entries. If we had set this before ScanDomains, then
	//ScanDomains would already have created the project_resources entries.
	cluster.QuotaConstraints = &limes.QuotaConstraintSet{
		Projects: map[string]map[string]limes.QuotaConstraints{
			domain1.Name: {
				project1.Name: {
					"unittest": {
						"capacity": {Minimum: p2u64(10), Maximum: p2u64(40)},
					},
				},
			},
		},
	}

	return cluster
}

func Test_Scrape(t *testing.T) {
	plugin := test.NewPlugin("unittest")
	cluster := prepareScrapeTest(t, plugin)
	cluster.Authoritative = true
	c := Collector{
		Cluster:  cluster,
		Plugin:   plugin,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}

	//check that ScanDomains created the domain, project and their services
	test.AssertDBContent(t, "fixtures/scrape0.sql")

	//first Scrape should create the entries in `project_resources` with the
	//correct usage and backend quota values (and quota = 0 because nothing was approved yet)
	//and set `project_services.scraped_at` to the current time
	plugin.SetQuotaFails = true
	c.Scrape()
	test.AssertDBContent(t, "fixtures/scrape1.sql")

	//second Scrape should not change anything (not even the timestamps) since
	//less than 30 minutes have passed since the last Scrape()
	c.Scrape()
	test.AssertDBContent(t, "fixtures/scrape1.sql")

	//change the data that is reported by the plugin
	plugin.StaticResourceData["capacity"].Quota = 110
	plugin.StaticResourceData["things"].Usage = 5
	setProjectServicesStale(t)
	//Scrape should pick up the changed resource data
	c.Scrape()
	test.AssertDBContent(t, "fixtures/scrape2.sql")

	//set some new quota values (note that "capacity" already had a non-zero
	//quota because of the cluster.QuotaConstraints)
	_, err := db.DB.Exec(`UPDATE project_resources SET quota = ? WHERE name = ?`, 20, "capacity")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB.Exec(`UPDATE project_resources SET quota = ? WHERE name = ?`, 13, "things")
	if err != nil {
		t.Fatal(err)
	}

	//Scrape should try to enforce quota values in the backend (this did not work
	//until now because the test.Plugin was instructed to have SetQuota fail)
	plugin.SetQuotaFails = false
	setProjectServicesStale(t)
	c.Scrape()
	test.AssertDBContent(t, "fixtures/scrape3.sql")

	//another Scrape (with SetQuota disabled again) should show that the quota
	//update was durable
	plugin.SetQuotaFails = true
	setProjectServicesStale(t)
	c.Scrape()
	test.AssertDBContent(t, "fixtures/scrape4.sql") //same as scrape3.sql except for scraped_at timestamp

	//set a quota that contradicts the cluster.QuotaConstraints
	_, err = db.DB.Exec(`UPDATE project_resources SET quota = ? WHERE name = ?`, 50, "capacity")
	if err != nil {
		t.Fatal(err)
	}

	//Scrape should apply the constraint, then enforce quota values in the backend
	plugin.SetQuotaFails = false
	setProjectServicesStale(t)
	c.Scrape()
	test.AssertDBContent(t, "fixtures/scrape5.sql")

	//check data metrics generated by this scraping pass
	registry := prometheus.NewPedanticRegistry()
	dmc := &DataMetricsCollector{Cluster: cluster}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/scrape_metrics.json"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
}

func setProjectServicesStale(t *testing.T) {
	//make sure that the project is scraped again
	_, err := db.DB.Exec(`UPDATE project_services SET stale = ?`, true)
	if err != nil {
		t.Fatal(err)
	}
}

////////////////////////////////////////////////////////////////////////////////
// test for auto-approval

type autoApprovalTestPlugin struct {
	StaticBackendQuota uint64
}

func (p *autoApprovalTestPlugin) Init(provider *gophercloud.ProviderClient) error {
	return nil
}

func (p *autoApprovalTestPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type: "autoapprovaltest",
	}
}

func (p *autoApprovalTestPlugin) Resources() []limes.ResourceInfo {
	//one resource can auto-approve, one cannot because BackendQuota != AutoApproveInitialQuota
	return []limes.ResourceInfo{
		{
			Name: "approve",
			AutoApproveInitialQuota: p.StaticBackendQuota,
		},
		{
			Name: "noapprove",
			AutoApproveInitialQuota: p.StaticBackendQuota,
		},
	}
}

func (p *autoApprovalTestPlugin) Scrape(provider *gophercloud.ProviderClient, clusterID, domainUUID, projectUUID string) (map[string]limes.ResourceData, error) {
	return map[string]limes.ResourceData{
		"approve":   {Usage: 0, Quota: int64(p.StaticBackendQuota)},
		"noapprove": {Usage: 0, Quota: int64(p.StaticBackendQuota) + 10},
	}, nil
}

func (p *autoApprovalTestPlugin) SetQuota(provider *gophercloud.ProviderClient, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	return errors.New("unimplemented")
}

func Test_AutoApproveInitialQuota(t *testing.T) {
	plugin := &autoApprovalTestPlugin{StaticBackendQuota: 10}
	cluster := prepareScrapeTest(t, plugin)
	c := Collector{
		Cluster:  cluster,
		Plugin:   plugin,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}

	//when first scraping, the initial backend quota of the "approve" resource
	//shall be approved automatically
	c.Scrape()
	test.AssertDBContent(t, "fixtures/scrape-autoapprove1.sql")

	//modify the backend quota; verify that the second scrape does not
	//auto-approve the changed value again (auto-approval is limited to the
	//initial scrape)
	plugin.StaticBackendQuota += 10
	setProjectServicesStale(t)
	c.Scrape()
	test.AssertDBContent(t, "fixtures/scrape-autoapprove2.sql")
}
