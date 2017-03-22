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

package api

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"testing"

	policy "github.com/databus23/goslo.policy"
	"github.com/gorilla/mux"
	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/test"
)

func testSetup(t *testing.T) (*test.Driver, *mux.Router) {
	test.InitDatabase(t, "../test/migrations")

	cluster := &limes.ClusterConfiguration{
		ID: "openstack",
		Services: []limes.ServiceConfiguration{
			limes.ServiceConfiguration{Type: "unittest"},
		},
	}
	driver := test.NewDriver(cluster)

	//seed domains, projects into DB
	_, err := collector.ScanDomains(driver, collector.ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Fatal(err)
	}
	//seed quota/usage data into DB
	c := collector.Collector{
		Driver:   driver,
		Plugin:   limes.GetQuotaPlugin("unittest"),
		LogError: t.Fatalf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}
	for {
		//the problem with Once is that it only scrapes a single project; so keep
		//going until all projects have been scraped
		var count int
		err := db.DB.QueryRow(`SELECT COUNT(*) FROM project_services WHERE stale OR scraped_at IS NULL`).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			break
		} else {
			c.Scrape()
		}
	}

	//remove indeterminism from DB
	_, err = db.DB.Exec(`UPDATE project_services SET scraped_at = 1 WHERE scraped_at IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}

	//simulate agreement between approved and backend quota (pure scraping cannot approve quotas)
	_, err = db.DB.Exec(`UPDATE project_resources SET quota = backend_quota WHERE name = ?`, "capacity")
	if err != nil {
		t.Fatal(err)
	}

	//load test policy (where everything is allowed)
	policyBytes, err := ioutil.ReadFile("../test/policy.json")
	if err != nil {
		t.Fatal(err)
	}
	policyRules := make(map[string]string)
	err = json.Unmarshal(policyBytes, &policyRules)
	if err != nil {
		t.Fatal(err)
	}
	enforcer, err := policy.NewEnforcer(policyRules)
	if err != nil {
		t.Fatal(err)
	}

	router, _ := NewV1Router(driver, limes.APIConfiguration{
		ListenAddress:  "", //irrelevant, we do not listen
		PolicyFilePath: "", //irrelevant, only used for loading
		PolicyEnforcer: enforcer,
	})

	return driver, router
}

func Test_ProjectOperations(t *testing.T) {
	driver, router := testSetup(t)

	domainUUID := driver.StaticDomains[0].UUID
	projectUUID := driver.StaticProjects[domainUUID][0].UUID

	//check GetProject
	test.APIRequest{
		Method:           "GET",
		Path:             fmt.Sprintf("/v1/domains/%s/projects/%s", domainUUID, projectUUID),
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/get-project.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             fmt.Sprintf("/v1/domains/%s/projects/%s?service=unknown", domainUUID, projectUUID),
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/get-project-no-services.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             fmt.Sprintf("/v1/domains/%s/projects/%s?service=unittest&resource=unknown", domainUUID, projectUUID),
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/get-project-no-resources.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             fmt.Sprintf("/v1/domains/%s/projects/%s?service=unittest&resource=things", domainUUID, projectUUID),
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/get-project-filtered.json",
	}.Check(t, router)

	//TODO: check PutProject

	//check ListProjects
	test.APIRequest{
		Method:           "GET",
		Path:             fmt.Sprintf("/v1/domains/%s/projects", domainUUID),
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/list-projects.json",
	}.Check(t, router)
}
