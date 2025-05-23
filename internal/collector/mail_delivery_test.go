// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"

	"github.com/sapcc/limes/internal/test"
)

const (
	testMailNoopYAML = `
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
`
)

var errMailUndeliverable = errors.New("mail undeliverable")

type MockMail struct {
	UndeliverableMails uint64
}

func (m *MockMail) PostMail(ctx context.Context, req MailRequest) error {
	switch req.ProjectID {
	case "uuid-for-waldorf":
		return nil
	case "uuid-for-berlin":
		return errors.New("fail project id 1")
	case "uuid-for-dresden":
		return nil
	case "uuid-for-frankfurt":
		m.UndeliverableMails++
		return UndeliverableMailError{Inner: errMailUndeliverable}
	}
	return nil
}

func Test_MailDelivery(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo()
	_, liquidServiceType := test.NewMockLiquidClient(srvInfo)
	s := test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(testMailNoopYAML, liquidServiceType)),
		test.WithDBFixtureFile("fixtures/mail_delivery.sql"))
	c := getCollector(t, s)

	mailer := &MockMail{}
	job := c.MailDeliveryJob(nil, mailer)

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()
	// day 1: successfully send a mail
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`DELETE FROM project_mail_notifications WHERE id = 1;`)

	// day 2: fail a mail delivery and increase the fail counter
	s.Clock.StepBy(24 * time.Hour)
	nextDelivery := s.Clock.Now().Add(2 * time.Minute)
	err := job.ProcessOne(s.Ctx)
	if err == nil {
		t.Fatal("failed mail delivery has to return an error")
	}
	tr.DBChanges().AssertEqualf(`UPDATE project_mail_notifications SET next_submission_at = %d, failed_submissions = 1 WHERE id = 2;`, nextDelivery.Unix())

	// day 3: send another mail successfully. Now notification with ID 2 and 3 overlap.
	s.Clock.StepBy(24 * time.Hour)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`DELETE FROM project_mail_notifications WHERE id = 3;`)

	// day 4: unmaged project metadata will result in a queue deletion. No recipient could be resolved from the project.
	s.Clock.StepBy(24 * time.Hour)
	assert.DeepEqual(t, "undeliverable mail count", mailer.UndeliverableMails, 0)
	mustT(t, job.ProcessOne(s.Ctx))
	tr.DBChanges().AssertEqualf(`DELETE FROM project_mail_notifications WHERE id = 4;`)
	assert.DeepEqual(t, "undeliverable mail count", mailer.UndeliverableMails, 1)
}
