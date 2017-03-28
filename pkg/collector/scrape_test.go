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

	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/test"
)

func scrapeTestDriver(t *testing.T) *test.Driver {
	test.InitDatabase(t, "../test/migrations")

	cluster := &limes.ClusterConfiguration{
		ID: "west",
		Services: []limes.ServiceConfiguration{
			limes.ServiceConfiguration{Type: "unittest", Shared: false},
		},
	}

	return test.NewDriver(cluster)
}

func Test_Scrape(t *testing.T) {
	driver := scrapeTestDriver(t)
	plugin := limes.GetQuotaPlugin("unittest").(*test.Plugin)
	c := Collector{
		Driver:   driver,
		Plugin:   plugin,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}

	//one domain and one project is enough
	domainUUID1 := driver.StaticDomains[0].UUID
	driver.StaticDomains = driver.StaticDomains[0:1]
	driver.StaticProjects = map[string][]limes.KeystoneProject{
		domainUUID1: driver.StaticProjects[domainUUID1][0:1],
	}

	//ScanDomains is required to create the entries in `domains`, `domain_services`
	_, err := ScanDomains(driver, ScanDomainsOpts{})
	if err != nil {
		t.Fatal(err)
	}
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

	//set some non-zero quota values so that we can discriminate them from zero
	_, err = db.DB.Exec(`UPDATE project_resources SET quota = ? WHERE name = ?`, 20, "capacity")
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
}

func setProjectServicesStale(t *testing.T) {
	//make sure that the project is scraped again
	_, err := db.DB.Exec(`UPDATE project_services SET stale = ?`, true)
	if err != nil {
		t.Fatal(err)
	}
}
