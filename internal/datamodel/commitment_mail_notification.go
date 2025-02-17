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
package datamodel

import (
	"bytes"
	"fmt"
	"path"
	"text/template"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/osext"

	"github.com/sapcc/limes/internal/db"
)

type MailInfo struct {
	Region      string
	DomainName  string
	ProjectName string
	Commitments []CommitmentInfo
}

type CommitmentInfo struct {
	CreatorName      string
	Amount           uint64
	Duration         limesresources.CommitmentDuration
	Date             *time.Time
	ServiceName      db.ServiceType
	ResourceName     liquid.ResourceName
	AvailabilityZone limes.AvailabilityZone
}

func (m MailInfo) ScheduleMailNotification(dbi db.Interface, subject string, projectID db.ProjectID) error {
	body, err := m.getEmailContent()
	if err != nil {
		return err
	}

	_, err = dbi.Exec(`UPDATE project_mail_notifications SET project_id = $1, subject = $2, body = $3, next_submission_at = $4`,
		projectID, subject, body, time.Now())
	if err != nil {
		return fmt.Errorf("while creating an email notification for project: %d, %w", projectID, err)
	}

	return nil
}

func (m MailInfo) getEmailContent() (string, error) {
	tplPath, err := osext.NeedGetenv("email_template")
	if err != nil {
		return "", err
	}
	emailTpl, err := template.New(path.Base(tplPath)).ParseFiles(tplPath)
	if err != nil {
		return "", err
	}

	var ioBuffer bytes.Buffer
	err = emailTpl.Execute(&ioBuffer, m)
	if err != nil {
		return "", err
	}

	return ioBuffer.String(), nil
}
