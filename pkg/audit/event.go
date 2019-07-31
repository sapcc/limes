/*******************************************************************************
*
* Copyright 2018 SAP SE
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

package audit

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gofrs/uuid"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/hermes/pkg/cadf"
	"github.com/sapcc/limes"
)

var observerUUID = generateUUID()

//EventParams contains parameters for creating an audit event.
type EventParams struct {
	Token      *gopherpolicy.Token
	Request    *http.Request
	ReasonCode int
	Time       time.Time
	Target     EventTarget
}

//EventTarget is the interface that different event target types must implement
//in order to render the respective Event.Target section.
type EventTarget interface {
	Render() cadf.Resource
}

//QuotaEventTarget contains the structure for rendering a Event.Target for
//changes regarding resource quota.
type QuotaEventTarget struct {
	DomainID     string
	ProjectID    string
	ServiceType  string
	ResourceName string
	OldQuota     uint64
	NewQuota     uint64
	QuotaUnit    limes.Unit
	RejectReason string
}

// Render implements the EventTarget interface type.
func (t QuotaEventTarget) Render() cadf.Resource {
	targetID := t.ProjectID
	if t.ProjectID == "" {
		targetID = t.DomainID
	}

	return cadf.Resource{
		TypeURI:   fmt.Sprintf("service/%s/%s/quota", t.ServiceType, t.ResourceName),
		ID:        targetID,
		DomainID:  t.DomainID,
		ProjectID: t.ProjectID,
		Attachments: []cadf.Attachment{{
			Name:    "payload",
			TypeURI: "mime:application/json",
			Content: attachmentContent{
				OldQuota:     t.OldQuota,
				NewQuota:     t.NewQuota,
				Unit:         t.QuotaUnit,
				RejectReason: t.RejectReason},
		}},
	}
}

//BurstEventTarget contains the structure for rendering a Event.Target for
//changes regarding quota bursting for some project.
type BurstEventTarget struct {
	DomainID     string
	ProjectID    string
	NewStatus    bool
	RejectReason string
}

// Render implements the EventTarget interface type.
func (t BurstEventTarget) Render() cadf.Resource {
	return cadf.Resource{
		TypeURI:   "service/resources/bursting",
		ID:        t.ProjectID,
		DomainID:  t.DomainID,
		ProjectID: t.ProjectID,
		Attachments: []cadf.Attachment{{
			Name:    "payload",
			TypeURI: "mime:application/json",
			Content: attachmentContent{
				NewStatus:    t.NewStatus,
				RejectReason: t.RejectReason},
		}},
	}
}

//This type is needed for the custom MarshalJSON behavior.
type attachmentContent struct {
	RejectReason string
	// for quota changes
	OldQuota uint64
	NewQuota uint64
	Unit     limes.Unit
	// for quota bursting
	NewStatus bool
}

//MarshalJSON implements the json.Marshaler interface.
func (a attachmentContent) MarshalJSON() ([]byte, error) {
	//copy data into a struct that does not have a custom MarshalJSON
	data := struct {
		OldQuota     uint64     `json:"oldQuota,omitempty"`
		NewQuota     uint64     `json:"newQuota,omitempty"`
		Unit         limes.Unit `json:"unit,omitempty"`
		NewStatus    bool       `json:"newStatus,omitempty"`
		RejectReason string     `json:"rejectReason,omitempty"`
	}{
		OldQuota:     a.OldQuota,
		NewQuota:     a.NewQuota,
		NewStatus:    a.NewStatus,
		Unit:         a.Unit,
		RejectReason: a.RejectReason,
	}
	//Hermes does not accept a JSON object at target.attachments[].content, so
	//we need to wrap the marshaled JSON into a JSON string
	bytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(bytes))
}

//NewEvent takes the necessary parameters and returns a new audit event.
func NewEvent(p EventParams) cadf.Event {
	outcome := "failure"
	if p.ReasonCode == http.StatusOK {
		outcome = "success"
	}

	return cadf.Event{
		TypeURI:   "http://schemas.dmtf.org/cloud/audit/1.0/event",
		ID:        generateUUID(),
		EventTime: p.Time.Format("2006-01-02T15:04:05.999999+00:00"),
		EventType: "activity",
		Action:    "update",
		Outcome:   outcome,
		Reason: cadf.Reason{
			ReasonType: "HTTP",
			ReasonCode: strconv.Itoa(p.ReasonCode),
		},
		Initiator: cadf.Resource{
			TypeURI:   "service/security/account/user",
			Name:      p.Token.Context.Auth["user_name"],
			ID:        p.Token.Context.Auth["user_id"],
			Domain:    p.Token.Context.Auth["domain_name"],
			DomainID:  p.Token.Context.Auth["domain_id"],
			ProjectID: p.Token.Context.Auth["project_id"],
			Host: &cadf.Host{
				Address: tryStripPort(p.Request.RemoteAddr),
				Agent:   p.Request.Header.Get("User-Agent"),
			},
		},
		Target: p.Target.Render(),
		Observer: cadf.Resource{
			TypeURI: "service/resources",
			Name:    "limes",
			ID:      observerUUID,
		},
		RequestPath: p.Request.URL.String(),
	}
}

//Generate an UUID based on random numbers (RFC 4122).
func generateUUID() string {
	u, err := uuid.NewV4()
	if err != nil {
		logg.Fatal(err.Error())
	}

	return u.String()
}

func tryStripPort(hostPort string) string {
	host, _, err := net.SplitHostPort(hostPort)
	if err == nil {
		return host
	}
	return hostPort
}
