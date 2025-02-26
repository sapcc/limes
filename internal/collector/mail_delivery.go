/*******************************************************************************
*
* Copyright 2025 SAP SE
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
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/db"
)

type MailRequest struct {
	ProjectID db.ProjectID `json:"project_id"`
	Subject   string       `json:"subject"`
	MimeType  string       `json:"mime_type"`
	MailText  string       `json:"mail_text"`
}

const (
	// how long to wait after error before retrying sending mails
	mailDeliveryErrorInterval = 2 * time.Minute
)

// MailDeliveryJob is a jobloop.CronJob. A task searches for a queued mail notification.
// If any is found, it builds the mail request body and posts the mail to the mail API.
// Unsuccessful mail deliveries will increase the fail counter and will be requeued with an updated submission timestamp.
func (c *Collector) MailDeliveryJob(registerer prometheus.Registerer, client MailClient) jobloop.Job {
	return (&jobloop.ProducerConsumerJob[db.MailNotification]{
		Metadata: jobloop.JobMetadata{
			ReadableName: "mail delivery",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_mail_deliveries",
				Help: "Counter for mail delivery operations.",
			},
		},
		DiscoverTask: c.discoverMailDeliveryTask,
		ProcessTask: func(ctx context.Context, task db.MailNotification, labels prometheus.Labels) error {
			return c.processMailDeliveryTask(ctx, task, client, labels)
		},
	}).Setup(registerer)
}

var (
	findMailsToProcessQuery = sqlext.SimplifyWhitespace(`
		SELECT * FROM project_mail_notifications
		WHERE next_submission_at <= $1
		-- if a requeue overlaps with another notification, prioritise the one with fewer attempts
		ORDER BY failed_submissions
		LIMIT 1
	`)
)

func (c *Collector) discoverMailDeliveryTask(_ context.Context, _ prometheus.Labels) (task db.MailNotification, err error) {
	startTime := c.MeasureTime()
	err = c.DB.SelectOne(&task, findMailsToProcessQuery, startTime)
	return task, err
}

func (c *Collector) processMailDeliveryTask(ctx context.Context, task db.MailNotification, client MailClient, _ prometheus.Labels) error {
	request := MailRequest{
		ProjectID: task.ProjectID,
		Subject:   task.Subject,
		MimeType:  "text/html",
		MailText:  task.Body,
	}

	mailErr := client.PostMail(ctx, request)
	if mailErr != nil {
		task.NextSubmissionAt = c.MeasureTime().Add(c.AddJitter(mailDeliveryErrorInterval))
		task.FailedSubmissions++
		_, queueErr := c.DB.Update(&task)
		if queueErr != nil {
			return queueErr
		}
		return mailErr
	}
	_, err := c.DB.Delete(&task)
	if err != nil {
		return err
	}
	return nil
}
