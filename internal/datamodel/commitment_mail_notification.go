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
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

type MailInfo struct {
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

func (m MailInfo) CreateMailNotification(cluster *core.Cluster, subject string, projectID db.ProjectID, now time.Time) (*db.MailNotification, error) {
	if len(m.Commitments) == 0 {
		return nil, fmt.Errorf("mail: no commitments provided for projectID: %v", projectID)
	}

	body, err := m.getEmailContent(cluster)
	if err != nil {
		return nil, err
	}

	if body == "" {
		return nil, fmt.Errorf("mail: body is empty. Check the accessiblity of the mail template. ProjectID: %v", projectID)
	}

	notification := db.MailNotification{
		ProjectID:        projectID,
		Subject:          subject,
		Body:             body,
		NextSubMissionAt: now,
	}

	return &notification, nil
}

func (m MailInfo) getEmailContent(cluster *core.Cluster) (string, error) {
	var ioBuffer bytes.Buffer
	err := cluster.MailTemplate.Execute(&ioBuffer, m)
	if err != nil {
		return "", err
	}

	return ioBuffer.String(), nil
}
