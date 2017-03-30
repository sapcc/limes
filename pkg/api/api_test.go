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
	limes.RegisterQuotaPlugin(test.NewPluginFactory("shared"))
	limes.RegisterQuotaPlugin(test.NewPluginFactory("unshared"))
}

type object map[string]interface{}

func setupTest(t *testing.T) (*test.Driver, http.Handler) {
	//load test database
	test.InitDatabase(t, "../test/migrations")
	test.ExecSQLFile(t, "fixtures/start-data.sql")

	//prepare test configuration
	servicesConfig := []limes.ServiceConfiguration{
		{Type: "shared", Shared: true},
		{Type: "unshared", Shared: false},
	}
	config := limes.Configuration{
		Clusters: map[string]*limes.ClusterConfiguration{
			"west": {ID: "west", Services: servicesConfig},
			"east": {ID: "east", Services: servicesConfig},
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

	test.APIRequest{
		Method:           "POST",
		Path:             "/v1/domains/discover",
		ExpectStatusCode: 204, //no content because no new domains discovered
		ExpectBody:       p2s(""),
	}.Check(t, router)

	//check PutDomain error cases
	test.APIRequest{
		Method:           "PUT",
		Path:             "/v1/domains/uuid-for-germany",
		ExpectStatusCode: 422,
		ExpectBody:       p2s("cannot change shared/capacity quota: domain quota may not be smaller than sum of project quotas in that domain (20 B)\n"),
		RequestJSON: object{
			"domain": object{
				"services": []object{
					{
						"type": "shared",
						"resources": []object{
							//should fail because project quota sum exceeds new quota
							{"name": "capacity", "quota": 1},
						},
					},
				},
			},
		},
	}.Check(t, router)

	//check PutDomain happy path
	test.APIRequest{
		Method:           "PUT",
		Path:             "/v1/domains/uuid-for-germany",
		ExpectStatusCode: 200,
		RequestJSON: object{
			"domain": object{
				"services": []object{
					{
						"type": "shared",
						"resources": []object{
							//should fail because project quota sum exceeds new quota
							{"name": "capacity", "quota": 1234},
						},
					},
				},
			},
		},
	}.Check(t, router)

	var actualQuota uint64
	err := db.DB.QueryRow(`
		SELECT dr.quota FROM domain_resources dr
		JOIN domain_services ds ON ds.id = dr.service_id
		JOIN domains d ON d.id = ds.domain_id
		WHERE d.name = ? AND ds.type = ? AND dr.name = ?`,
		"germany", "shared", "capacity").Scan(&actualQuota)
	if err != nil {
		t.Fatal(err)
	}
	if actualQuota != 1234 {
		t.Error("quota was not updated in database")
	}
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

	test.APIRequest{
		Method:           "POST",
		Path:             "/v1/domains/uuid-for-germany/projects/discover",
		ExpectStatusCode: 204, //no content because no new projects discovered
		ExpectBody:       p2s(""),
	}.Check(t, router)

	//check SyncProject
	expectStaleProjectServices(t /*, nothing */)
	test.APIRequest{
		Method:           "POST",
		Path:             "/v1/domains/uuid-for-germany/projects/uuid-for-dresden/sync",
		ExpectStatusCode: 202,
		ExpectBody:       p2s(""),
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
		ExpectBody:       p2s(""),
	}.Check(t, router)
	expectStaleProjectServices(t, "dresden:shared", "dresden:unshared", "walldorf:shared", "walldorf:unshared")

	//check PutProject: pre-flight checks
	test.APIRequest{
		Method:           "PUT",
		Path:             "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatusCode: 422,
		ExpectBody:       p2s("cannot change shared/capacity quota: quota may not be lower than current usage\ncannot change shared/things quota: domain quota exceeded (maximum acceptable project quota is 20)\n"),
		RequestJSON: object{
			"project": object{
				"services": []object{
					{
						"type": "shared",
						"resources": []object{
							//should fail because usage exceeds new quota
							{"name": "capacity", "quota": 1},
							//should fail because domain quota exceeded
							{"name": "things", "quota": 30},
						},
					},
				},
			},
		},
	}.Check(t, router)

	//check PutProject: quota admissible (i.e. will be persisted in DB), but
	//SetQuota fails for some reason (e.g. backend service down)
	plugin := driver.Cluster().GetQuotaPlugin("shared").(*test.Plugin)
	plugin.SetQuotaFails = true
	test.APIRequest{
		Method:           "PUT",
		Path:             "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatusCode: 500,
		ExpectBody:       p2s("quotas have been accepted, but some error(s) occurred while trying to write the quotas into the backend services:\nSetQuota failed as requested\n"),
		RequestJSON: object{
			"project": object{
				"services": []object{
					{
						"type": "shared",
						"resources": []object{
							{"name": "capacity", "quota": 5},
						},
					},
				},
			},
		},
	}.Check(t, router)
	var (
		actualQuota        uint64
		actualBackendQuota uint64
	)
	err := db.DB.QueryRow(`
		SELECT pr.quota, pr.backend_quota FROM project_resources pr
		JOIN project_services ps ON ps.id = pr.service_id
		JOIN projects p ON p.id = ps.project_id
		WHERE p.name = ? AND ps.type = ? AND pr.name = ?`,
		"berlin", "shared", "capacity").Scan(&actualQuota, &actualBackendQuota)
	if err != nil {
		t.Fatal(err)
	}
	if actualQuota != 5 {
		t.Error("quota was not updated in database")
	}
	if actualBackendQuota == 5 {
		t.Error("backend quota was updated in database even though SetQuota failed")
	}

	//check PutProject happy path
	plugin.SetQuotaFails = false
	test.APIRequest{
		Method:           "PUT",
		Path:             "/v1/domains/uuid-for-germany/projects/uuid-for-berlin",
		ExpectStatusCode: 200,
		RequestJSON: object{
			"project": object{
				"services": []object{
					{
						"type": "shared",
						"resources": []object{
							{"name": "capacity", "quota": 6},
						},
					},
				},
			},
		},
	}.Check(t, router)
	err = db.DB.QueryRow(`
		SELECT pr.quota, pr.backend_quota FROM project_resources pr
		JOIN project_services ps ON ps.id = pr.service_id
		JOIN projects p ON p.id = ps.project_id
		WHERE p.name = ? AND ps.type = ? AND pr.name = ?`,
		"berlin", "shared", "capacity").Scan(&actualQuota, &actualBackendQuota)
	if err != nil {
		t.Fatal(err)
	}
	if actualQuota != 6 {
		t.Error("quota was not updated in database")
	}
	if actualBackendQuota != 6 {
		t.Error("backend quota was not updated in database")
	}
	//double-check with the plugin that the write was durable, and also that
	//SetQuota sent *all* quotas for that service (even for resources that were
	//not touched) as required by the QuotaPlugin interface documentation
	expectBackendQuota := map[string]uint64{
		"capacity": 6,  //as set above
		"things":   10, //unchanged
	}
	backendQuota, exists := plugin.OverrideQuota["uuid-for-berlin"]
	if !exists {
		t.Error("quota was not sent to backend")
	}
	if !reflect.DeepEqual(expectBackendQuota, backendQuota) {
		t.Errorf("expected backend quota %#v, but got %#v", expectBackendQuota, backendQuota)
	}
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

//p2s makes a "pointer to string".
func p2s(val string) *string {
	return &val
}
