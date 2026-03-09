// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/majewsky/gg/option"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// everythingCommitment provides a dummy commitment where all available fields
// are filled with data to cover all potential cases in the mail template rendering process.
var everythingCommitment db.ProjectCommitment = db.ProjectCommitment{
	ID:           42,
	UUID:         "commitment-uuid",
	ProjectID:    1,
	AZResourceID: 7,
	Amount:       500,
	Duration:     must.Return(limesresources.ParseCommitmentDuration("1 year")),
	CreatedAt:    time.Now(),
	CreatorUUID:  "creator-uuid",
	CreatorName:  "Foo User",
	ConfirmBy:    Some(time.Now()),
	ConfirmedAt:  Some(time.Now()),
	ExpiresAt:    time.Now(),

	SupersededAt:         Some(time.Now()),
	CreationContextJSON:  json.RawMessage(`{"reason": "create"}`),
	SupersedeContextJSON: Some(json.RawMessage(`{"reason": "merge", "related_uuids": ["other-commitment-uuid"]}`)),
	RenewContextJSON:     Some(json.RawMessage(`{"reason": "renew"}`)),

	TransferStatus:    limesresources.CommitmentTransferStatusPublic,
	TransferToken:     Some("transfer-token"),
	TransferStartedAt: Some(time.Now()),

	Status:                liquid.CommitmentStatusConfirmed,
	NotifyOnConfirm:       true,
	NotifiedForExpiration: true,
}

// RenderMailTemplate handles GET /admin/mail/render
func (p *v1Provider) RenderMailTemplate(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/admin/mail/render")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show") {
		return
	}

	mailConfig, ok := p.Cluster.Config.MailNotifications.Unpack()
	if !ok {
		respondwith.ErrorText(w, errors.New("could not get mail configuration"))
		return
	}

	templates := map[string]core.MailTemplate{
		"confirmed_commitments":   mailConfig.Templates.ConfirmedCommitments,
		"expiring_commitments":    mailConfig.Templates.ExpiringCommitments,
		"transferred_commitments": mailConfig.Templates.TransferredCommitments,
	}

	dummyResource := core.AZResourceLocation{
		ServiceType:      "foo-service",
		ResourceName:     "bar-resource",
		AvailabilityZone: "eu-de-1a",
	}

	now := time.Now()
	notification := core.CommitmentGroupNotification{
		DomainName:  "example-domain",
		ProjectName: "test-project",
		Commitments: []core.CommitmentNotification{
			{
				Commitment:     everythingCommitment,
				DateString:     now.Format(time.DateOnly),
				Resource:       dummyResource,
				LeftoverAmount: 100,
			},
			{
				Commitment:     everythingCommitment,
				DateString:     now.Format(time.DateOnly),
				Resource:       dummyResource,
				LeftoverAmount: 200,
			},
			{
				Commitment:     everythingCommitment,
				DateString:     now.Format(time.DateOnly),
				Resource:       dummyResource,
				LeftoverAmount: 300,
			},
		},
	}
	projectID := db.ProjectID(42)

	renderedTemplates := make(map[string]string)
	for templateType, template := range templates {
		mailNotification, err := template.Render(notification, projectID, now)
		if err != nil {
			respondwith.ErrorText(w, fmt.Errorf("failed to render template %q: %w", templateType, err))
			return
		}

		err = isValidXML(mailNotification.Body)
		if err != nil {
			respondwith.ErrorText(w, fmt.Errorf("template %q returned invalid HTML: %w", templateType, err))
			return
		}

		// Check for over-escaped content
		if strings.Contains(mailNotification.Body, "\\u") {
			respondwith.ErrorText(w, fmt.Errorf("template %q was escaped multiple times", templateType))
			return
		}

		renderedTemplates[templateType] = mailNotification.Body
	}

	respondwith.JSON(w, http.StatusOK, renderedTemplates)
}

func isValidXML(body string) error {
	r := strings.NewReader(body)
	d := xml.NewDecoder(r)
	d.Strict = true
	d.Entity = xml.HTMLEntity
	for {
		if _, err := d.Token(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			} else {
				return err
			}
		}
	}
}
