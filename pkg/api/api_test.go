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
	"github.com/gorilla/mux"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/test"
)

func init() {
	limes.RegisterQuotaPlugin(&test.Plugin{StaticServiceType: "shared"})
	limes.RegisterQuotaPlugin(&test.Plugin{StaticServiceType: "unshared"})
}

func setupTest(t *testing.T) *mux.Router {
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
	return router
}

func Test_ClusterOperations(t *testing.T) {
	router := setupTest(t)

	//check GetCluster
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/clusters/west",
		ExpectStatusCode: 200,
		ExpectJSON:       "fixtures/cluster-get-west.json",
	}.Check(t, router)

	//check ListClusters
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/clusters",
		ExpectStatusCode: 200,
		ExpectJSON:       "fixtures/cluster-list.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/clusters?service=unknown",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/cluster-list-no-services.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/clusters?service=shared&resource=unknown",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/cluster-list-no-resources.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/clusters?service=shared&resource=things",
		ExpectStatusCode: 200,
		ExpectJSON:       "fixtures/cluster-list-filtered.json",
	}.Check(t, router)
}

func Test_DomainOperations(t *testing.T) {
	router := setupTest(t)

	//check GetDomain
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains/uuid-for-germany",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/domain-get-germany.json",
	}.Check(t, router)
	//domain "france" covers some special cases: an infinite backend quota and
	//missing domain quota entries for one service
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains/uuid-for-france",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/domain-get-france.json",
	}.Check(t, router)

	//check ListDomains
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/domain-list.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains?service=unknown",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/domain-list-no-services.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains?service=shared&resource=unknown",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/domain-list-no-resources.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains?service=shared&resource=things",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/domain-list-filtered.json",
	}.Check(t, router)
}
