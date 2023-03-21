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
	"testing"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/test"
)

func p2u64(x uint64) *uint64 {
	return &x
}

func prepareScrapeTest(t *testing.T, numProjects int, quotaPlugins ...core.QuotaPlugin) (*core.Cluster, *gorp.DbMap) {
	test.ResetTime()
	dbm := test.InitDatabase(t, nil)

	cluster := &core.Cluster{
		DiscoveryPlugin: test.NewDiscoveryPlugin(),
		QuotaPlugins:    map[string]core.QuotaPlugin{},
		CapacityPlugins: map[string]core.CapacityPlugin{},
		Config:          core.ClusterConfiguration{},
	}
	for _, plugin := range quotaPlugins {
		info := plugin.ServiceInfo()
		cluster.QuotaPlugins[info.Type] = plugin
	}

	//one domain is enough; one or two projects is enough
	discovery := cluster.DiscoveryPlugin.(*test.DiscoveryPlugin) //nolint:errcheck
	domain1 := discovery.StaticDomains[0]
	project1 := discovery.StaticProjects[domain1.UUID][0]
	project2 := discovery.StaticProjects[domain1.UUID][1]

	discovery.StaticDomains = discovery.StaticDomains[0:1]
	discovery.StaticProjects = map[string][]core.KeystoneProject{
		domain1.UUID: discovery.StaticProjects[domain1.UUID][0:numProjects],
	}

	//if there is "centralized" service type, operate it under centralized quota
	//distribution (only used by Test_ScrapeCentralized())
	cluster.Config.QuotaDistributionConfigs = []*core.QuotaDistributionConfiguration{
		{
			FullResourceNameRx:  "centralized/capacity",
			Model:               limesresources.CentralizedQuotaDistribution,
			DefaultProjectQuota: 10,
		},
		{
			FullResourceNameRx:  "centralized/things",
			Model:               limesresources.CentralizedQuotaDistribution,
			DefaultProjectQuota: 15,
		},
	}

	//ScanDomains is required to create the entries in `domains`,
	//`domain_services`, `projects` and `project_services`
	timeZero := func() time.Time { return time.Unix(0, 0).UTC() }
	_, err := (&Collector{Cluster: cluster, DB: dbm, TimeNow: timeZero, AddJitter: test.NoJitter}).ScanDomains(ScanDomainsOpts{})
	if err != nil {
		t.Fatal(err)
	}

	//if we have two projects, we are going to test with and without bursting, so
	//set up bursting for one of both projects
	if numProjects == 2 {
		_, err := dbm.Exec(`UPDATE projects SET has_bursting = TRUE WHERE id = 2`)
		if err != nil {
			t.Fatal(err)
		}
		cluster.Config.Bursting.MaxMultiplier = 0.2
	}

	//setup a quota constraint for the project that we're scraping (this is ignored by Test_ScrapeFailure())
	//
	//NOTE: This is set only *after* ScanDomains has run, in order to exercise
	//the code path in Scrape() that applies constraints when first creating
	//project_resources entries. If we had set this before ScanDomains, then
	//ScanDomains would already have created the project_resources entries.
	projectConstraints := core.QuotaConstraints{
		"unittest": {
			"capacity": {Minimum: p2u64(10), Maximum: p2u64(40)},
		},
		// only used by Test_ScrapeCentralized()
		"centralized": {
			"capacity": {Minimum: p2u64(5)},  //below the DefaultProjectQuota, so the DefaultProjectQuota should take precedence
			"things":   {Minimum: p2u64(20)}, //above the DefaultProjectQuota, so the constraint.Minimum should take precedence
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

	return cluster, dbm
}

func Test_ScrapeSuccess(t *testing.T) {
	plugin := test.NewPlugin("unittest")
	cluster, dbm := prepareScrapeTest(t, 2, plugin)
	cluster.Authoritative = true
	c := Collector{
		Cluster:   cluster,
		DB:        dbm,
		Plugin:    plugin,
		LogError:  t.Errorf,
		TimeNow:   test.TimeNow,
		AddJitter: test.NoJitter,
		Once:      true,
	}

	//check that ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, dbm.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	//first Scrape should create the entries in `project_resources` with the
	//correct usage and backend quota values (and quota = 0 because nothing was approved yet)
	//and set `project_services.scraped_at` to the current time
	plugin.SetQuotaFails = true
	c.Scrape()
	c.Scrape() //twice because there are two projects
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota, physical_usage) VALUES (1, 'capacity', 10, 0, 100, 10, 0);
		INSERT INTO project_resources (service_id, name, usage) VALUES (1, 'capacity_portion', 0);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota) VALUES (1, 'things', 0, 2, 42, '[{"index":0},{"index":1}]', 0);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota, physical_usage) VALUES (2, 'capacity', 10, 0, 100, 12, 0);
		INSERT INTO project_resources (service_id, name, usage) VALUES (2, 'capacity_portion', 0);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota) VALUES (2, 'things', 0, 2, 42, '[{"index":0},{"index":1}]', 0);
		UPDATE project_services SET scraped_at = 1, scrape_duration_secs = 1, serialized_metrics = '{"capacity_usage":0,"things_usage":2}', checked_at = 1, next_scrape_at = 1801 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = 3, scrape_duration_secs = 1, serialized_metrics = '{"capacity_usage":0,"things_usage":2}', checked_at = 3, next_scrape_at = 1803 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	//second Scrape should not change anything (not even the timestamps) since
	//less than 30 minutes have passed since the last Scrape()
	c.Scrape()
	tr.DBChanges().AssertEmpty()

	//change the data that is reported by the plugin
	plugin.StaticResourceData["capacity"].Quota = 110
	plugin.StaticResourceData["things"].Usage = 5
	setProjectServicesStale(t, dbm)
	//Scrape should pick up the changed resource data
	c.Scrape()
	c.Scrape() //twice because there are two projects
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET backend_quota = 110 WHERE service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET usage = 5, subresources = '[{"index":0},{"index":1},{"index":2},{"index":3},{"index":4}]' WHERE service_id = 1 AND name = 'things';
		UPDATE project_resources SET backend_quota = 110 WHERE service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET usage = 5, subresources = '[{"index":0},{"index":1},{"index":2},{"index":3},{"index":4}]' WHERE service_id = 2 AND name = 'things';
		UPDATE project_services SET scraped_at = 6, serialized_metrics = '{"capacity_usage":0,"things_usage":5}', checked_at = 6, next_scrape_at = 1806 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = 8, serialized_metrics = '{"capacity_usage":0,"things_usage":5}', checked_at = 8, next_scrape_at = 1808 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	//set some new quota values (note that "capacity" already had a non-zero
	//quota because of the cluster.QuotaConstraints)
	_, err := dbm.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 20, "capacity")
	if err != nil {
		t.Fatal(err)
	}
	_, err = dbm.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 13, "things")
	if err != nil {
		t.Fatal(err)
	}

	//Scrape should try to enforce quota values in the backend (this did not work
	//until now because the test.Plugin was instructed to have SetQuota fail)
	plugin.SetQuotaFails = false
	setProjectServicesStale(t, dbm)
	c.Scrape()
	c.Scrape() //twice because there are two projects
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET quota = 20, backend_quota = 20, desired_backend_quota = 20 WHERE service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET quota = 13, backend_quota = 13, desired_backend_quota = 13 WHERE service_id = 1 AND name = 'things';
		UPDATE project_resources SET quota = 20, backend_quota = 24, desired_backend_quota = 24 WHERE service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET quota = 13, backend_quota = 15, desired_backend_quota = 15 WHERE service_id = 2 AND name = 'things';
		UPDATE project_services SET scraped_at = 10, checked_at = 10, next_scrape_at = 1810 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = 12, checked_at = 12, next_scrape_at = 1812 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	//another Scrape (with SetQuota disabled again) should show that the quota
	//update was durable
	plugin.SetQuotaFails = true
	setProjectServicesStale(t, dbm)
	c.Scrape() //twice because there are two projects
	c.Scrape()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET scraped_at = 14, checked_at = 14, next_scrape_at = 1814 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = 16, checked_at = 16, next_scrape_at = 1816 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	//set a quota that contradicts the cluster.QuotaConstraints
	_, err = dbm.Exec(`UPDATE project_resources SET quota = $1 WHERE name = $2`, 50, "capacity")
	if err != nil {
		t.Fatal(err)
	}

	//Scrape should apply the constraint, then enforce quota values in the backend
	plugin.SetQuotaFails = false
	setProjectServicesStale(t, dbm)
	c.Scrape()
	c.Scrape() //twice because there are two projects
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET quota = 40, backend_quota = 40, desired_backend_quota = 40 WHERE service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET quota = 40, backend_quota = 48, desired_backend_quota = 48 WHERE service_id = 2 AND name = 'capacity';
		UPDATE project_services SET scraped_at = 18, checked_at = 18, next_scrape_at = 1818 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = 20, checked_at = 20, next_scrape_at = 1820 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	//set "capacity" to a non-zero usage to observe a non-zero usage on
	//"capacity_portion" (otherwise this resource has been all zeroes this entire
	//time)
	plugin.StaticResourceData["capacity"].Usage = 20
	setProjectServicesStale(t, dbm)
	c.Scrape()
	c.Scrape() //twice because there are two projects
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET usage = 20, physical_usage = 10 WHERE service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET usage = 5 WHERE service_id = 1 AND name = 'capacity_portion';
		UPDATE project_resources SET usage = 20, physical_usage = 10 WHERE service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET usage = 5 WHERE service_id = 2 AND name = 'capacity_portion';
		UPDATE project_services SET scraped_at = 22, serialized_metrics = '{"capacity_usage":20,"things_usage":5}', checked_at = 22, next_scrape_at = 1822 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = 24, serialized_metrics = '{"capacity_usage":20,"things_usage":5}', checked_at = 24, next_scrape_at = 1824 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	//check data metrics generated by this scraping pass
	registry := prometheus.NewPedanticRegistry()
	amc := &AggregateMetricsCollector{Cluster: cluster, DB: dbm}
	registry.MustRegister(amc)
	pmc := &QuotaPluginMetricsCollector{Cluster: cluster, DB: dbm}
	registry.MustRegister(pmc)
	dmc := &DataMetricsCollector{Cluster: cluster, DB: dbm, ReportZeroes: true}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/scrape_metrics.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	//check data metrics with the skip_zero flag set
	registry = prometheus.NewPedanticRegistry()
	amc = &AggregateMetricsCollector{Cluster: cluster, DB: dbm}
	registry.MustRegister(amc)
	pmc = &QuotaPluginMetricsCollector{Cluster: cluster, DB: dbm}
	registry.MustRegister(pmc)
	dmc = &DataMetricsCollector{Cluster: cluster, DB: dbm, ReportZeroes: false}
	registry.MustRegister(dmc)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/metrics",
		ExpectStatus: 200,
		ExpectBody:   assert.FixtureFile("fixtures/scrape_metrics_skipzero.prom"),
	}.Check(t, promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
}

func setProjectServicesStale(t *testing.T, dbm *gorp.DbMap) {
	t.Helper()
	//make sure that the project is scraped again
	_, err := dbm.Exec(`UPDATE project_services SET stale = $1`, true)
	if err != nil {
		t.Fatal(err)
	}
}

func Test_ScrapeFailure(t *testing.T) {
	plugin := test.NewPlugin("unittest")
	cluster, dbm := prepareScrapeTest(t, 2, plugin)
	c := Collector{
		Cluster:   cluster,
		DB:        dbm,
		Plugin:    plugin,
		TimeNow:   test.TimeNow,
		AddJitter: test.NoJitter,
		Once:      true,
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
	tr, tr0 := easypg.NewTracker(t, dbm.Db)
	tr0.AssertEqualToFile("fixtures/scrape0.sql")

	//failing Scrape should create dummy records to ensure that the API finds
	//plausibly-structured data
	plugin.ScrapeFails = true
	c.Scrape()
	c.Scrape() //twice because there are two projects
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota) VALUES (1, 'capacity', 10, 0, -1, 10);
		INSERT INTO project_resources (service_id, name, usage) VALUES (1, 'capacity_portion', 0);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota) VALUES (1, 'things', 0, 0, -1, 0);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota) VALUES (2, 'capacity', 10, 0, -1, 12);
		INSERT INTO project_resources (service_id, name, usage) VALUES (2, 'capacity_portion', 0);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota) VALUES (2, 'things', 0, 0, -1, 0);
		UPDATE project_services SET scraped_at = 0, checked_at = 1, scrape_error_message = 'Scrape failed as requested', next_scrape_at = 301 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = 0, checked_at = 3, scrape_error_message = 'Scrape failed as requested', next_scrape_at = 303 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	//next Scrape should yield the same result
	c.Scrape()
	c.Scrape() //twice because there are two projects
	tr.DBChanges().AssertEmpty()

	//once the backend starts working, we start to see plausible data again
	plugin.ScrapeFails = false
	setProjectServicesStale(t, dbm)
	c.Scrape()
	c.Scrape() //twice because there are two projects
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET backend_quota = 100, physical_usage = 0 WHERE service_id = 1 AND name = 'capacity';
		UPDATE project_resources SET usage = 2, backend_quota = 42, subresources = '[{"index":0},{"index":1}]' WHERE service_id = 1 AND name = 'things';
		UPDATE project_resources SET backend_quota = 100, physical_usage = 0 WHERE service_id = 2 AND name = 'capacity';
		UPDATE project_resources SET usage = 2, backend_quota = 42, subresources = '[{"index":0},{"index":1}]' WHERE service_id = 2 AND name = 'things';
		UPDATE project_services SET scraped_at = 7, scrape_duration_secs = 1, serialized_metrics = '{"capacity_usage":0,"things_usage":2}', checked_at = 7, scrape_error_message = '', next_scrape_at = 1807 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET scraped_at = 9, scrape_duration_secs = 1, serialized_metrics = '{"capacity_usage":0,"things_usage":2}', checked_at = 9, scrape_error_message = '', next_scrape_at = 1809 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)

	//backend fails again and we need to scrape because of the stale flag ->
	//touch neither scraped_at nor the existing resources (this also tests that a
	//failed check causes Scrape() to continue with the next resource afterwards)
	plugin.ScrapeFails = true
	setProjectServicesStale(t, dbm)
	c.Scrape()
	c.Scrape() //twice because there are two projects
	tr.DBChanges().AssertEqualf(`
		UPDATE project_services SET checked_at = 11, scrape_error_message = 'Scrape failed as requested', next_scrape_at = 311 WHERE id = 1 AND project_id = 1 AND type = 'unittest';
		UPDATE project_services SET checked_at = 13, scrape_error_message = 'Scrape failed as requested', next_scrape_at = 313 WHERE id = 2 AND project_id = 2 AND type = 'unittest';
	`)
}

func Test_ScrapeCentralized(t *testing.T) {
	//since all resources in this test operate under centralized quota
	//distribution, bursting makes absolutely no difference
	for _, hasBursting := range []bool{false, true} {
		logg.Info("===== hasBursting = %t =====", hasBursting)

		plugin := test.NewPlugin("centralized")
		cluster, dbm := prepareScrapeTest(t, 1, plugin)
		cluster.Authoritative = true
		c := Collector{
			Cluster:   cluster,
			DB:        dbm,
			Plugin:    plugin,
			LogError:  t.Errorf,
			TimeNow:   test.TimeNow,
			AddJitter: test.NoJitter,
			Once:      true,
		}

		//check that ScanDomains created the domain, project and their services and
		//applied the DefaultProjectQuota from the QuotaDistributionConfiguration
		tr, tr0 := easypg.NewTracker(t, dbm.Db)
		tr0.AssertEqualToFile("fixtures/scrape-centralized0.sql")

		if hasBursting {
			_, err := dbm.Exec(`UPDATE projects SET has_bursting = TRUE WHERE id = 2`)
			if err != nil {
				t.Fatal(err)
			}
			tr.DBChanges().Ignore()
			cluster.Config.Bursting.MaxMultiplier = 0.2
		}

		//first Scrape creates the remaining project_resources, fills usage and
		//enforces quota constraints (note that both projects behave identically
		//since bursting is ineffective under centralized quota distribution)
		c.Scrape()
		tr.DBChanges().AssertEqualf(`
			UPDATE domain_resources SET quota = 10 WHERE service_id = 1 AND name = 'capacity';
			UPDATE domain_resources SET quota = 20 WHERE service_id = 1 AND name = 'things';
			INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota, physical_usage) VALUES (1, 'capacity', 10, 0, 10, 10, 0);
			INSERT INTO project_resources (service_id, name, usage) VALUES (1, 'capacity_portion', 0);
			INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, subresources, desired_backend_quota) VALUES (1, 'things', 20, 2, 20, '[{"index":0},{"index":1}]', 20);
			UPDATE project_services SET scraped_at = 1, scrape_duration_secs = 1, serialized_metrics = '{"capacity_usage":0,"things_usage":2}', checked_at = 1, next_scrape_at = 1801 WHERE id = 1 AND project_id = 1 AND type = 'centralized';
		`)

		//check that DefaultProjectQuota gets reapplied when the quota is 0 (zero
		//quota on CQD resources is defined to mean "DefaultProjectQuota not
		//applied yet"; this check is also relevant for resources moving from HQD to CQD)
		_, err := dbm.Exec(`UPDATE project_resources SET quota = 0 WHERE service_id = 1`)
		if err != nil {
			t.Fatal(err)
		}
		setProjectServicesStale(t, dbm)
		c.Scrape()
		//because Scrape converges back into the same state, the only change is in the timestamp fields
		tr.DBChanges().AssertEqualf(`
			UPDATE project_services SET scraped_at = 3, checked_at = 3, next_scrape_at = 1803 WHERE id = 1 AND project_id = 1 AND type = 'centralized';
		`)
	}
}

////////////////////////////////////////////////////////////////////////////////
// test for auto-approval

type autoApprovalTestPlugin struct {
	StaticBackendQuota uint64
}

func (p *autoApprovalTestPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubresources map[string]bool) error {
	return nil
}

func (p *autoApprovalTestPlugin) PluginTypeID() string {
	return "autoapprovaltest"
}

func (p *autoApprovalTestPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type: "autoapprovaltest",
	}
}

func (p *autoApprovalTestPlugin) Resources() []limesresources.ResourceInfo {
	//one resource can auto-approve, one cannot because BackendQuota != AutoApproveInitialQuota
	return []limesresources.ResourceInfo{
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

func (p *autoApprovalTestPlugin) Rates() []limesrates.RateInfo {
	return nil
}
func (p *autoApprovalTestPlugin) ScrapeRates(project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}
func (p *autoApprovalTestPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
}
func (p *autoApprovalTestPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics string) error {
	return nil
}

func (p *autoApprovalTestPlugin) Scrape(project core.KeystoneProject) (result map[string]core.ResourceData, serializedMetrics string, err error) {
	return map[string]core.ResourceData{
		"approve":   {Usage: 0, Quota: int64(p.StaticBackendQuota)},
		"noapprove": {Usage: 0, Quota: int64(p.StaticBackendQuota) + 10},
	}, "", nil
}

func (p *autoApprovalTestPlugin) IsQuotaAcceptableForProject(project core.KeystoneProject, fullQuotas map[string]map[string]uint64) error {
	return errors.New("unimplemented")
}

func (p *autoApprovalTestPlugin) SetQuota(project core.KeystoneProject, quotas map[string]uint64) error {
	return errors.New("unimplemented")
}

func Test_AutoApproveInitialQuota(t *testing.T) {
	plugin := &autoApprovalTestPlugin{StaticBackendQuota: 10}
	cluster, dbm := prepareScrapeTest(t, 1, plugin)
	c := Collector{
		Cluster:   cluster,
		DB:        dbm,
		Plugin:    plugin,
		LogError:  t.Errorf,
		TimeNow:   test.TimeNow,
		AddJitter: test.NoJitter,
		Once:      true,
	}

	//ScanDomains created the domain, project and their services
	tr, tr0 := easypg.NewTracker(t, dbm.Db)
	tr0.Ignore()

	//when first scraping, the initial backend quota of the "approve" resource
	//shall be approved automatically
	c.Scrape()
	tr.DBChanges().AssertEqualf(`
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota) VALUES (1, 'approve', 10, 0, 10, 10);
		INSERT INTO project_resources (service_id, name, quota, usage, backend_quota, desired_backend_quota) VALUES (1, 'noapprove', 0, 0, 20, 0);
		UPDATE project_services SET scraped_at = 1, scrape_duration_secs = 1, checked_at = 1, next_scrape_at = 1801 WHERE id = 1 AND project_id = 1 AND type = 'autoapprovaltest';
	`)

	//modify the backend quota; verify that the second scrape does not
	//auto-approve the changed value again (auto-approval is limited to the
	//initial scrape)
	plugin.StaticBackendQuota += 10
	setProjectServicesStale(t, dbm)
	c.Scrape()
	tr.DBChanges().AssertEqualf(`
		UPDATE project_resources SET backend_quota = 20 WHERE service_id = 1 AND name = 'approve';
		UPDATE project_resources SET backend_quota = 30 WHERE service_id = 1 AND name = 'noapprove';
		UPDATE project_services SET scraped_at = 3, checked_at = 3, next_scrape_at = 1803 WHERE id = 1 AND project_id = 1 AND type = 'autoapprovaltest';
	`)
}

// A quota plugin with absolutely no resources and rates.
type noopQuotaPlugin struct{}

func (noopQuotaPlugin) Init(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubresources map[string]bool) error {
	return nil
}
func (noopQuotaPlugin) PluginTypeID() string {
	return "noop"
}
func (noopQuotaPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{Type: "noop"}
}
func (noopQuotaPlugin) Resources() []limesresources.ResourceInfo {
	return nil
}
func (noopQuotaPlugin) Scrape(project core.KeystoneProject) (result map[string]core.ResourceData, serializedMetrics string, err error) {
	return nil, "", nil
}
func (noopQuotaPlugin) IsQuotaAcceptableForProject(project core.KeystoneProject, fullQuotas map[string]map[string]uint64) error {
	return nil
}
func (noopQuotaPlugin) SetQuota(project core.KeystoneProject, quotas map[string]uint64) error {
	return nil
}
func (noopQuotaPlugin) Rates() []limesrates.RateInfo {
	return nil
}
func (noopQuotaPlugin) ScrapeRates(project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}
func (noopQuotaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
}
func (noopQuotaPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics string) error {
	return nil
}

func Test_ScrapeButNoResources(t *testing.T) {
	plugin := noopQuotaPlugin{}
	cluster, dbm := prepareScrapeTest(t, 1, plugin)
	c := Collector{
		Cluster:   cluster,
		DB:        dbm,
		Plugin:    plugin,
		LogError:  t.Errorf,
		TimeNow:   test.TimeNow,
		AddJitter: test.NoJitter,
		Once:      true,
	}

	//check that Scrape() behaves properly when encountering a quota plugin with
	//no Resources() (in the wild, this can happen because some quota plugins
	//only have Rates())
	c.Scrape()
	_, tr0 := easypg.NewTracker(t, dbm.Db)
	tr0.AssertEqualf(`
		INSERT INTO domain_services (id, domain_id, type) VALUES (1, 1, 'noop');
		INSERT INTO domains (id, name, uuid) VALUES (1, 'germany', 'uuid-for-germany');
		INSERT INTO project_services (id, project_id, type, scraped_at, scrape_duration_secs, checked_at, next_scrape_at, rates_next_scrape_at) VALUES (1, 1, 'noop', 1, 1, 1, 1801, 0);
		INSERT INTO projects (id, domain_id, name, uuid, parent_uuid, has_bursting) VALUES (1, 1, 'berlin', 'uuid-for-berlin', 'uuid-for-germany', FALSE);
	`)
}
