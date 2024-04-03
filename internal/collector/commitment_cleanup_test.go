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
	"testing"
	"time"

	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
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
				type: --test-generic
		resource_behavior:
			# enable commitments for the */capacity resources
			- { resource: '.*/capacity', commitment_durations: [ '1 day', '3 years' ], commitment_is_az_aware: true }
	`
)

func TestCleanupOldCommitmentsJob(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testCleanupOldCommitmentsConfigYAML),
	)
	c := getCollector(t, s)

	// to be able to create commitments, we need to have the projects discovered
	// and their respective project resources created
	_, err := c.ScanDomains(ScanDomainsOpts{})
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
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		ID:           1,
		AZResourceID: 1,
		Amount:       10,
		Duration:     commitmentForOneDay,
		CreatedAt:    s.Clock.Now(),
		ConfirmedAt:  pointerTo(s.Clock.Now()),
		ExpiresAt:    commitmentForThreeYears.AddTo(s.Clock.Now()),
		State:        db.CommitmentStateActive,
	}))

	// test 1: create an expired commitment
	s.Clock.StepBy(30 * oneDay)
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		ID:           2,
		AZResourceID: 1,
		Amount:       10,
		Duration:     commitmentForOneDay,
		CreatedAt:    s.Clock.Now().Add(-oneDay),
		ConfirmedAt:  pointerTo(s.Clock.Now().Add(-oneDay)),
		ExpiresAt:    s.Clock.Now(),
		State:        db.CommitmentStateActive,
	}))
	tr.DBChanges().Ignore()

	// job should set it to "expired", but leave it around for now
	s.Clock.StepBy(1 * time.Minute)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`UPDATE project_commitments SET state = 'expired' WHERE id = 2;`)

	// one month later, the commitment should be deleted
	s.Clock.StepBy(10 * oneDay)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEmpty()
	s.Clock.StepBy(30 * oneDay)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`DELETE FROM project_commitments WHERE id = 2;`)

	// test 2: simulate a commitment that was created yesterday,
	// and then moved to a different project five minutes later
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		ID:           3,
		AZResourceID: 1,
		Amount:       10,
		Duration:     commitmentForOneDay,
		CreatedAt:    s.Clock.Now().Add(-oneDay),
		ConfirmedAt:  pointerTo(s.Clock.Now().Add(-oneDay)),
		ExpiresAt:    s.Clock.Now(),
		SupersededAt: pointerTo(s.Clock.Now().Add(-oneDay).Add(5 * time.Minute)),
		State:        db.CommitmentStateSuperseded,
	}))
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		ID:            4,
		AZResourceID:  2,
		Amount:        10,
		Duration:      commitmentForOneDay,
		CreatedAt:     s.Clock.Now().Add(-oneDay).Add(5 * time.Minute),
		ConfirmedAt:   pointerTo(s.Clock.Now().Add(-oneDay)),
		ExpiresAt:     s.Clock.Now(),
		State:         db.CommitmentStateActive,
		PredecessorID: pointerTo(db.ProjectCommitmentID(3)),
	}))
	tr.DBChanges().Ignore()

	// the commitment in state "superseded" should not be touched when moving to state "expired"
	s.Clock.StepBy(1 * time.Minute)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`UPDATE project_commitments SET state = 'expired' WHERE id = 4;`)

	// when cleaning up, the successor commitment needs to be cleaned up first...
	s.Clock.StepBy(40 * oneDay)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`DELETE FROM project_commitments WHERE id = 4;`)

	// ...and then the superseded commitment can be cleaned up because it does not have predecessors left
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`DELETE FROM project_commitments WHERE id = 3;`)
}

func pointerTo[T any](val T) *T {
	return &val
}
