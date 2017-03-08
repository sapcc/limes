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
	test.InitDatabase(t, "../db/migrations")

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

	var expectedNewDomains []string
	for _, domain := range driver.StaticDomains {
		expectedNewDomains = append(expectedNewDomains, domain.UUID)
	}

	actualNewDomains, err := ScanDomains(driver, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains #1 failed: %v", err)
	}
	sort.Strings(expectedNewDomains) //order does not matter
	sort.Strings(actualNewDomains)
	test.AssertDeepEqual(t, "new domains after first ScanDomains run", actualNewDomains, expectedNewDomains)
}
