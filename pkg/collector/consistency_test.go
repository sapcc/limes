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
	"time"

	"github.com/sapcc/go-bits/easypg"

	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/test"
)

func Test_Consistency(t *testing.T) {
	test.ResetTime()
	cluster, dbm := keystoneTestCluster(t)
	c := Collector{
		Cluster:  cluster,
		DB:       dbm,
		Plugin:   nil,
		LogError: t.Errorf,
		TimeNow:  test.TimeNow,
		Once:     true,
	}

	//run ScanDomains once to establish a baseline
	_, err := c.ScanDomains(ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains failed: %v", err)
	}
	easypg.AssertDBContent(t, dbm.Db, "fixtures/checkconsistency-pre.sql")

	//check that CheckConsistency() is satisfied with the
	//{domain,project}_services created by ScanDomains(), but adds
	//cluster_services entries
	c.CheckConsistency()
	easypg.AssertDBContent(t, dbm.Db, "fixtures/checkconsistency0.sql")

	//add some quota constraints
	cluster.QuotaConstraints = &core.QuotaConstraintSet{
		Domains: map[string]core.QuotaConstraints{
			"germany": {
				"unshared": {
					"capacity": {Minimum: p2u64(10)},
				},
				"shared": {
					"capacity": {Maximum: p2u64(100)},
				},
			},
		},
		Projects: map[string]map[string]core.QuotaConstraints{
			"germany": {
				"berlin": {
					"unshared": {
						"capacity": {Maximum: p2u64(10)},
					},
				},
				"dresden": {
					"shared": {
						"capacity": {Minimum: p2u64(10)},
					},
				},
			},
		},
	}

	//remove some *_services entries
	_, err = dbm.Exec(`DELETE FROM cluster_services WHERE type = $1`, "shared")
	if err != nil {
		t.Error(err)
	}
	_, err = dbm.Exec(`DELETE FROM domain_services WHERE type = $1`, "unshared")
	if err != nil {
		t.Error(err)
	}
	_, err = dbm.Exec(`DELETE FROM project_services WHERE type = $1`, "shared")
	if err != nil {
		t.Error(err)
	}
	//add some useless *_services entries
	epoch := time.Unix(0, 0).UTC()
	err = dbm.Insert(&db.ClusterService{
		ClusterID: "west",
		Type:      "whatever",
		ScrapedAt: &epoch,
	})
	if err != nil {
		t.Error(err)
	}
	err = dbm.Insert(&db.DomainService{
		DomainID: 1,
		Type:     "whatever",
	})
	if err != nil {
		t.Error(err)
	}
	err = dbm.Insert(&db.ProjectService{
		ProjectID: 1,
		Type:      "whatever",
	})
	if err != nil {
		t.Error(err)
	}

	//add a domain_resource that contradicts the cluster.QuotaConstraints; this
	//should be fixed by CheckConsistency()
	_, err = dbm.Update(&db.DomainResource{
		ServiceID: 2,
		Name:      "capacity",
		Quota:     200,
	})
	if err != nil {
		t.Error(err)
	}
	//add a project_resource that contradicts the cluster.QuotaConstraints; this
	//should cause CheckConsistency() to mark the corresponding project_service
	//as stale (to prompt the scraper to take care of the problem)
	err = dbm.Insert(&db.ProjectResource{
		ServiceID:           3,
		Name:                "capacity",
		Quota:               p2u64(20),
		BackendQuota:        p2i64(0),
		DesiredBackendQuota: p2u64(0),
	})
	if err != nil {
		t.Error(err)
	}
	//remove some project_resources under centralized quota distribution; this
	//should cause CheckConsistency() to notice that default quota is missing and
	//mark the corresponding project_service as stale (to prompt the scraper to
	//take care of the problem)
	_, err = dbm.Exec(`DELETE FROM project_resources WHERE name = $1 AND quota = $2`, "things", 15)
	if err != nil {
		t.Error(err)
	}
	easypg.AssertDBContent(t, dbm.Db, "fixtures/checkconsistency1.sql")

	//check that CheckConsistency() brings everything back into a nice state
	//
	//Also, for all domain services that are created here, all domain resources
	//are added; for all project services that are created here, project
	//resources are added where the quota constraint contains a Minimum value or
	//the quota distribution configuration contains a DefaultQuota value..
	c.CheckConsistency()
	easypg.AssertDBContent(t, dbm.Db, "fixtures/checkconsistency2.sql")
}
