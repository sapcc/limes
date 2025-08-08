// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"fmt"
	"html/template"
	"testing"

	"github.com/sapcc/go-bits/easypg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/test"
)

const (
	testMailNoopWithTemplateYAML = `
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
			shared:
				area: testing
				liquid_service_type: %[1]s
		resource_behavior:
		- resource: first/things
			identity_in_v1_api: service/resource
		mail_notifications:
			templates:
				expiring_commitments:
					subject: "Information about expiring commitments"
					body: "Domain:{{ .DomainName }} Project:{{ .ProjectName }}{{ range .Commitments }} Creator:{{ .Commitment.CreatorName }} Amount:{{ .Commitment.Amount }} Duration:{{ .Commitment.Duration }} Date:{{ .DateString }} ProjectService:{{ .Resource.ServiceType }} Resource:{{ .Resource.ResourceName }} AZ:{{ .Resource.AvailabilityZone }}{{ end }}"
`
)

func Test_ExpiringCommitmentNotification(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo()
	_, liquidServiceType := test.NewMockLiquidClient(srvInfo)
	s := test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(testMailNoopWithTemplateYAML, liquidServiceType)),
		test.WithDBFixtureFile("fixtures/mail_expiring_commitments.sql"))
	c := getCollector(t, s)

	job := c.ExpiringCommitmentNotificationJob(nil)
	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()
	// successfully queue two projects with 2 commitments each. Ignore short-term commitments and mark them as notified.
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 4 AND uuid = '00000000-0000-0000-0000-000000000004' AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 5 AND uuid = '00000000-0000-0000-0000-000000000005' AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 6 AND uuid = '00000000-0000-0000-0000-000000000006' AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 7 AND uuid = '00000000-0000-0000-0000-000000000007' AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 8 AND uuid = '00000000-0000-0000-0000-000000000008' AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 9 AND uuid = '00000000-0000-0000-0000-000000000009' AND transfer_token = NULL;
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (1, 1, 'Information about expiring commitments', 'Domain:germany Project:berlin Creator:dummy Amount:5 Duration:1 year Date:1970-01-01 ProjectService:service Resource:resource AZ:az-one Creator:dummy Amount:10 Duration:1 year Date:1970-01-01 ProjectService:service Resource:resource AZ:az-two', %[1]d);
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (2, 2, 'Information about expiring commitments', 'Domain:germany Project:dresden Creator:dummy Amount:5 Duration:1 year Date:1970-01-27 ProjectService:service Resource:resource AZ:az-one Creator:dummy Amount:10 Duration:1 year Date:1970-01-27 ProjectService:service Resource:resource AZ:az-two', %[1]d);
	`, c.MeasureTime().Unix())

	// mail queue with an empty template should fail
	mailConfig := s.Cluster.Config.MailNotifications.UnwrapOrPanic("MailNotifications == nil!")
	originalMailTemplates := mailConfig.Templates
	mailConfig.Templates = core.MailTemplateConfiguration{ExpiringCommitments: core.MailTemplate{Compiled: template.New("")}}
	// commitments that are already sent out for a notification are not visible in the result set anymore - a new one gets created.
	_, err := s.DB.Exec("INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state, creation_context_json) VALUES (99, '00000000-0000-0000-0000-000000000099', 1, 1, 10, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(0), 'active', '{}'::jsonb);")
	tr.DBChanges().Ignore()
	mustT(t, err)
	err = (job.ProcessOne(s.Ctx))
	if err == nil {
		t.Fatal("execution without mail template must fail")
	}
	mailConfig.Templates = core.MailTemplateConfiguration{ExpiringCommitments: core.MailTemplate{Compiled: nil}}
	err = (job.ProcessOne(s.Ctx))
	if err == nil {
		t.Fatal("execution without mail template must fail")
	}

	// create a notification for the created commitment. Do not send another notification for commitments that are already marked as notified.
	mailConfig.Templates = originalMailTemplates
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 99 AND uuid = '00000000-0000-0000-0000-000000000099' AND transfer_token = NULL;
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (3, 1, 'Information about expiring commitments', 'Domain:germany Project:berlin Creator:dummy Amount:10 Duration:1 year Date:1970-01-01 ProjectService:service Resource:resource AZ:az-one', %d);
	`, c.MeasureTime().Unix())
}
