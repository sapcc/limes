// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector_test

import (
	"encoding/json"
	"html/template"
	"testing"
	"time"

	. "github.com/majewsky/gg/option"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

func Test_ExpiringCommitmentNotification(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(`{
			"availability_zones": ["az-one", "az-two"],
			"discovery": {
				"method": "static",
				"static_config": {
					"domains": [{"name": "germany", "id": "uuid-for-germany"}],
					"projects": {
						"uuid-for-germany": [
							{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"},
							{"name": "dresden", "id": "uuid-for-dresden", "parent_id": "uuid-for-berlin"}
						]
					}
				}
			},
			"liquids": {
				"first": {"area": "testing"}
			},
			"resource_behavior": [
				{"resource": "first/capacity", "identity_in_v1_api": "service/resource"}
			],
			"mail_notifications": {
				"templates": {
					"expiring_commitments": {
						"subject": "Information about expiring commitments",
						"body": "Domain:{{ .DomainName }} Project:{{ .ProjectName }}{{ range .Commitments }} Creator:{{ .Commitment.CreatorName }} Amount:{{ .Commitment.Amount }} Duration:{{ .Commitment.Duration }} Date:{{ .DateString }} Service:{{ .Resource.ServiceType }} Resource:{{ .Resource.ResourceName }} AZ:{{ .Resource.AvailabilityZone }}{{ end }}"
					}
				}
			}
		}`),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo()),
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
	)

	// shorthands for the DB setup below
	berlin := s.GetProjectID("berlin")
	dresden := s.GetProjectID("dresden")
	firstCapacityAZOne := s.GetAZResourceID("first", "capacity", "az-one")
	firstCapacityAZTwo := s.GetAZResourceID("first", "capacity", "az-two")
	committedForOneYear := must.Return(limesresources.ParseCommitmentDuration("1 year"))
	committedForTenDays := must.Return(limesresources.ParseCommitmentDuration("10 days"))
	const oneDay = 24 * time.Hour

	add := func(c db.ProjectCommitment) {
		t.Helper()
		c.CreatorUUID = "dummy"
		c.CreatorName = "dummy"
		c.CreationContextJSON = json.RawMessage(`{}`)

		c.ExpiresAt = c.Duration.AddTo(c.ConfirmBy.UnwrapOr(c.CreatedAt))
		if c.Status != liquid.CommitmentStatusPlanned && c.Status != liquid.CommitmentStatusPending {
			c.ConfirmedAt = Some(c.ConfirmBy.UnwrapOr(c.CreatedAt))
		}

		s.MustDBInsert(&c)
	}

	// set up some confirmed/planned commitments to check that the job ignores those
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000001",
		ProjectID:    berlin,
		AZResourceID: firstCapacityAZOne,
		Amount:       10,
		CreatedAt:    s.Clock.Now(),
		ConfirmBy:    Some(s.Clock.Now().Add(oneDay)),
		Duration:     committedForOneYear,
		Status:       liquid.CommitmentStatusPlanned,
	})
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000002",
		ProjectID:    berlin,
		AZResourceID: firstCapacityAZOne,
		Amount:       10,
		CreatedAt:    s.Clock.Now(),
		Duration:     committedForOneYear,
		Status:       liquid.CommitmentStatusConfirmed,
	})
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000003",
		ProjectID:    dresden,
		AZResourceID: firstCapacityAZOne,
		Amount:       10,
		CreatedAt:    s.Clock.Now(),
		ConfirmBy:    Some(s.Clock.Now().Add(60 * oneDay)),
		Duration:     committedForTenDays,
		Status:       liquid.CommitmentStatusPlanned,
	})

	// each project has two expiring commitments that should spawn expiry notifications
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000004",
		ProjectID:    berlin,
		AZResourceID: firstCapacityAZOne,
		Amount:       5,
		CreatedAt:    s.Clock.Now().Add(-360 * oneDay),
		Duration:     committedForOneYear,
		Status:       liquid.CommitmentStatusConfirmed,
	})
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000005",
		ProjectID:    berlin,
		AZResourceID: firstCapacityAZTwo,
		Amount:       10,
		CreatedAt:    s.Clock.Now().Add(-360 * oneDay),
		Duration:     committedForOneYear,
		Status:       liquid.CommitmentStatusConfirmed,
	})
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000006",
		ProjectID:    dresden,
		AZResourceID: firstCapacityAZOne,
		Amount:       5,
		CreatedAt:    s.Clock.Now().Add(-340 * oneDay),
		Duration:     committedForOneYear,
		Status:       liquid.CommitmentStatusConfirmed,
	})
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000007",
		ProjectID:    dresden,
		AZResourceID: firstCapacityAZTwo,
		Amount:       10,
		CreatedAt:    s.Clock.Now().Add(-340 * oneDay),
		Duration:     committedForOneYear,
		Status:       liquid.CommitmentStatusConfirmed,
	})

	// expiring short-term commitments should not be queued and instead be marked as notified
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000008",
		ProjectID:    berlin,
		AZResourceID: firstCapacityAZOne,
		Amount:       10,
		CreatedAt:    s.Clock.Now(),
		Duration:     committedForTenDays,
		Status:       liquid.CommitmentStatusConfirmed,
	})

	// superseded commitments should be ignored even if expiresAt is drawing close
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000009",
		ProjectID:    dresden,
		AZResourceID: firstCapacityAZTwo,
		Amount:       10,
		CreatedAt:    s.Clock.Now().Add(-350 * oneDay),
		Duration:     committedForOneYear,
		Status:       liquid.CommitmentStatusSuperseded,
	})

	job := s.Collector.ExpiringCommitmentNotificationJob(nil)
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	// successfully queue two projects with 2 commitments each. Ignore short-term commitments and mark them as notified.
	must.SucceedT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 4 AND uuid = '00000000-0000-0000-0000-000000000004' AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 5 AND uuid = '00000000-0000-0000-0000-000000000005' AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 6 AND uuid = '00000000-0000-0000-0000-000000000006' AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 7 AND uuid = '00000000-0000-0000-0000-000000000007' AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 8 AND uuid = '00000000-0000-0000-0000-000000000008' AND transfer_token = NULL;
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (1, 1, 'Information about expiring commitments', 'Domain:germany Project:berlin Creator:dummy Amount:5 Duration:1 year Date:1970-01-06 Service:service Resource:resource AZ:az-one Creator:dummy Amount:10 Duration:1 year Date:1970-01-06 Service:service Resource:resource AZ:az-two', %[1]d);
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (2, 2, 'Information about expiring commitments', 'Domain:germany Project:dresden Creator:dummy Amount:5 Duration:1 year Date:1970-01-26 Service:service Resource:resource AZ:az-one Creator:dummy Amount:10 Duration:1 year Date:1970-01-26 Service:service Resource:resource AZ:az-two', %[1]d);
	`, s.Clock.Now().Unix())

	// mail queue with an empty template should fail
	mailConfig := s.Cluster.Config.MailNotifications.UnwrapOrPanic("MailNotifications == nil!")
	originalMailTemplates := mailConfig.Templates
	mailConfig.Templates = core.MailTemplateConfiguration{ExpiringCommitments: core.MailTemplate{Compiled: template.New("")}}

	// to test this error, we need to set up a new commitment which did not have its notification enqueued yet
	add(db.ProjectCommitment{
		UUID:         "00000000-0000-0000-0000-000000000010",
		ProjectID:    berlin,
		AZResourceID: firstCapacityAZOne,
		Amount:       10,
		CreatedAt:    s.Clock.Now().Add(-364 * oneDay),
		Duration:     committedForOneYear,
		Status:       liquid.CommitmentStatusConfirmed,
	})
	tr.DBChanges().Ignore()

	err := job.ProcessOne(s.Ctx)
	if err == nil {
		t.Fatal("execution without mail template must fail")
	}
	mailConfig.Templates = core.MailTemplateConfiguration{ExpiringCommitments: core.MailTemplate{Compiled: nil}}
	err = job.ProcessOne(s.Ctx)
	if err == nil {
		t.Fatal("execution without mail template must fail")
	}

	// once the configuration error is fixed, the missing notification will be enqueued on the next run
	mailConfig.Templates = originalMailTemplates
	must.SucceedT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 10 AND uuid = '00000000-0000-0000-0000-000000000010' AND transfer_token = NULL;
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (3, 1, 'Information about expiring commitments', 'Domain:germany Project:berlin Creator:dummy Amount:10 Duration:1 year Date:1970-01-02 Service:service Resource:resource AZ:az-one', %d);
	`, s.Clock.Now().Unix())
}
