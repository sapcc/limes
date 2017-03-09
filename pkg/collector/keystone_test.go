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

	return test.NewDriver(&limes.ClusterConfiguration{
		ID: "cluster-id-test",
		Services: []limes.ServiceConfiguration{
			limes.ServiceConfiguration{Type: "foo", Shared: false},
			limes.ServiceConfiguration{Type: "bar", Shared: true},
		},
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
	domainUUID := "a2f0d9a6a8a0410f9881335f1fe0b538"
	driver.StaticProjects[domainUUID] = append(driver.StaticProjects[domainUUID],
		limes.KeystoneProject{Name: "qux2", UUID: "f4bfdc9cf7284f7e849d91a22ab80e6d"},
	)

	//ScanDomains without ScanAllProjects should not see this new project
	actualNewDomains, err = ScanDomains(driver, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #3 failed: %v", err)
	}
	test.AssertDeepEqual(t, "new domains after ScanDomains #2", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains1.sql")

	//ScanDomains with ScanAllProjects should discover the new project
	actualNewDomains, err = ScanDomains(driver, ScanDomainsOpts{ScanAllProjects: true})
	if err != nil {
		t.Errorf("ScanDomains #3 failed: %v", err)
	}
	test.AssertDeepEqual(t, "new domains after ScanDomains #2", actualNewDomains, []string(nil))
	test.AssertDBContent(t, "fixtures/scandomains2.sql")
}
