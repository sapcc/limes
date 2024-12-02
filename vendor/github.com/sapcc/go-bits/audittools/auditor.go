/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package audittools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/cadf"

	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/osext"
)

// Auditor is a high-level interface for audit event acceptors.
// In a real process, use NewAuditor() or NewNullAuditor() depending on whether you have RabbitMQ client credentials.
// In a test scenario, use NewMockAuditor() to get an assertable mock implementation.
type Auditor interface {
	Record(EventParameters)
}

////////////////////////////////////////////////////////////////////////////////
// type standardAuditor

// AuditorOpts contains options for NewAuditor().
type AuditorOpts struct {
	// Required. Identifies the current process.
	// The Observer.ID field should be set to a UUID, such as those generated by GenerateUUID().
	//
	// When recording events, the EventParameters.Observer field does not need to be filled by the caller.
	// It will instead be filled with this Observer.
	Observer Observer

	// Optional. If given, RabbitMQ connection options will be read from the following environment variables:
	//   - "${PREFIX}_HOSTNAME" (defaults to "localhost")
	//   - "${PREFIX}_PORT" (defaults to "5672")
	//   - "${PREFIX}_USERNAME" (defaults to "guest")
	//   - "${PREFIX}_PASSWORD" (defaults to "guest")
	//   - "${PREFIX}_QUEUE_NAME" (required)
	EnvPrefix string

	// Required if EnvPrefix is empty, ignored otherwise.
	// Contains the RabbitMQ connection options that would otherwise be read from environment variables.
	ConnectionURL string
	QueueName     string

	// Optional. If given, the Auditor will register its Prometheus metrics with this registry instead of the default registry.
	// The following metrics are registered:
	//   - "audittools_successful_submissions" (counter, no labels)
	//   - "audittools_failed_submissions" (counter, no labels)
	Registry prometheus.Registerer
}

func (opts AuditorOpts) getConnectionOptions() (rabbitURL url.URL, queueName string, err error) {
	// option 1: passed explicitly
	if opts.EnvPrefix == "" {
		if opts.ConnectionURL == "" {
			return url.URL{}, "", errors.New("missing required value: AuditorOpts.ConnectionURL")
		}
		if opts.QueueName == "" {
			return url.URL{}, "", errors.New("missing required value: AuditorOpts.QueueName")
		}
		rabbitURL, err := url.Parse(opts.ConnectionURL)
		if err != nil {
			return url.URL{}, "", fmt.Errorf("while parsing AuditorOpts.ConnectionURL (%q): %w", opts.ConnectionURL, err)
		}
		return *rabbitURL, opts.QueueName, nil
	}

	// option 2: passed via environment variables
	queueName, err = osext.NeedGetenv(opts.EnvPrefix + "_QUEUE_NAME")
	if err != nil {
		return url.URL{}, "", err
	}
	hostname := osext.GetenvOrDefault(opts.EnvPrefix+"_HOSTNAME", "localhost")
	port, err := strconv.Atoi(osext.GetenvOrDefault(opts.EnvPrefix+"_PORT", "5672"))
	if err != nil {
		return url.URL{}, "", fmt.Errorf("invalid value for %s_PORT: %w", opts.EnvPrefix, err)
	}
	username := osext.GetenvOrDefault(opts.EnvPrefix+"_USERNAME", "guest")
	pass := osext.GetenvOrDefault(opts.EnvPrefix+"_PASSWORD", "guest")
	rabbitURL = url.URL{
		Scheme: "amqp",
		Host:   net.JoinHostPort(hostname, strconv.Itoa(port)),
		User:   url.UserPassword(username, pass),
		Path:   "/",
	}
	return rabbitURL, queueName, nil
}

type standardAuditor struct {
	Observer  Observer
	EventSink chan<- cadf.Event
}

// NewAuditor builds an Auditor connected to a RabbitMQ instance, using the provided configuration.
// This is the recommended high-level constructor for an audit event receiver.
// The more low-level type AuditTrail should be used instead only if absolutely necessary.
func NewAuditor(ctx context.Context, opts AuditorOpts) (Auditor, error) {
	// validate provided options
	if opts.Observer.TypeURI == "" {
		return nil, errors.New("missing required value: AuditorOpts.Observer.TypeURI")
	}
	if opts.Observer.Name == "" {
		return nil, errors.New("missing required value: AuditorOpts.Observer.Name")
	}
	if opts.Observer.ID == "" {
		return nil, errors.New("missing required value: AuditorOpts.Observer.ID")
	}
	if opts.EnvPrefix == "" {
		return nil, errors.New("missing required value: AuditorOpts.EnvPrefix")
	}

	// register Prometheus metrics
	successCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "audittools_successful_submissions",
		Help: "Counter for successful audit event submissions to the Hermes RabbitMQ server.",
	})
	failureCounter := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "audittools_failed_submissions",
		Help: "Counter for failed (but retryable) audit event submissions to the Hermes RabbitMQ server.",
	})
	successCounter.Add(0)
	failureCounter.Add(0)
	if opts.Registry == nil {
		prometheus.MustRegister(successCounter)
		prometheus.MustRegister(failureCounter)
	} else {
		opts.Registry.MustRegister(successCounter)
		opts.Registry.MustRegister(failureCounter)
	}

	// spawn event delivery goroutine
	rabbitURL, queueName, err := opts.getConnectionOptions()
	if err != nil {
		return nil, err
	}
	eventChan := make(chan cadf.Event, 20)
	go AuditTrail{
		EventSink:           eventChan,
		OnSuccessfulPublish: func() { successCounter.Inc() },
		OnFailedPublish:     func() { failureCounter.Inc() },
	}.Commit(ctx, rabbitURL, queueName)

	return &standardAuditor{
		Observer:  opts.Observer,
		EventSink: eventChan,
	}, nil
}

// Record implements the Auditor interface.
func (a *standardAuditor) Record(params EventParameters) {
	params.Observer = a.Observer
	a.EventSink <- NewEvent(params)
}

////////////////////////////////////////////////////////////////////////////////
// type nullAuditor

// NewNullAuditor returns an Auditor that does nothing (except produce a debug log of the discarded event).
// This is only intended to be used for non-productive deployments without a Hermes instance.
func NewNullAuditor() Auditor {
	return nullAuditor{}
}

type nullAuditor struct{}

// Record implements the Auditor interface.
func (nullAuditor) Record(params EventParameters) {
	if logg.ShowDebug {
		msg, err := json.Marshal(NewEvent(params))
		if err == nil {
			logg.Debug("audit event received: %s", string(msg))
		}
	}
}

////////////////////////////////////////////////////////////////////////////////
// type MockAuditor

// MockAuditor is a test recorder that satisfies the Auditor interface.
type MockAuditor struct {
	events []cadf.Event
}

// NewMockAuditor constructs a new MockAuditor instance.
func NewMockAuditor() *MockAuditor {
	return &MockAuditor{}
}

// Record implements the Auditor interface.
func (a *MockAuditor) Record(params EventParameters) {
	a.events = append(a.events, a.normalize(NewEvent(params)))
}

// ExpectEvents checks that the recorded events are equivalent to the supplied expectation.
// At the end of the call, the recording will be disposed, so the next ExpectEvents call will not check against the same events again.
func (a *MockAuditor) ExpectEvents(t *testing.T, expectedEvents ...cadf.Event) {
	t.Helper()
	if len(expectedEvents) == 0 {
		expectedEvents = nil
	} else {
		for idx, event := range expectedEvents {
			expectedEvents[idx] = a.normalize(event)
		}
	}
	assert.DeepEqual(t, "CADF events", a.events, expectedEvents)

	// reset state for next test
	a.events = nil
}

// IgnoreEventsUntilNow clears the list of recorded events, so that the next
// ExpectEvents() will only cover events generated after this point.
func (a *MockAuditor) IgnoreEventsUntilNow() {
	a.events = nil
}

func (a *MockAuditor) normalize(event cadf.Event) cadf.Event {
	// overwrite some attributes where we don't care about variance
	event.TypeURI = "http://schemas.dmtf.org/cloud/audit/1.0/event"
	event.ID = "00000000-0000-0000-0000-000000000000"
	event.EventTime = "2006-01-02T15:04:05.999999+00:00"
	event.EventType = "activity"
	if event.Initiator.TypeURI == standardUserInfoTypeURI {
		// we do not care about the Initiator unless it's a NonStandardUserInfo
		event.Initiator = cadf.Resource{}
	}
	event.Observer = cadf.Resource{}
	return event
}