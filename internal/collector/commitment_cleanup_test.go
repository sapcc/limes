// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

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
			method: static
			static_config:
				domains:
					- { name: germany, id: uuid-for-germany }
					- { name: france,id: uuid-for-france }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
						- { name: dresden, id: uuid-for-dresden, parent_id: uuid-for-berlin }
					uuid-for-france:
						- { name: paris, id: uuid-for-paris, parent_id: uuid-for-france}
		liquids:
			unittest:
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
		test.WithLiquidConnections,
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
	scrapeJob := c.ScrapeJob(s.Registry)
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
		UUID:                "00000000-0000-0000-0000-000000000001",
		ID:                  1,
		ProjectID:           1,
		AZResourceID:        1,
		Amount:              10,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now(),
		ConfirmedAt:         Some(s.Clock.Now()),
		ExpiresAt:           commitmentForThreeYears.AddTo(s.Clock.Now()),
		Status:              liquid.CommitmentStatusConfirmed,
		CreationContextJSON: json.RawMessage(buf),
	}))

	// test 1: create an expired commitment
	s.Clock.StepBy(30 * oneDay)
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		UUID:                "00000000-0000-0000-0000-000000000002",
		ID:                  2,
		ProjectID:           1,
		AZResourceID:        1,
		Amount:              10,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now().Add(-oneDay),
		ConfirmedAt:         Some(s.Clock.Now().Add(-oneDay)),
		ExpiresAt:           s.Clock.Now(),
		Status:              liquid.CommitmentStatusConfirmed,
		CreationContextJSON: json.RawMessage(buf),
	}))
	tr.DBChanges().Ignore()

	// job should set it to "expired", but leave it around for now
	s.Clock.StepBy(1 * time.Minute)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_commitments SET status = 'expired' WHERE id = 2 AND uuid = '00000000-0000-0000-0000-000000000002' AND transfer_token = NULL;
	`)

	// one month later, the commitment should be deleted
	s.Clock.StepBy(10 * oneDay)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEmpty()
	s.Clock.StepBy(30 * oneDay)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		DELETE FROM project_commitments WHERE id = 2 AND uuid = '00000000-0000-0000-0000-000000000002' AND transfer_token = NULL;
	`)

	// test 2: simulate a commitment that was created yesterday,
	// and then converted five minutes later
	creationContext = db.CommitmentWorkflowContext{Reason: db.CommitmentReasonCreate}
	buf, err = json.Marshal(creationContext)
	mustT(t, err)
	supersedeContext := db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonConvert,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{4},
		RelatedCommitmentUUIDs: []liquid.CommitmentUUID{"00000000-0000-0000-0000-000000000004"},
	}
	supersedeBuf, err := json.Marshal(supersedeContext)
	mustT(t, err)
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		ID:                   3,
		UUID:                 "00000000-0000-0000-0000-000000000003",
		ProjectID:            1,
		AZResourceID:         1,
		Amount:               10,
		Duration:             commitmentForOneDay,
		CreatedAt:            s.Clock.Now().Add(-oneDay),
		ConfirmedAt:          Some(s.Clock.Now().Add(-oneDay)),
		ExpiresAt:            s.Clock.Now(),
		SupersededAt:         Some(s.Clock.Now().Add(-oneDay).Add(5 * time.Minute)),
		Status:               liquid.CommitmentStatusSuperseded,
		CreationContextJSON:  json.RawMessage(buf),
		SupersedeContextJSON: Some(json.RawMessage(supersedeBuf)),
	}))
	creationContext = db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonConvert,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{3},
		RelatedCommitmentUUIDs: []liquid.CommitmentUUID{"00000000-0000-0000-0000-000000000003"},
	}
	buf, err = json.Marshal(creationContext)
	mustT(t, err)
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		ID:                  4,
		UUID:                "00000000-0000-0000-0000-000000000004",
		ProjectID:           1,
		AZResourceID:        2,
		Amount:              10,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now().Add(-oneDay).Add(5 * time.Minute),
		ConfirmedAt:         Some(s.Clock.Now().Add(-oneDay)),
		ExpiresAt:           s.Clock.Now(),
		Status:              liquid.CommitmentStatusConfirmed,
		CreationContextJSON: json.RawMessage(buf),
	}))
	tr.DBChanges().Ignore()

	// the commitment in status "superseded" should not be touched when moving to status "expired"
	s.Clock.StepBy(1 * time.Minute)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_commitments SET status = 'expired' WHERE id = 4 AND uuid = '00000000-0000-0000-0000-000000000004' AND transfer_token = NULL;
	`)

	// when cleaning up, both commitments should be deleted simultaneously
	s.Clock.StepBy(40 * oneDay)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		DELETE FROM project_commitments WHERE id = 3 AND uuid = '00000000-0000-0000-0000-000000000003' AND transfer_token = NULL;
		DELETE FROM project_commitments WHERE id = 4 AND uuid = '00000000-0000-0000-0000-000000000004' AND transfer_token = NULL;
	`)

	// test 3: simulate two commitments with different expiration dates that were merged
	creationContext = db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonMerge,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{7},
		RelatedCommitmentUUIDs: []liquid.CommitmentUUID{"00000000-0000-0000-0000-000000000007"},
	}
	buf, err = json.Marshal(creationContext)
	mustT(t, err)
	commitment5 := db.ProjectCommitment{
		ID:                  5,
		UUID:                "00000000-0000-0000-0000-000000000005",
		ProjectID:           1,
		AZResourceID:        1,
		Amount:              10,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now().Add(-oneDay),
		ConfirmedAt:         Some(s.Clock.Now().Add(-oneDay)),
		ExpiresAt:           s.Clock.Now(),
		SupersededAt:        Some(s.Clock.Now().Add(-oneDay).Add(10 * time.Minute)),
		Status:              liquid.CommitmentStatusSuperseded,
		CreationContextJSON: json.RawMessage(buf),
	}
	mustT(t, c.DB.Insert(&commitment5))
	commitment6 := db.ProjectCommitment{
		ID:                  6,
		UUID:                "00000000-0000-0000-0000-000000000006",
		ProjectID:           1,
		AZResourceID:        1,
		Amount:              5,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now().Add(-oneDay).Add(5 * time.Minute),
		ConfirmedAt:         Some(s.Clock.Now().Add(-oneDay).Add(5 * time.Minute)),
		ExpiresAt:           s.Clock.Now().Add(5 * time.Minute),
		SupersededAt:        Some(s.Clock.Now().Add(-oneDay).Add(10 * time.Minute)),
		Status:              liquid.CommitmentStatusSuperseded,
		CreationContextJSON: buf,
	}
	mustT(t, c.DB.Insert(&commitment6))
	creationContext = db.CommitmentWorkflowContext{
		Reason:                 db.CommitmentReasonMerge,
		RelatedCommitmentIDs:   []db.ProjectCommitmentID{5, 6},
		RelatedCommitmentUUIDs: []liquid.CommitmentUUID{"00000000-0000-0000-0000-000000000005", "00000000-0000-0000-0000-000000000006"},
	}
	buf, err = json.Marshal(creationContext)
	mustT(t, err)
	mustT(t, c.DB.Insert(&db.ProjectCommitment{
		ID:                  7,
		UUID:                "00000000-0000-0000-0000-000000000007",
		ProjectID:           1,
		AZResourceID:        1,
		Amount:              15,
		Duration:            commitmentForOneDay,
		CreatedAt:           s.Clock.Now().Add(-oneDay).Add(10 * time.Minute),
		ConfirmedAt:         Some(s.Clock.Now().Add(-oneDay).Add(10 * time.Minute)),
		ExpiresAt:           s.Clock.Now().Add(5 * time.Minute),
		Status:              liquid.CommitmentStatusConfirmed,
		CreationContextJSON: json.RawMessage(buf),
	}))
	tr.DBChanges().Ignore()

	// only the merged commitment should be set to status expired,
	// the superseded commitments should not be touched
	s.Clock.StepBy(5 * time.Minute)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`UPDATE project_commitments SET status = 'expired' WHERE id = 7 AND uuid = '00000000-0000-0000-0000-000000000007' AND transfer_token = NULL;`)

	// when cleaning up, all commitments related to the merge should be deleted simultaneously
	s.Clock.StepBy(40 * oneDay)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		DELETE FROM project_commitments WHERE id = 5 AND uuid = '00000000-0000-0000-0000-000000000005' AND transfer_token = NULL;
		DELETE FROM project_commitments WHERE id = 6 AND uuid = '00000000-0000-0000-0000-000000000006' AND transfer_token = NULL;
		DELETE FROM project_commitments WHERE id = 7 AND uuid = '00000000-0000-0000-0000-000000000007' AND transfer_token = NULL;
	`)
}
