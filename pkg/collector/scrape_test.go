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
	"fmt"
	"math/big"
	"regexp"
	"sort"
	"testing"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/test"
)

func p2u64(x uint64) *uint64 {
	return &x
}

func p2i64(x int64) *int64 {
	return &x
}

func prepareScrapeTest(t *testing.T, numProjects int, quotaPlugins ...core.QuotaPlugin) *core.Cluster {
	test.ResetTime()
	test.InitDatabase(t, nil)

	cluster := &core.Cluster{
		ID:              "west",
		DiscoveryPlugin: test.NewDiscoveryPlugin(),
		QuotaPlugins:    map[string]core.QuotaPlugin{},
		CapacityPlugins: map[string]core.CapacityPlugin{},
		Config:          &core.ClusterConfiguration{Auth: &core.AuthParameters{}},
	}
	for _, plugin := range quotaPlugins {
		info := plugin.ServiceInfo()
		cluster.ServiceTypes = append(cluster.ServiceTypes, info.Type)
		cluster.QuotaPlugins[info.Type] = plugin
	}
	sort.Strings(cluster.ServiceTypes)

	//one domain is enough; one or two projects is enough
	discovery := cluster.DiscoveryPlugin.(*test.DiscoveryPlugin)
	domain1 := discovery.StaticDomains[0]
	project1 := discovery.StaticProjects[domain1.UUID][0]
	project2 := discovery.StaticProjects[domain1.UUID][1]

	discovery.StaticDomains = discovery.StaticDomains[0:1]
	discovery.StaticProjects = map[string][]core.KeystoneProject{
		domain1.UUID: discovery.StaticProjects[domain1.UUID][0:numProjects],
	}

	//ScanDomains is required to create the entries in `domains`,
	//`domain_services`, `projects` and `project_services`
	_, err := ScanDomains(cluster, ScanDomainsOpts{})
	if err != nil {
		t.Fatal(err)
	}

	//if we have two projects, we are going to test with and without bursting, so
	//set up bursting for one of both projects
	if numProjects == 2 {
		_, err := db.DB.Exec(`UPDATE projects SET has_bursting = TRUE WHERE id = 2`)
		if err != nil {
			t.Fatal(err)
		}
		cluster.Config.Bursting.MaxMultiplier = 0.2
	}

	//setup a quota constraint for the project that we're scraping (this is only used by Test_ScrapeSuccess())
	//
	//NOTE: This is set only *after* ScanDomains has run, in order to exercise
	//the code path in Scrape() that applies constraints when first creating
	//project_resources entries. If we had set this before ScanDomains, then
	//ScanDomains would already have created the project_resources entries.
	projectConstraints := core.QuotaConstraints{
		"unittest": {
			"capacity": {Minimum: p2u64(10), Maximum: p2u64(40)},
		},
	}
	cluster.QuotaConstraints = &core.QuotaConstraintSet{
		Projects: map[string]map[string]core.QuotaConstraints{
			domain1.Name: {
				project1.Name: projectConstraints,
				project2.Name: projectConstraints,
			},
		},
	}

	return cluster
}

func Test_ScrapeSuccess(t *testing.T) {
	plugin := test.NewPlugin("unittest")
	cluster := prepareScrapeTest(t, 2, plugin)
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
	c.Scrape() //twice because there are two projects
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
	c.Scrape() //twice because there are two projects
	test.AssertDBContent(t, "fixtures/scrape2.sql")

	//set some new quota values (note that "capacity" already had a non-zero
	//quota because of the cluster.QuotaConstraints)
	_, err := db.DB.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 20, "capacity")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.DB.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 13, "things")
	if err != nil {
		t.Fatal(err)
	}

	//Scrape should try to enforce quota values in the backend (this did not work
	//until now because the test.Plugin was instructed to have SetQuota fail)
	plugin.SetQuotaFails = false
	setProjectServicesStale(t)
	c.Scrape()
	c.Scrape() //twice because there are two projects
	test.AssertDBContent(t, "fixtures/scrape3.sql")

	//another Scrape (with SetQuota disabled again) should show that the quota
	//update was durable
	plugin.SetQuotaFails = true
	setProjectServicesStale(t)
	c.Scrape() //twice because there are two projects
	c.Scrape()
	test.AssertDBContent(t, "fixtures/scrape4.sql") //same as scrape3.sql except for scraped_at timestamp

	//set a quota that contradicts the cluster.QuotaConstraints
	_, err = db.DB.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 50, "capacity")
	if err != nil {
		t.Fatal(err)
	}

	//Scrape should apply the constraint, then enforce quota values in the backend
	plugin.SetQuotaFails = false
	setProjectServicesStale(t)
	c.Scrape()
	c.Scrape() //twice because there are two projects
	test.AssertDBContent(t, "fixtures/scrape5.sql")

	//add an externally-managed resource, scrape it twice (first time adds the
	//project_resources entry, second time updates it)
	plugin.WithExternallyManagedResource = true
	setProjectServicesStale(t)
	c.Scrape()
	c.Scrape() //twice because there are two projects
	test.AssertDBContent(t, "fixtures/scrape6.sql")

	plugin.StaticResourceData["external_things"].Quota = 10
	setProjectServicesStale(t)
	c.Scrape()
	c.Scrape() //twice because there are two projects
	test.AssertDBContent(t, "fixtures/scrape7.sql")

	//check that setting the quota of an externally-managed resource on our side
	//is pointless
	_, err = db.DB.Exec(`UPDATE project_resources SET quota = 42 WHERE name = $1`, "external_things")
	if err != nil {
		t.Fatal(err)
	}
	setProjectServicesStale(t)
	c.Scrape() //twice because there are two projects
	c.Scrape()
	test.AssertDBContent(t, "fixtures/scrape8.sql") //identical to scrape7.sql except for timestamps

	//set "capacity" to a non-zero usage to observe a non-zero usage on
	//"capacity_portion" (otherwise this resource has been all zeroes this entire
	//time)
	plugin.StaticResourceData["capacity"].Usage = 20
	setProjectServicesStale(t)
	c.Scrape()
	c.Scrape() //twice because there are two projects
	test.AssertDBContent(t, "fixtures/scrape9.sql")

	//check data metrics generated by this scraping pass
	registry := prometheus.NewPedanticRegistry()
	amc := &AggregateMetricsCollector{Cluster: cluster}
	registry.MustRegister(amc)
	pmc := &QuotaPluginMetricsCollector{Cluster: cluster}
	registry.MustRegister(pmc)
	dmc := &DataMetricsCollector{Cluster: cluster, ReportZeroes: true}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/scrape_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	//check data metrics with the skip_zero flag set
	registry = prometheus.NewPedanticRegistry()
	amc = &AggregateMetricsCollector{Cluster: cluster}
	registry.MustRegister(amc)
	pmc = &QuotaPluginMetricsCollector{Cluster: cluster}
	registry.MustRegister(pmc)
	dmc = &DataMetricsCollector{Cluster: cluster, ReportZeroes: false}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/scrape_metrics_skipzero.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
}

func setProjectServicesStale(t *testing.T) {
	t.Helper()
	//make sure that the project is scraped again
	_, err := db.DB.Exec(`UPDATE project_services SET stale = $1`, true)
	if err != nil {
		t.Fatal(err)
	}
}

func Test_ScrapeFailure(t *testing.T) {
	plugin := test.NewPlugin("unittest")
	cluster := prepareScrapeTest(t, 2, plugin)
	c := Collector{
		Cluster: cluster,
		Plugin:  plugin,
		TimeNow: test.TimeNow,
		Once:    true,
	}
	//we will see an expected ERROR during testing, do not make the test fail because of this
	expectedErrorRx := regexp.MustCompile(`^scrape unittest resources for germany/(berlin|dresden) failed: Scrape failed as requested$`)
	c.LogError = func(msg string, args ...interface{}) {
		msg = fmt.Sprintf(msg, args...)
		if expectedErrorRx.MatchString(msg) {
			logg.Info(msg)
		} else {
			t.Error(msg)
		}
	}

	//check that ScanDomains created the domain, project and their services
	test.AssertDBContent(t, "fixtures/scrape0.sql")

	//failing Scrape should create dummy records to ensure that the API finds
	//plausibly-structured data
	plugin.ScrapeFails = true
	c.Scrape()
	c.Scrape() //twice because there are two projects
	test.AssertDBContent(t, "fixtures/scrape-failures1.sql")

	//next Scrape should yield the same result
	c.Scrape()
	c.Scrape() //twice because there are two projects
	test.AssertDBContent(t, "fixtures/scrape-failures1.sql")

	//once the backend starts working, we start to see plausible data again
	plugin.ScrapeFails = false
	setProjectServicesStale(t)
	c.Scrape()
	c.Scrape() //twice because there are two projects
	test.AssertDBContent(t, "fixtures/scrape-failures2.sql")

	//backend fails again and we need to scrape because of the stale flag ->
	//touch neither scraped_at nor the existing resources
	plugin.ScrapeFails = true
	setProjectServicesStale(t)
	c.Scrape()
	c.Scrape() //twice because there are two projects
	test.AssertDBContent(t, "fixtures/scrape-failures3.sql")
}

////////////////////////////////////////////////////////////////////////////////
// test for auto-approval

type autoApprovalTestPlugin struct {
	StaticBackendQuota uint64
}

func (p *autoApprovalTestPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
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
			Name:                    "approve",
			AutoApproveInitialQuota: p.StaticBackendQuota,
		},
		{
			Name:                    "noapprove",
			AutoApproveInitialQuota: p.StaticBackendQuota,
		},
	}
}

func (p *autoApprovalTestPlugin) Rates() []limes.RateInfo {
	return nil
}
func (p *autoApprovalTestPlugin) ScrapeRates(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, prevSerializedState string) (map[string]*big.Int, string, error) {
	return nil, "", nil
}
func (p *autoApprovalTestPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
}
func (p *autoApprovalTestPlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID string, project core.KeystoneProject, serializedMetrics string) error {
	return nil
}

func (p *autoApprovalTestPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject) (map[string]core.ResourceData, string, error) {
	return map[string]core.ResourceData{
		"approve":   {Usage: 0, Quota: int64(p.StaticBackendQuota)},
		"noapprove": {Usage: 0, Quota: int64(p.StaticBackendQuota) + 10},
	}, "", nil
}

func (p *autoApprovalTestPlugin) IsQuotaAcceptableForProject(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	return errors.New("unimplemented")
}

func (p *autoApprovalTestPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	return errors.New("unimplemented")
}

func Test_AutoApproveInitialQuota(t *testing.T) {
	plugin := &autoApprovalTestPlugin{StaticBackendQuota: 10}
	cluster := prepareScrapeTest(t, 1, plugin)
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

//A quota plugin with absolutely no resources and rates.
type noopQuotaPlugin struct{}

func (noopQuotaPlugin) Init(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}
func (noopQuotaPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{Type: "noop"}
}
func (noopQuotaPlugin) Resources() []limes.ResourceInfo {
	return nil
}
func (noopQuotaPlugin) Scrape(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject) (map[string]core.ResourceData, string, error) {
	return nil, "", nil
}
func (noopQuotaPlugin) IsQuotaAcceptableForProject(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	return nil
}
func (noopQuotaPlugin) SetQuota(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	return nil
}
func (noopQuotaPlugin) Rates() []limes.RateInfo {
	return nil
}
func (noopQuotaPlugin) ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}
func (noopQuotaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
}
func (noopQuotaPlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID string, project core.KeystoneProject, serializedMetrics string) error {
	return nil
}

func Test_ScrapeButNoResources(t *testing.T) {
	plugin := noopQuotaPlugin{}
	cluster := prepareScrapeTest(t, 1, plugin)
	c := Collector{
		Cluster:  cluster,
		Plugin:   plugin,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}

	//check that Scrape() behaves properly when encountering a quota plugin with
	//no Resources() (in the wild, this can happen because some quota plugins
	//only have Rates())
	c.Scrape()
	test.AssertDBContent(t, "fixtures/scrape-no-resources.sql")
}
