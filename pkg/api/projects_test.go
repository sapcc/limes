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
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sapcc/limes/pkg/collector"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/test"
)

func testDriver(t *testing.T) *test.Driver {
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
		Plugin:   limes.GetPlugin("unittest"),
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

	return driver
}

func Test_ProjectOperations(t *testing.T) {
	driver := testDriver(t)
	router, _ := NewV1Router(driver, limes.APIConfiguration{
		ListenAddress:  "",  //irrelevant, we do not listen
		PolicyFilePath: "",  //irrelevant, only used for loading
		PolicyEnforcer: nil, //irrelevant, the mock driver does not touch this
	})

	domainUUID := driver.StaticDomains[0].UUID
	projectUUID := driver.StaticProjects[domainUUID][0].UUID

	//check GetProject
	path := fmt.Sprintf("/v1/domains/%s/projects/%s", domainUUID, projectUUID)
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("X-Auth-Token", "something")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	response := recorder.Result()
	jsonBytes, _ := ioutil.ReadAll(response.Body)

	if response.StatusCode != 200 {
		t.Errorf("GET %s: expected status code 200, got %d", path, response.StatusCode)
	}
	var buf bytes.Buffer
	err := json.Indent(&buf, jsonBytes, "", "  ")
	if err != nil {
		t.Logf("Response body: %s", jsonBytes)
		t.Fatal(err)
	}
	buf.WriteByte('\n')

	fixturePath, _ := filepath.Abs("./fixtures/get-project.json")
	cmd := exec.Command("diff", "-u", fixturePath, "-")
	cmd.Stdin = &buf
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		t.Logf("Response body: %s", jsonBytes)
		t.Fatal(err)
	}
}
