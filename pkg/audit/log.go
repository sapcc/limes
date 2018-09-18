/*******************************************************************************
*
* Copyright 2017 SAP SE
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
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gofrs/uuid"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/retry"
	"github.com/sapcc/limes/pkg/limes"
)

func init() {
	log.SetOutput(os.Stdout)
	if os.Getenv("LIMES_DEBUG") == "1" {
		logg.ShowDebug = true
	}
	observerUUID = generateUUID()
}

var observerUUID string

//Trail is a list of CADF formatted events with log level AUDIT. It has a separate interface
//from the rest of the logging to allow to withhold the logging until DB changes are committed.
type Trail struct {
	events []CADFEvent
}

//CADFEvent contains the CADF event format according to CADF spec (section 6.6.1 Event)
//and includes extensions for better auditing.
type CADFEvent struct {
	TypeURI     string       `json:"typeURI"`
	ID          string       `json:"id"`
	EventTime   string       `json:"eventTime"`
	EventType   string       `json:"eventType"`
	Action      string       `json:"action"`
	Outcome     string       `json:"outcome"`
	Reason      Reason       `json:"reason,omitempty"`
	Initiator   Resource     `json:"initiator"`
	Target      Resource     `json:"target"`
	Observer    Resource     `json:"observer"`
	Attachments []Attachment `json:"attachments,omitempty"`
	RequestPath string       `json:"requestPath,omitempty"`
}

//Resource is a substructure of CADFEvent and contains attributes describing a (OpenStack-) resource.
type Resource struct {
	TypeURI   string `json:"typeURI"`
	Name      string `json:"name,omitempty"`
	Domain    string `json:"domain,omitempty"`
	ID        string `json:"id"`
	Addresses []struct {
		URL  string `json:"url"`
		Name string `json:"name,omitempty"`
	} `json:"addresses,omitempty"`
	Host        *Host        `json:"host,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
	ProjectID   string       `json:"project_id,omitempty"`
	DomainID    string       `json:"domain_id,omitempty"`
}

//Attachment is a substructure of CADFEvent and contains self-describing extensions to the event.
type Attachment struct {
	Name    string      `json:"name,omitempty"`
	TypeURI string      `json:"typeURI"`
	Content interface{} `json:"content"`
}

//Reason is a substructure of CADFevent containing data for the event outcome's reason.
type Reason struct {
	ReasonType string `json:"reasonType"`
	ReasonCode string `json:"reasonCode"`
}

//Host is a substructure of Resource containing data for the event initiator's host.
type Host struct {
	ID       string `json:"id,omitempty"`
	Address  string `json:"address,omitempty"`
	Agent    string `json:"agent,omitempty"`
	Platform string `json:"platform,omitempty"`
}

//EventParams contains parameters for creating an audit event.
type EventParams struct {
	Token        *gopherpolicy.Token
	Request      *http.Request
	ReasonCode   int
	Time         string
	DomainID     string
	ProjectID    string
	ServiceType  string
	ResourceName string
	OldQuota     uint64
	NewQuota     uint64
	QuotaUnit    limes.Unit
	RejectReason string
}

//newEvent takes the necessary parameters and returns a new audit event.
func (p EventParams) newEvent() CADFEvent {
	targetID := p.ProjectID
	if p.ProjectID == "" {
		targetID = p.DomainID
	}

	outcome := "failure"
	if p.ReasonCode == http.StatusOK {
		outcome = "success"
	}

	return CADFEvent{
		TypeURI:   "http://schemas.dmtf.org/cloud/audit/1.0/event",
		ID:        generateUUID(),
		EventTime: p.Time,
		EventType: "activity",
		Action:    "update",
		Outcome:   outcome,
		Reason: Reason{
			ReasonType: "HTTP",
			ReasonCode: strconv.Itoa(p.ReasonCode),
		},
		Initiator: Resource{
			TypeURI:   "service/security/account/user",
			Name:      p.Token.Context.Auth["user_name"],
			ID:        p.Token.Context.Auth["user_id"],
			Domain:    p.Token.Context.Auth["domain_name"],
			DomainID:  p.Token.Context.Auth["domain_id"],
			ProjectID: p.Token.Context.Auth["project_id"],
			Host: &Host{
				Address: tryStripPort(p.Request.RemoteAddr),
				Agent:   p.Request.Header.Get("User-Agent"),
			},
		},
		Target: Resource{
			TypeURI:   fmt.Sprintf("service/%s/%s/quota", p.ServiceType, p.ResourceName),
			ID:        targetID,
			DomainID:  p.DomainID,
			ProjectID: p.ProjectID,
			Attachments: []Attachment{{
				Name:    "payload",
				TypeURI: "mime:application/json",
				Content: attachmentContent{
					OldQuota:     p.OldQuota,
					NewQuota:     p.NewQuota,
					Unit:         p.QuotaUnit,
					RejectReason: p.RejectReason},
			}},
		},
		Observer: Resource{
			TypeURI: "service/resources",
			Name:    "limes",
			ID:      observerUUID,
		},
		RequestPath: p.Request.URL.String(),
	}
}

//This type is needed for the custom MarshalJSON behavior.
type attachmentContent struct {
	OldQuota     uint64
	NewQuota     uint64
	Unit         limes.Unit
	RejectReason string
}

//MarshalJSON implements the json.Marshaler interface.
func (a attachmentContent) MarshalJSON() ([]byte, error) {
	//copy data into a struct that does not have a custom MarshalJSON
	data := struct {
		OldQuota     uint64     `json:"oldQuota,omitempty"`
		NewQuota     uint64     `json:"newQuota,omitempty"`
		Unit         limes.Unit `json:"unit,omitempty"`
		RejectReason string     `json:"rejectReason,omitempty"`
	}{
		OldQuota:     a.OldQuota,
		NewQuota:     a.NewQuota,
		Unit:         a.Unit,
		RejectReason: a.RejectReason,
	}
	//Hermes does not accept a JSON object at target.attachments[].content, so we need
	//to wrap the marshaled JSON into a JSON string
	bytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(string(bytes))
}

//Add adds an event to the audit trail.
func (t *Trail) Add(p EventParams) {
	event := p.newEvent()
	t.events = append(t.events, event)
}

//Commit sends the whole audit trail into the log. Call this after tx.Commit().
func (t *Trail) Commit(clusterID string, config limes.CADFConfiguration) {
	if config.Enabled && len(t.events) != 0 {
		events := t.events //take a copy to pass into the goroutine
		go retry.ExponentialBackoff{
			Factor:      2,
			MaxInterval: 5 * time.Minute,
		}.RetryUntilSuccessful(func() error { return sendEvents(clusterID, config, events) })
	}

	for _, event := range t.events {
		msg, _ := json.Marshal(event)
		logg.Other("AUDIT", string(msg))
	}
	t.events = nil //do not log these lines again
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
