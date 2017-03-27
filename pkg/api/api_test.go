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
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"reflect"
	"testing"

	policy "github.com/databus23/goslo.policy"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/test"
)

func init() {
	limes.RegisterQuotaPlugin(&test.Plugin{StaticServiceType: "shared"})
	limes.RegisterQuotaPlugin(&test.Plugin{StaticServiceType: "unshared"})
}

func setupTest(t *testing.T) (*test.Driver, http.Handler) {
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

	//create test driver with the domains and projects from start-data.sql
	driver := test.NewDriver(config.Clusters["west"])
	router, _ := NewV1Router(driver, config)
	return driver, router
}

func Test_ClusterOperations(t *testing.T) {
	_, router := setupTest(t)

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
	driver, router := setupTest(t)

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

	//check DiscoverDomains
	driver.StaticDomains = append(driver.StaticDomains,
		limes.KeystoneDomain{Name: "spain", UUID: "uuid-for-spain"},
	)
	test.APIRequest{
		Method:           "POST",
		Path:             "/v1/domains/discover",
		ExpectStatusCode: 202,
		ExpectJSON:       "./fixtures/domain-discover.json",
	}.Check(t, router)

	emptyString := ""
	test.APIRequest{
		Method:           "POST",
		Path:             "/v1/domains/discover",
		ExpectStatusCode: 204, //no content because no new domains discovered
		ExpectBody:       &emptyString,
	}.Check(t, router)
}

func Test_ProjectOperations(t *testing.T) {
	driver, router := setupTest(t)

	//check GetProject
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/project-get-berlin.json",
	}.Check(t, router)
	//dresden has a case of backend quota != quota
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains/uuid-for-germany/projects/uuid-for-dresden",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/project-get-dresden.json",
	}.Check(t, router)
	//paris has a case of infinite backend quota
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains/uuid-for-france/projects/uuid-for-paris",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/project-get-paris.json",
	}.Check(t, router)

	//check ListProjects
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains/uuid-for-germany/projects",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/project-list.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains/uuid-for-germany/projects?service=unknown",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/project-list-no-services.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains/uuid-for-germany/projects?service=shared&resource=unknown",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/project-list-no-resources.json",
	}.Check(t, router)
	test.APIRequest{
		Method:           "GET",
		Path:             "/v1/domains/uuid-for-germany/projects?service=shared&resource=things",
		ExpectStatusCode: 200,
		ExpectJSON:       "./fixtures/project-list-filtered.json",
	}.Check(t, router)

	//check DiscoverProjects
	driver.StaticProjects["uuid-for-germany"] = append(driver.StaticProjects["uuid-for-germany"],
		limes.KeystoneProject{Name: "frankfurt", UUID: "uuid-for-frankfurt"},
	)
	test.APIRequest{
		Method:           "POST",
		Path:             "/v1/domains/uuid-for-germany/projects/discover",
		ExpectStatusCode: 202,
		ExpectJSON:       "./fixtures/project-discover.json",
	}.Check(t, router)

	emptyString := ""
	test.APIRequest{
		Method:           "POST",
		Path:             "/v1/domains/uuid-for-germany/projects/discover",
		ExpectStatusCode: 204, //no content because no new projects discovered
		ExpectBody:       &emptyString,
	}.Check(t, router)

	//check SyncProject
	expectStaleProjectServices(t /*, nothing */)
	test.APIRequest{
		Method:           "POST",
		Path:             "/v1/domains/uuid-for-germany/projects/uuid-for-dresden/sync",
		ExpectStatusCode: 202,
		ExpectBody:       &emptyString,
	}.Check(t, router)
	expectStaleProjectServices(t, "dresden:shared", "dresden:unshared")

	//SyncProject should discover the given project if not yet done
	driver.StaticProjects["uuid-for-germany"] = append(driver.StaticProjects["uuid-for-germany"],
		limes.KeystoneProject{Name: "walldorf", UUID: "uuid-for-walldorf"},
	)
	test.APIRequest{
		Method:           "POST",
		Path:             "/v1/domains/uuid-for-germany/projects/uuid-for-walldorf/sync",
		ExpectStatusCode: 202,
		ExpectBody:       &emptyString,
	}.Check(t, router)
	expectStaleProjectServices(t, "dresden:shared", "dresden:unshared", "walldorf:shared", "walldorf:unshared")
}

func expectStaleProjectServices(t *testing.T, pairs ...string) {
	queryStr := `
		SELECT p.name, ps.type
		  FROM projects p JOIN project_services ps ON ps.project_id = p.id
		 WHERE ps.stale
		 ORDER BY p.name, ps.type
	`
	var actualPairs []string

	err := db.ForeachRow(db.DB, queryStr, nil, func(rows *sql.Rows) error {
		var (
			projectName string
			serviceType string
		)
		err := rows.Scan(&projectName, &serviceType)
		if err != nil {
			return err
		}
		actualPairs = append(actualPairs, fmt.Sprintf("%s:%s", projectName, serviceType))
		return nil
	})
	if err != nil {
		t.Fatal(err.Error())
	}

	if !reflect.DeepEqual(pairs, actualPairs) {
		t.Errorf("expected stale project services %v, but got %v", pairs, actualPairs)
	}
}
