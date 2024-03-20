/*******************************************************************************
*
* Copyright 2019 SAP SE
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

package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/osext"
)

var showAuditOnStdout = !osext.GetenvBool("LIMES_SILENT")

// eventSink is a channel that receives audit events.
var eventSink chan<- cadf.Event

// StartAuditTrail starts the audit trail by initializing the event sink and
// starting a Commit() goroutine.
func StartAuditTrail() {
	if osext.GetenvBool("LIMES_AUDIT_ENABLE") {
		auditEventPublishSuccessCounter.Add(0)
		auditEventPublishFailedCounter.Add(0)

		onSuccessFunc := func() {
			auditEventPublishSuccessCounter.Inc()
		}
		onFailFunc := func() {
			auditEventPublishFailedCounter.Inc()
		}
		s := make(chan cadf.Event, 20)
		eventSink = s

		rabbitURI := url.URL{
			Scheme: "amqp",
			Host:   net.JoinHostPort(osext.GetenvOrDefault("LIMES_AUDIT_RABBITMQ_HOSTNAME", "localhost"), osext.GetenvOrDefault("LIMES_AUDIT_RABBITMQ_PORT", "5672")),
			User:   url.UserPassword(osext.GetenvOrDefault("LIMES_AUDIT_RABBITMQ_USERNAME", "guest"), osext.GetenvOrDefault("LIMES_AUDIT_RABBITMQ_PASSWORD", "guest")),
			Path:   "/",
		}

		go audittools.AuditTrail{
			EventSink:           s,
			OnSuccessfulPublish: onSuccessFunc,
			OnFailedPublish:     onFailFunc,
		}.Commit(rabbitURI, osext.MustGetenv("LIMES_AUDIT_QUEUE_NAME"))
	}
}

var observerUUID = audittools.GenerateUUID()

// logAndPublishEvent takes the necessary parameters and generates a cadf.Event.
// It logs the event to stdout and publishes it to a RabbitMQ server.
func logAndPublishEvent(eventTime time.Time, req *http.Request, token *gopherpolicy.Token, reasonCode int, target audittools.TargetRenderer) {
	p := audittools.EventParameters{
		Time:       eventTime,
		Request:    req,
		User:       token,
		ReasonCode: reasonCode,
		Action:     cadf.UpdateAction,
		Observer: struct {
			TypeURI string
			Name    string
			ID      string
		}{
			TypeURI: "service/resources",
			Name:    "limes",
			ID:      observerUUID,
		},
		Target: target,
	}
	event := audittools.NewEvent(p)

	if showAuditOnStdout {
		if msg, err := json.Marshal(event); err == nil {
			logg.Other("AUDIT", string(msg))
		} else {
			logg.Error("could not marshal audit event: %s", err.Error())
		}
	}

	if eventSink != nil {
		eventSink <- event
	}
}

// quotaEventTarget contains the structure for rendering a cadf.Event.Target for
// changes regarding resource quota.
type quotaEventTarget struct {
	DomainID     string
	DomainName   string
	ProjectID    string
	ProjectName  string
	ServiceType  string
	ResourceName string
	OldQuota     uint64
	NewQuota     uint64
	QuotaUnit    limes.Unit
	RejectReason string
}

// Render implements the audittools.TargetRenderer interface type.
func (t quotaEventTarget) Render() cadf.Resource {
	targetID := t.ProjectID
	if t.ProjectID == "" {
		targetID = t.DomainID
	}

	return cadf.Resource{
		TypeURI:     fmt.Sprintf("service/%s/%s/quota", t.ServiceType, t.ResourceName),
		ID:          targetID,
		DomainID:    t.DomainID,
		DomainName:  t.DomainName,
		ProjectID:   t.ProjectID,
		ProjectName: t.ProjectName,
		Attachments: []cadf.Attachment{{
			Name:    "payload",
			TypeURI: "mime:application/json",
			Content: targetAttachmentContent{
				OldQuota:     t.OldQuota,
				NewQuota:     t.NewQuota,
				Unit:         t.QuotaUnit,
				RejectReason: t.RejectReason,
			},
		}},
	}
}

// burstEventTarget contains the structure for rendering a cadf.Event.Target for
// changes regarding quota bursting for some project.
type burstEventTarget struct {
	DomainID     string
	DomainName   string
	ProjectID    string
	ProjectName  string
	NewStatus    bool
	RejectReason string
}

// Render implements the audittools.TargetRenderer interface type.
func (t burstEventTarget) Render() cadf.Resource {
	return cadf.Resource{
		TypeURI:     "service/resources/bursting",
		ID:          t.ProjectID,
		DomainID:    t.DomainID,
		DomainName:  t.DomainName,
		ProjectID:   t.ProjectID,
		ProjectName: t.ProjectName,
		Attachments: []cadf.Attachment{{
			Name:    "payload",
			TypeURI: "mime:application/json",
			Content: targetAttachmentContent{
				NewStatus:    t.NewStatus,
				RejectReason: t.RejectReason,
			},
		}},
	}
}

// rateLimitEventTarget contains the structure for rendering a cadf.Event.Target for
// changes regarding rate limits
type rateLimitEventTarget struct {
	DomainID     string
	DomainName   string
	ProjectID    string
	ProjectName  string
	ServiceType  string
	Name         string
	Unit         limes.Unit
	OldLimit     uint64
	NewLimit     uint64
	OldWindow    limesrates.Window
	NewWindow    limesrates.Window
	RejectReason string
}

// Render implements the audittools.TargetRenderer interface type.
func (t rateLimitEventTarget) Render() cadf.Resource {
	return cadf.Resource{
		TypeURI:     fmt.Sprintf("service/%s/%s/rates", t.ServiceType, t.Name),
		ID:          t.ProjectID,
		DomainID:    t.DomainID,
		DomainName:  t.DomainName,
		ProjectID:   t.ProjectID,
		ProjectName: t.ProjectName,
		Attachments: []cadf.Attachment{
			{
				Name:    "payload",
				TypeURI: "mime:application/json",
				Content: targetAttachmentContent{
					Unit:         t.Unit,
					OldLimit:     t.OldLimit,
					NewLimit:     t.NewLimit,
					OldWindow:    t.OldWindow,
					NewWindow:    t.NewWindow,
					RejectReason: t.RejectReason,
				},
			},
		},
	}
}

// commitmentEventTarget contains the structure for rendering a cadf.Event.Target for
// changes regarding commitments.
type commitmentEventTarget struct {
	DomainID    string
	DomainName  string
	ProjectID   string
	ProjectName string
	Commitment  limesresources.Commitment
}

// Render implements the audittools.TargetRenderer interface type.
func (t commitmentEventTarget) Render() cadf.Resource {
	return cadf.Resource{
		TypeURI:     "service/resources/commitment",
		ID:          strconv.FormatInt(t.Commitment.ID, 10),
		DomainID:    t.DomainID,
		DomainName:  t.DomainName,
		ProjectID:   t.ProjectID,
		ProjectName: t.ProjectName,
		Attachments: []cadf.Attachment{{
			Name:    "payload",
			TypeURI: "mime:application/json",
			Content: wrappedAttachment[limesresources.Commitment]{t.Commitment},
		}},
	}
}

// This type marshals to JSON like a string containing the JSON representation of its inner type.
// This is the type of structure that cadf.Attachment.Content expects.
type wrappedAttachment[T any] struct {
	Inner T
}

// MarshalJSON implements the json.Marshaler interface.
func (a wrappedAttachment[T]) MarshalJSON() ([]byte, error) {
	buf, err := json.Marshal(a.Inner)
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(buf))
}

// This type is needed for the custom MarshalJSON behavior.
type targetAttachmentContent struct {
	RejectReason string
	// for quota or rate limit changes
	Unit limes.Unit
	// for quota changes
	OldQuota uint64
	NewQuota uint64
	// for quota bursting
	NewStatus bool
	// for rate limit changes
	OldLimit  uint64
	NewLimit  uint64
	OldWindow limesrates.Window
	NewWindow limesrates.Window
}

// MarshalJSON implements the json.Marshaler interface.
func (a targetAttachmentContent) MarshalJSON() ([]byte, error) {
	// copy data into a struct that does not have a custom MarshalJSON
	data := struct {
		OldQuota     uint64            `json:"oldQuota,omitempty"`
		NewQuota     uint64            `json:"newQuota,omitempty"`
		Unit         limes.Unit        `json:"unit,omitempty"`
		NewStatus    bool              `json:"newStatus,omitempty"`
		RejectReason string            `json:"rejectReason,omitempty"`
		OldLimit     uint64            `json:"oldLimit,omitempty"`
		NewLimit     uint64            `json:"newLimit,omitempty"`
		OldWindow    limesrates.Window `json:"oldWindow,omitempty"`
		NewWindow    limesrates.Window `json:"newWindow,omitempty"`
	}{
		OldQuota:     a.OldQuota,
		NewQuota:     a.NewQuota,
		NewStatus:    a.NewStatus,
		Unit:         a.Unit,
		RejectReason: a.RejectReason,
		OldLimit:     a.OldLimit,
		NewLimit:     a.NewLimit,
		OldWindow:    a.OldWindow,
		NewWindow:    a.NewWindow,
	}
	// Hermes does not accept a JSON object at target.attachments[].content, so
	// we need to wrap the marshaled JSON into a JSON string
	bytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(bytes))
}
