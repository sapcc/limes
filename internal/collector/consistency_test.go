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

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

func Test_Consistency(t *testing.T) {
	s, cluster := keystoneTestCluster(t)
	c := getCollector(t, s)
	consistencyJob := c.CheckConsistencyJob(s.Registry)

	//run ScanDomains once to establish a baseline
	_, err := c.ScanDomains(ScanDomainsOpts{})
	if err != nil {
		t.Errorf("ScanDomains failed: %v", err)
	}
	easypg.AssertDBContent(t, s.DB.Db, "fixtures/checkconsistency-pre.sql")

	//check that CheckConsistency() is satisfied with the
	//{domain,project}_services created by ScanDomains(), but adds
	//cluster_services entries
	s.Clock.StepBy(time.Hour)
	err = consistencyJob.ProcessOne(s.Ctx)
	if err != nil {
		t.Error(err)
	}
	easypg.AssertDBContent(t, s.DB.Db, "fixtures/checkconsistency0.sql")

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
	_, err = s.DB.Exec(`DELETE FROM cluster_services WHERE type = $1`, "shared")
	if err != nil {
		t.Error(err)
	}
	_, err = s.DB.Exec(`DELETE FROM domain_services WHERE type = $1`, "unshared")
	if err != nil {
		t.Error(err)
	}
	_, err = s.DB.Exec(`DELETE FROM project_services WHERE type = $1`, "shared")
	if err != nil {
		t.Error(err)
	}
	//add some useless *_services entries
	err = s.DB.Insert(&db.ClusterService{Type: "whatever"})
	if err != nil {
		t.Error(err)
	}
	err = s.DB.Insert(&db.DomainService{
		DomainID: 1,
		Type:     "whatever",
	})
	if err != nil {
		t.Error(err)
	}
	err = s.DB.Insert(&db.ProjectService{
		ProjectID:         1,
		Type:              "whatever",
		NextScrapeAt:      time.Unix(0, 0).UTC(),
		RatesNextScrapeAt: time.Unix(0, 0).UTC(),
	})
	if err != nil {
		t.Error(err)
	}

	//add a domain_resource that contradicts the cluster.QuotaConstraints; this
	//should be fixed by CheckConsistency()
	_, err = s.DB.Update(&db.DomainResource{
		ServiceID: 2,
		Name:      "capacity",
		Quota:     200,
	})
	if err != nil {
		t.Error(err)
	}
	easypg.AssertDBContent(t, s.DB.Db, "fixtures/checkconsistency1.sql")

	//check that CheckConsistency() brings everything back into a nice state
	//
	//Also, for all domain services that are created here, all domain resources
	//are added; for all project services that are created here, project
	//resources are added where the quota constraint contains a Minimum value or
	//the quota distribution configuration contains a DefaultQuota value..
	s.Clock.StepBy(time.Hour)
	err = consistencyJob.ProcessOne(s.Ctx)
	if err != nil {
		t.Error(err)
	}
	easypg.AssertDBContent(t, s.DB.Db, "fixtures/checkconsistency2.sql")
}
