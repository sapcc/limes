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
	"github.com/sapcc/limes/pkg/test"
)

func Test_Consistency(t *testing.T) {
	driver := keystoneTestDriver(t)
	c := Collector{
		Driver:   driver,
		Plugin:   nil,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}

	//run ScanDomains once to establish a baseline
	_, err := ScanDomains(driver, ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains failed: %v", err)
	}
	test.AssertDBContent(t, "fixtures/scandomains1.sql")

	//remove some *_services entries
	_, err = db.DB.Exec(`DELETE FROM domain_services WHERE type = ?`, "foo")
	if err != nil {
		t.Error(err)
	}
	_, err = db.DB.Exec(`DELETE FROM project_services WHERE type = ?`, "bar")
	if err != nil {
		t.Error(err)
	}
	//add some useless *_services entries
	err = db.DB.Insert(&db.DomainService{
		DomainID: 1,
		Type:     "whatever",
	})
	if err != nil {
		t.Error(err)
	}
	err = db.DB.Insert(&db.ProjectService{
		ProjectID: 1,
		Type:      "whatever",
	})
	if err != nil {
		t.Error(err)
	}
	test.AssertDBContent(t, "fixtures/checkconsistency1.sql")

	//check that CheckConsistency() brings everything back into a nice state
	c.CheckConsistency()
	test.AssertDBContent(t, "fixtures/checkconsistency2.sql")
}
