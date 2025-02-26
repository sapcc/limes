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

package core

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/db"
)

// CommitmentGroupNotification contains data for rendering mails notifying about commitment workflows (confirmation or expiration).
type CommitmentGroupNotification struct {
	DomainName  string
	ProjectName string
	Commitments []CommitmentNotification
}

// AZResourceLocation is a tuple identifying an AZ resource within a project.
type AZResourceLocation struct {
	ServiceType      db.ServiceType
	ResourceName     liquid.ResourceName
	AvailabilityZone limes.AvailabilityZone
}

// CommitmentNotification appears in type CommitmentGroupNotification.
type CommitmentNotification struct {
	Commitment db.ProjectCommitment
	DateString string
	Resource   AZResourceLocation
}

// MailTemplate is a template for notification mails generated by Limes.
// It appears in type MailTemplateConfiguration.
type MailTemplate struct {
	Subject  string             `yaml:"subject"`
	Body     string             `yaml:"body"`
	Compiled *template.Template `yaml:"-"` // filled during Config.Validate()
}

// Compile compiles the provided mail body template.
// This needs to be run once before any call to Render().
func (t *MailTemplate) Compile() (err error) {
	t.Compiled, err = template.New("mail-body").Parse(t.Body)
	return err
}

// Render generates a mail notification for a completed commitment workflow.
func (t MailTemplate) Render(m CommitmentGroupNotification, projectID db.ProjectID, now time.Time) (db.MailNotification, error) {
	if len(m.Commitments) == 0 {
		return db.MailNotification{}, fmt.Errorf("mail: no commitments provided for projectID: %v", projectID)
	}

	if t.Subject == "" {
		return db.MailNotification{}, fmt.Errorf("mail: subject is empty for projectID: %v", projectID)
	}
	body, err := t.getMailContent(m)
	if err != nil {
		return db.MailNotification{}, err
	}
	if body == "" {
		return db.MailNotification{}, fmt.Errorf("mail: body has no content. Check the mail template. Halted at projectID: %v", projectID)
	}

	notification := db.MailNotification{
		ProjectID:        projectID,
		Subject:          t.Subject,
		Body:             body,
		NextSubmissionAt: now,
	}

	return notification, nil
}

func (t MailTemplate) getMailContent(m CommitmentGroupNotification) (string, error) {
	var ioBuffer bytes.Buffer
	tpl := t.Compiled
	if tpl == nil {
		return "", errors.New("mail: body is empty. Check the accessiblity of the mail template")
	}

	err := tpl.Execute(&ioBuffer, m)
	if err != nil {
		return "", err
	}

	return ioBuffer.String(), nil
}
