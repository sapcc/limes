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
	"sort"
	"testing"

	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/test"

	_ "github.com/mattn/go-sqlite3"
)

func keystoneTestDriver(t *testing.T) *test.Driver {
	test.InitDatabase(t, "../test/migrations")

	return test.NewDriver(&limes.Cluster{
		ID:              "west",
		ServiceTypes:    []string{"unshared", "shared"},
		IsServiceShared: map[string]bool{"shared": true},
		QuotaPlugins: map[string]limes.QuotaPlugin{
			"shared":   test.NewPlugin("shared"),
			"unshared": test.NewPlugin("unshared"),
		},
		CapacityPlugins: map[string]limes.CapacityPlugin{},
	})
}

func Test_ScanDomains(t *testing.T) {
	driver := keystoneTestDriver(t)

	//construct expectation for return value
	var expectedNewDomains []string
	for _, domain := range driver.StaticDomains {
		expectedNewDomains = append(expectedNewDomains, domain.UUID)
	}

	//first ScanDomains should discover the StaticDomains in the driver,
	//and initialize domains, projects and project_services (project_resources
	//are then constructed by the scraper, and domain_services/domain_resources
	//are created when a cloud admin approves quota for the domain)
	actualNewDomains, err := ScanDomains(driver, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #1 failed: %v", err)
	}
	sort.Strings(expectedNewDomains) //order does not matter
	sort.Strings(actualNewDomains)
	test.AssertDeepEqual(t, "new domains after ScanDomains #1", actualNewDomains, expectedNewDomains)
	test.AssertDBContent(t, "fixtures/scandomains1.sql")

	//first ScanDomains should not discover anything new
	actualNewDomains, err = ScanDomains(driver, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #2 failed: %v", err)
	}
	test.AssertDeepEqual(t, "new domains after ScanDomains #2", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains1.sql")

	//add another project
	domainUUID := "uuid-for-france"
	driver.StaticProjects[domainUUID] = append(driver.StaticProjects[domainUUID],
		limes.KeystoneProject{Name: "bordeaux", UUID: "uuid-for-bordeaux", ParentUUID: "uuid-for-france"},
	)

	//ScanDomains without ScanAllProjects should not see this new project
	actualNewDomains, err = ScanDomains(driver, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #3 failed: %v", err)
	}
	test.AssertDeepEqual(t, "new domains after ScanDomains #3", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains1.sql")

	//ScanDomains with ScanAllProjects should discover the new project
	actualNewDomains, err = ScanDomains(driver, ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #4 failed: %v", err)
	}
	test.AssertDeepEqual(t, "new domains after ScanDomains #4", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains2.sql")

	//remove the project again
	driver.StaticProjects[domainUUID] = driver.StaticProjects[domainUUID][0:1]

	//ScanDomains without ScanAllProjects should not notice anything
	actualNewDomains, err = ScanDomains(driver, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #5 failed: %v", err)
	}
	test.AssertDeepEqual(t, "new domains after ScanDomains #5", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains2.sql")

	//ScanDomains with ScanAllProjects should notice the deleted project and cleanup its records
	actualNewDomains, err = ScanDomains(driver, ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #6 failed: %v", err)
	}
	test.AssertDeepEqual(t, "new domains after ScanDomains #6", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains1.sql")

	//remove a whole domain
	driver.StaticDomains = driver.StaticDomains[0:1]

	//ScanDomains should notice the deleted domain and cleanup its records and also its projects
	actualNewDomains, err = ScanDomains(driver, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #7 failed: %v", err)
	}
	test.AssertDeepEqual(t, "new domains after ScanDomains #7", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains3.sql")

	//rename a domain and a project
	driver.StaticDomains[0].Name = "germany-changed"
	driver.StaticProjects["uuid-for-germany"][0].Name = "berlin-changed"

	//ScanDomains should notice the changed names and update the domain/project records accordingly
	actualNewDomains, err = ScanDomains(driver, ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #8 failed: %v", err)
	}
	test.AssertDeepEqual(t, "new domains after ScanDomains #8", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains4.sql")
}
