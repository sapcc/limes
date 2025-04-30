/******************************************************************************
*
*  Copyright 2025 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

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
			method: --test-static
		services:
			- service_type: shared
				type: liquid
				area: testing
				params:
					liquid_service_type: %[1]s
		capacitors:
		- id: noop
			params:
				service_type: noop
				liquid_service_type: %[1]s
		resource_behavior:
		- resource: first/things
			identity_in_v1_api: service/resource
		mail_notifications:
			templates:
				expiring_commitments:
					subject: "Information about expiring commitments"
					body: "Domain:{{ .DomainName }} Project:{{ .ProjectName }}{{ range .Commitments }} Creator:{{ .Commitment.CreatorName }} Amount:{{ .Commitment.Amount }} Duration:{{ .Commitment.Duration }} Date:{{ .DateString }} Service:{{ .Resource.ServiceType }} Resource:{{ .Resource.ResourceName }} AZ:{{ .Resource.AvailabilityZone }}{{ end }}"
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
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 4 AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 5 AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 6 AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 7 AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 8 AND transfer_token = NULL;
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 9 AND transfer_token = NULL;
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (1, 1, 'Information about expiring commitments', 'Domain:germany Project:berlin Creator:dummy Amount:5 Duration:1 year Date:1970-01-01 Service:service Resource:resource AZ:az-one Creator:dummy Amount:10 Duration:1 year Date:1970-01-01 Service:service Resource:resource AZ:az-two', %[1]d);
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (2, 2, 'Information about expiring commitments', 'Domain:germany Project:dresden Creator:dummy Amount:5 Duration:1 year Date:1970-01-27 Service:service Resource:resource AZ:az-one Creator:dummy Amount:10 Duration:1 year Date:1970-01-27 Service:service Resource:resource AZ:az-two', %[1]d);
	`, c.MeasureTime().Unix())

	// mail queue with an empty template should fail
	mailConfig := s.Cluster.Config.MailNotifications.UnwrapOrPanic("MailNotifications == nil!")
	originalMailTemplates := mailConfig.Templates
	mailConfig.Templates = core.MailTemplateConfiguration{ExpiringCommitments: core.MailTemplate{Compiled: template.New("")}}
	// commitments that are already sent out for a notification are not visible in the result set anymore - a new one gets created.
	_, err := s.DB.Exec("INSERT INTO project_commitments (id, az_resource_id, amount, created_at, creator_uuid, creator_name, duration, expires_at, state, creation_context_json) VALUES (99, 1, 10, UNIX(0), 'dummy', 'dummy', '1 year', UNIX(0), 'active', '{}'::jsonb);")
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
		UPDATE project_commitments SET notified_for_expiration = TRUE WHERE id = 99 AND transfer_token = NULL;
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (3, 1, 'Information about expiring commitments', 'Domain:germany Project:berlin Creator:dummy Amount:10 Duration:1 year Date:1970-01-01 Service:service Resource:resource AZ:az-one', %d);
	`, c.MeasureTime().Unix())
}
