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
package datamodel

import (
	"bytes"
	"errors"
	"fmt"
	"text/template"
	"time"

	"github.com/sapcc/limes/internal/db"
)

type MailInfo struct {
	DomainName  string
	ProjectName string
	Commitments []CommitmentInfo
}

type CommitmentInfo struct {
	Commitment db.ProjectCommitment
	Date       string
	Resource   AZResourceLocation
}

func (m MailInfo) CreateMailNotification(tpl *template.Template, subject string, projectID db.ProjectID, now time.Time) (db.MailNotification, error) {
	if len(m.Commitments) == 0 {
		return db.MailNotification{}, fmt.Errorf("mail: no commitments provided for projectID: %v", projectID)
	}

	body, err := m.getMailContent(tpl)
	if err != nil {
		return db.MailNotification{}, err
	}
	if body == "" {
		return db.MailNotification{}, fmt.Errorf("mail: body is empty for projectID: %v", projectID)
	}

	notification := db.MailNotification{
		ProjectID:        projectID,
		Subject:          subject,
		Body:             body,
		NextSubmissionAt: now,
	}

	return notification, nil
}

func (m MailInfo) getMailContent(tpl *template.Template) (string, error) {
	var ioBuffer bytes.Buffer
	if tpl == nil {
		return "", errors.New("mail: body is empty. Check the accessiblity of the mail template")
	}

	err := tpl.Execute(&ioBuffer, m)
	if err != nil {
		return "", err
	}

	return ioBuffer.String(), nil
}
