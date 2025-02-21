/******************************************************************************
*
*  Copyright 2024 SAP SE
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
	"testing"
	"text/template"
	"time"

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
				type: --test-generic
		capacitors:
		- id: noop
			type: --test-static
			params:
				capacity: 0
				resources: []
		mail_templates:
			expiring_commitments: "Domain:{{ .DomainName }} Project:{{ .ProjectName }}{{ range .Commitments }}Creator:{{ .Commitment.CreatorName }} Amount:{{ .Commitment.Amount }} Duration:{{ .Commitment.Duration }} Date: {{ .Date }} Service:{{ .Resource.ServiceType }} Resource:{{ .Resource.ResourceName }} AZ:{{ .Resource.AvailabilityZone }}{{ end }}"
`
)

func Test_ExpiringCommitmentNotification(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testMailNoopWithTemplateYAML),
		test.WithDBFixtureFile("fixtures/mail_expiring_commitments.sql"))
	c := getCollector(t, s)

	job := c.AddExpiringCommitmentsAsMailJob(nil)
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
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (1, 1, 'Information about expiring commitments', 'Domain:germany Project:berlinCreator:dummy Amount:5 Duration:1 year Date: 1970-01-01 Service:first Resource:things AZ:az-oneCreator:dummy Amount:10 Duration:1 year Date: 1970-01-01 Service:first Resource:things AZ:az-two', %[1]d);
		INSERT INTO project_mail_notifications (id, project_id, subject, body, next_submission_at) VALUES (2, 2, 'Information about expiring commitments', 'Domain:germany Project:dresdenCreator:dummy Amount:5 Duration:1 year Date: 1970-02-01 Service:first Resource:things AZ:az-oneCreator:dummy Amount:10 Duration:1 year Date: 1970-02-01 Service:first Resource:things AZ:az-two', %[1]d);
	`, c.MeasureTime().Add(3*time.Minute).Unix())

	// mail queue with an empty template should fail
	s.Cluster.MailTemplates = core.MailTemplates{ConfirmedCommitments: template.New("")}
	err := (job.ProcessOne(s.Ctx))
	if err == nil {
		t.Fatal("execution without mail template must fail")
	}
	s.Cluster.MailTemplates = core.MailTemplates{ConfirmedCommitments: nil}
	err = (job.ProcessOne(s.Ctx))
	if err == nil {
		t.Fatal("execution without mail template must fail")
	}
}
