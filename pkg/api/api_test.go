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
	"io/ioutil"
	"testing"

	policy "github.com/databus23/goslo.policy"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/test"
)

func init() {
	limes.RegisterQuotaPlugin(&test.Plugin{StaticServiceType: "shared"})
	limes.RegisterQuotaPlugin(&test.Plugin{StaticServiceType: "unshared"})
}

func Test_ClusterOperations(t *testing.T) {
	//load test database
	test.InitDatabase(t, "../test/migrations")
	test.ExecSQLFile(t, "fixtures/start-data.sql")

	//prepare test configuration
	servicesConfig := []limes.ServiceConfiguration{
		limes.ServiceConfiguration{Type: "shared", Shared: true},
		limes.ServiceConfiguration{Type: "unshared", Shared: false},
	}
	config := limes.Configuration{
		Clusters: map[string]*limes.ClusterConfiguration{
			"west": &limes.ClusterConfiguration{ID: "west", Services: servicesConfig},
			"east": &limes.ClusterConfiguration{ID: "east", Services: servicesConfig},
		},
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
	config.API.PolicyEnforcer, err = policy.NewEnforcer(policyRules)
	if err != nil {
		t.Fatal(err)
	}

	router, _ := NewV1Router(test.NewDriver(config.Clusters["west"]), config)

	//check cluster operations
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/clusters/west",
		ExpectStatusCode: 200,
		ExpectJSON:       "fixtures/cluster-get-west.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/clusters",
		ExpectStatusCode: 200,
		ExpectJSON:       "fixtures/cluster-list.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/clusters?service=shared&resource=things",
		ExpectStatusCode: 200,
		ExpectJSON:       "fixtures/cluster-list-filtered.json",
	}.Check(t, router)
}
