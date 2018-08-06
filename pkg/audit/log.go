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
	"net/http"
	"os"

	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/satori/go.uuid"
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
	events []CADFevent
}

//CADFevent is a substructure of Trail containing data for a CADF event (read: quota change)
//regarding some resource in a domain or project.
type CADFevent struct {
	TypeURI     string         `json:"typeURI"`
	ID          string         `json:"id"`
	EventTime   string         `json:"eventTime"`
	EventType   string         `json:"eventType"`
	Action      string         `json:"action"`
	Outcome     string         `json:"outcome"`
	Reason      EventReason    `json:"reason"`
	Initiator   EventInitiator `json:"initiator"`
	Target      EventTarget    `json:"target"`
	Observer    EventObserver  `json:"observer"`
	RequestPath string         `json:"requestPath"`
}

//EventObserver is a substructure of CADFevent containing data for the event's observer.
type EventObserver struct {
	TypeURI string `json:"typeURI"`
	Name    string `json:"name"`
	ID      string `json:"id"`
}

//EventInitiator is a substructure of CADFevent containing data for the event's initiator.
type EventInitiator struct {
	TypeURI   string             `json:"typeURI"`
	Name      string             `json:"name"`
	ID        string             `json:"id"`
	Domain    string             `json:"domain,omitempty"`
	DomainID  string             `json:"domain_id,omitempty"`
	ProjectID string             `json:"project_id,omitempty"`
	Host      EventInitiatorHost `json:"host"`
}

//EventInitiatorHost is a substructure of eventInitiator containing data for
// the event initiator's host.
type EventInitiatorHost struct {
	Address string `json:"address"`
	Agent   string `json:"agent"`
}

//EventTarget is a substructure of CADFevent containing data for the event's target.
type EventTarget struct {
	TypeURI  string     `json:"typeURI"`
	ID       string     `json:"id"`
	OldQuota uint64     `json:"oldQuota"`
	NewQuota uint64     `json:"newQuota"`
	Unit     limes.Unit `json:"unit,omitempty"`
}

//EventReason is a substructure of CADFevent containing data for the event outcome's reason.
type EventReason struct {
	Type string `json:"reasonType"`
	Code string `json:"reasonCode"`
}

//NewEvent takes the necessary parameters from an API request and returns a new audit event.
func NewEvent(
	t *gopherpolicy.Token, r *http.Request, requestTime, targetID,
	srvType, resName string, resUnit limes.Unit, resQuota, newQuota uint64,
) CADFevent {
	return CADFevent{
		TypeURI:   "http://schemas.dmtf.org/cloud/audit/1.0/event",
		ID:        generateUUID(),
		EventTime: requestTime,
		EventType: "activity",
		Action:    "update",
		Outcome:   "success",
		Reason: EventReason{
			Type: "HTTP",
			Code: "200",
		},
		Initiator: EventInitiator{
			TypeURI:   "service/security/account/user",
			Name:      t.Context.Auth["user_name"],
			ID:        t.Context.Auth["user_id"],
			Domain:    t.Context.Auth["domain_name"],
			DomainID:  t.Context.Auth["domain_id"],
			ProjectID: t.Context.Auth["project_id"],
			Host: EventInitiatorHost{
				Address: TryStripPort(r.RemoteAddr),
				Agent:   r.Header.Get("User-Agent"),
			},
		},
		Target: EventTarget{
			TypeURI:  fmt.Sprintf("service/%s/%s/quota", srvType, resName),
			ID:       targetID,
			OldQuota: resQuota,
			NewQuota: newQuota,
			Unit:     resUnit,
		},
		Observer: EventObserver{
			TypeURI: "service/resources",
			Name:    "limes",
			ID:      observerUUID,
		},
		RequestPath: r.URL.String(),
	}
}

//Add adds an event to the audit trail.
func (t *Trail) Add(event CADFevent) {
	t.events = append(t.events, event)
}

//Commit sends the whole audit trail into the log. Call this after tx.Commit().
func (t *Trail) Commit(clusterID string, config limes.CADFConfiguration) {
	if config.Enabled {
		events := t.events //take a copy to pass into the goroutine
		go backoff(func() error { return sendEvents(clusterID, config, events) })
	}

	for _, event := range t.events {
		//encode the event to a []byte of json data
		msg, _ := json.Marshal(event)
		logg.Other("AUDIT", string(msg))
	}
	t.events = nil //do not log these lines again
}

//Generate an UUID based on random numbers (RFC 4122).
func generateUUID() string {
	u := uuid.Must(uuid.NewV4())
	return u.String()
}
