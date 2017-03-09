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

	"github.com/sapcc/limes/pkg/limes"
	_ "github.com/sapcc/limes/pkg/plugins"
	"github.com/sapcc/limes/pkg/test"
)

func scrapeTestDriver(t *testing.T) *test.Driver {
	test.InitDatabase(t, "../test/migrations")

	cluster := &limes.ClusterConfiguration{
		ID: "cluster-id-test",
		Services: []limes.ServiceConfiguration{
			limes.ServiceConfiguration{Type: "compute", Shared: false},
		},
	}

	return test.NewDriver(cluster)
}

func Test_Scrape(t *testing.T) {
	driver := scrapeTestDriver(t)

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
	s := scraper{once: true, logError: t.Errorf, timeNow: test.TimeNow}
	s.Scrape(driver, "compute")
	test.AssertDBContent(t, "fixtures/scrape1.sql")

	//second Scrape should not change anything (not even the timestamps) since
	//less than 30 minutes have passed since the last Scrape()
	s.Scrape(driver, "compute")
	test.AssertDBContent(t, "fixtures/scrape1.sql")
}
