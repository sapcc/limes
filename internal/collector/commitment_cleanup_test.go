/*******************************************************************************
*
* Copyright 2024 SAP SE
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
	"encoding/json"
	"fmt"
	"testing"
	"time"

	. "github.com/majewsky/gg/option"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/jobloop"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

const (
	testCleanupOldCommitmentsConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: --test-static
		services:
			- service_type: unittest
				type: liquid
				params:
					area: testing
					liquid_service_type: %[1]s
				commitment_behavior_per_resource:
					- key: capacity
						value:
							durations_per_domain: [{ key: '.*', value: [ '1 day', '3 years' ] }]
	`
)

func TestCleanupOldCommitmentsJob(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo()
	mockLiquidClient, liquidServiceType := test.NewMockLiquidClient(srvInfo)
	s := test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(testCleanupOldCommitmentsConfigYAML, liquidServiceType)),
	)
	c := getCollector(t, s)

	// the Scrape job needs a report that at least satisfies the topology constraints
	mockLiquidClient.SetUsageReport(liquid.ServiceUsageReport{
		InfoVersion: 1,
		Resources: map[liquid.ResourceName]*liquid.ResourceUsageReport{
			"capacity": {
				Quota: Some[int64](0),
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
					"az-one": {},
					"az-two": {},
				},
			},
			"things": {
				Quota: Some[int64](0),
				PerAZ: map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport{
					"any": {},
				},
			},
		},
	})

	// to be able to create commitments, we need to have the projects discovered
	// and their respective project resources created
	_, err := c.ScanDomains(s.Ctx, ScanDomainsOpts{})
	mustT(t, err)
	projectCount, err := c.DB.SelectInt(`SELECT COUNT(*) FROM projects`)
	mustT(t, err)
	scrapeJob := c.ResourceScrapeJob(s.Registry)
	mustT(t, jobloop.ProcessMany(scrapeJob, s.Ctx, int(projectCount), jobloop.WithLabel("service_type", "unittest")))

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	job := c.CleanupOldCommitmentsJob(s.Registry)
	oneDay := 24 * time.Hour
	commitmentForOneDay, err := limesresources.ParseCommitmentDuration("1 day")
	mustT(t, err)
	commitmentForThreeYears, err := limesresources.ParseCommitmentDuration("3 years")
	mustT(t, err)

	// as a control group, this commitment will not expire for the entire duration of the test
	creationContext := db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate}
	buf, err := json.Marshal(creationContext)
	mustT(t, err)
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		ID:                  1,
		AZResourceID:        1,
		Amount:              10,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now(),
		ConfirmedAt:         Some(s.Clock.Now()),
		ExpiresAt:           commitmentForThreeYears.AddTo(s.Clock.Now()),
		State:               db.CommitmentStateActive,
		CreationContextJSON: json.RawMessage(buf),
	}))

	// test 1: create an expired commitment
	s.Clock.StepBy(30 * oneDay)
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		ID:                  2,
		AZResourceID:        1,
		Amount:              10,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now().Add(-oneDay),
		ConfirmedAt:         Some(s.Clock.Now().Add(-oneDay)),
		ExpiresAt:           s.Clock.Now(),
		State:               db.CommitmentStateActive,
		CreationContextJSON: json.RawMessage(buf),
	}))
	tr.DBChanges().Ignore()

	// job should set it to "expired", but leave it around for now
	s.Clock.StepBy(1 * time.Minute)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`UPDATE project_commitments SET state = 'expired' WHERE id = 2 AND transfer_token = NULL;`)

	// one month later, the commitment should be deleted
	s.Clock.StepBy(10 * oneDay)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEmpty()
	s.Clock.StepBy(30 * oneDay)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`DELETE FROM project_commitments WHERE id = 2 AND transfer_token = NULL;`)

	// test 2: simulate a commitment that was created yesterday,
	// and then converted five minutes later
	creationContext = db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate}
	buf, err = json.Marshal(creationContext)
	mustT(t, err)
	supersedeContext := db.CommitmentWorkflowContext{Reason: db.CommitmentReasonConvert, RelatedCommitmentIDs: []db.ProjectCommitmentID{4}}
	supersedeBuf, err := json.Marshal(supersedeContext)
	mustT(t, err)
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		ID:                   3,
		AZResourceID:         1,
		Amount:               10,
		Duration:             commitmentForOneDay,
		CreatedAt:            s.Clock.Now().Add(-oneDay),
		ConfirmedAt:          Some(s.Clock.Now().Add(-oneDay)),
		ExpiresAt:            s.Clock.Now(),
		SupersededAt:         Some(s.Clock.Now().Add(-oneDay).Add(5 * time.Minute)),
		State:                db.CommitmentStateSuperseded,
		CreationContextJSON:  json.RawMessage(buf),
		SupersedeContextJSON: Some(json.RawMessage(supersedeBuf)),
	}))
	creationContext = db.CommitmentWorkflowContext{Reason: db.CommitmentReasonConvert, RelatedCommitmentIDs: []db.ProjectCommitmentID{3}}
	buf, err = json.Marshal(creationContext)
	mustT(t, err)
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		ID:                  4,
		AZResourceID:        2,
		Amount:              10,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now().Add(-oneDay).Add(5 * time.Minute),
		ConfirmedAt:         Some(s.Clock.Now().Add(-oneDay)),
		ExpiresAt:           s.Clock.Now(),
		State:               db.CommitmentStateActive,
		CreationContextJSON: json.RawMessage(buf),
	}))
	tr.DBChanges().Ignore()

	// the commitment in state "superseded" should not be touched when moving to state "expired"
	s.Clock.StepBy(1 * time.Minute)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`UPDATE project_commitments SET state = 'expired' WHERE id = 4 AND transfer_token = NULL;`)

	// when cleaning up, both commitments should be deleted simultaneously
	s.Clock.StepBy(40 * oneDay)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		DELETE FROM project_commitments WHERE id = 3 AND transfer_token = NULL;
		DELETE FROM project_commitments WHERE id = 4 AND transfer_token = NULL;
	`)

	// test 3: simulate two commitments with different expiration dates that were merged
	creationContext = db.CommitmentWorkflowContext{Reason: db.CommitmentReasonMerge, RelatedCommitmentIDs: []db.ProjectCommitmentID{7}}
	buf, err = json.Marshal(creationContext)
	mustT(t, err)
	commitment5 := db.ProjectCommitment{
		ID:                  5,
		AZResourceID:        1,
		Amount:              10,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now().Add(-oneDay),
		ConfirmedAt:         Some(s.Clock.Now().Add(-oneDay)),
		ExpiresAt:           s.Clock.Now(),
		SupersededAt:        Some(s.Clock.Now().Add(-oneDay).Add(10 * time.Minute)),
		State:               db.CommitmentStateSuperseded,
		CreationContextJSON: json.RawMessage(buf),
	}
	mustT(t, c.DB.Insert(&commitment5))
	commitment6 := db.ProjectCommitment{
		ID:                  6,
		AZResourceID:        1,
		Amount:              5,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now().Add(-oneDay).Add(5 * time.Minute),
		ConfirmedAt:         Some(s.Clock.Now().Add(-oneDay).Add(5 * time.Minute)),
		ExpiresAt:           s.Clock.Now().Add(5 * time.Minute),
		SupersededAt:        Some(s.Clock.Now().Add(-oneDay).Add(10 * time.Minute)),
		State:               db.CommitmentStateSuperseded,
		CreationContextJSON: buf,
	}
	mustT(t, c.DB.Insert(&commitment6))
	creationContext = db.CommitmentWorkflowContext{Reason: db.CommitmentReasonMerge, RelatedCommitmentIDs: []db.ProjectCommitmentID{5, 6}}
	buf, err = json.Marshal(creationContext)
	mustT(t, err)
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		ID:                  7,
		AZResourceID:        1,
		Amount:              15,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now().Add(-oneDay).Add(10 * time.Minute),
		ConfirmedAt:         Some(s.Clock.Now().Add(-oneDay).Add(10 * time.Minute)),
		ExpiresAt:           s.Clock.Now().Add(5 * time.Minute),
		State:               db.CommitmentStateActive,
		CreationContextJSON: json.RawMessage(buf),
	}))
	tr.DBChanges().Ignore()

	// only the merged commitment should be set to state expired,
	// the superseded commitments should not be touched
	s.Clock.StepBy(5 * time.Minute)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`UPDATE project_commitments SET state = 'expired' WHERE id = 7 AND transfer_token = NULL;`)

	// when cleaning up, all commitments related to the merge should be deleted simultaneously
	s.Clock.StepBy(40 * oneDay)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		DELETE FROM project_commitments WHERE id = 5 AND transfer_token = NULL;
		DELETE FROM project_commitments WHERE id = 6 AND transfer_token = NULL;
		DELETE FROM project_commitments WHERE id = 7 AND transfer_token = NULL;
	`)
}
