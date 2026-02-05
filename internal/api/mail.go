// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	. "github.com/majewsky/gg/option"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/respondwith"
	"golang.org/x/net/html"

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

// RenderMailTemplate handles GET /admin/mail/render?template_type=:type
func (p *v1Provider) RenderMailTemplate(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/admin/mail/render")
	token := p.CheckToken(r)
	if !token.Require(w, "cluster:show") {
		return
	}

	template_type := r.URL.Query().Get("template_type")
	if template_type == "" {
		http.Error(w, "missing required parameter: template_type", http.StatusBadRequest)
		return
	}

	mailConfig, ok := p.Cluster.Config.MailNotifications.Unpack()
	if !ok {
		respondwith.ErrorText(w, errors.New("could not get mail configuration"))
		return
	}

	var template core.MailTemplate
	switch template_type {
	case "confirmed_commitments":
		template = mailConfig.Templates.ConfirmedCommitments
	case "expiring_commitments":
		template = mailConfig.Templates.ExpiringCommitments
	case "transferred_commitments":
		template = mailConfig.Templates.TransferredCommitments
	default:
		http.Error(w, "invalid template type", http.StatusBadRequest)
		return
	}

	dummyResource := core.AZResourceLocation{
		ServiceType:      "foo-service",
		ResourceName:     "bar-resource",
		AvailabilityZone: "eu-de-1a",
	}

	notification := core.CommitmentGroupNotification{
		DomainName:  "example-domain",
		ProjectName: "test-project",
		Commitments: []core.CommitmentNotification{
			{
				Commitment:     everythingCommitment,
				DateString:     time.Now().Format("2000-01-01"),
				Resource:       dummyResource,
				LeftoverAmount: 100,
			},
			{
				Commitment:     everythingCommitment,
				DateString:     time.Now().Format("2000-01-01"),
				Resource:       dummyResource,
				LeftoverAmount: 200,
			},
			{
				Commitment:     everythingCommitment,
				DateString:     time.Now().Format("2000-01-01"),
				Resource:       dummyResource,
				LeftoverAmount: 300,
			},
		},
	}
	projectID := db.ProjectID(42)
	mailNotification, err := template.Render(notification, projectID, time.Now())
	if respondwith.ErrorText(w, err) {
		return
	}

	_, err = html.Parse(strings.NewReader(mailNotification.Body))
	if err != nil {
		respondwith.ErrorText(w, fmt.Errorf("mail template rendering returned invalid HTML: %w", err))
		return
	}

	w.WriteHeader(http.StatusOK)
	_, err = w.Write([]byte(mailNotification.Body))
	if err != nil {
		logg.Error("cannot write response for %s %s: %s", r.Method, r.URL.Path, err.Error())
	}
}
