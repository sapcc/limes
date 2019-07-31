/*******************************************************************************
*
* Copyright 2017-2019 SAP SE
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
	"log"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/hermes/pkg/cadf"
	"github.com/sapcc/hermes/pkg/rabbit"
	"github.com/sapcc/limes/pkg/core"
)

func init() {
	log.SetOutput(os.Stdout)
	if os.Getenv("LIMES_DEBUG") == "1" {
		logg.ShowDebug = true
	}
}

var showAuditOnStdout = os.Getenv("LIMES_SILENT") != "1"

//EventSinkPerCluster is a map of cluster ID to a channel that receives audit events.
var EventSinkPerCluster map[string]chan<- cadf.Event

//Start starts the audit trail by initializing the EventSinkPerCluster
//and starting separate Commit() goroutines per Cluster.
func Start(configPerCluster map[string]core.CADFConfiguration) {
	EventSinkPerCluster = make(map[string]chan<- cadf.Event)
	for clusterID, config := range configPerCluster {
		eventSink := make(chan cadf.Event, 20)
		EventSinkPerCluster[clusterID] = eventSink
		go commit(clusterID, config, eventSink)
	}
}

//commit receives the audit events from an event sink channel and sends them to
//a RabbitMQ server as per the configuration.
func commit(clusterID string, config core.CADFConfiguration, eventSink <-chan cadf.Event) {
	labels := prometheus.Labels{
		"os_cluster": clusterID,
	}
	eventPublishSuccessCounter.With(labels).Add(0)
	eventPublishFailedCounter.With(labels).Add(0)

	rc := &rabbitConnection{}
	connect := func() {
		if !rc.isConnected {
			err := rc.connect(config.RabbitMQ.URL, config.RabbitMQ.QueueName)
			if err != nil {
				logg.Error(err.Error())
			}
		}
	}
	sendEvent := func(e *cadf.Event) bool {
		if !rc.isConnected {
			return false
		}
		err := rabbit.PublishEvent(rc.ch, rc.q.Name, e)
		if err != nil {
			eventPublishFailedCounter.With(labels).Inc()
			logg.Error("RabbitMQ: failed to publish audit event with ID %q: %s", e.ID, err.Error())
			return false
		}
		eventPublishSuccessCounter.With(labels).Inc()
		return true
	}

	var pendingEvents []cadf.Event

	ticker := time.Tick(1 * time.Minute)
	for {
		select {
		case e := <-eventSink:
			if showAuditOnStdout {
				msg, _ := json.Marshal(e)
				logg.Other("AUDIT", string(msg))
			}

			if config.Enabled {
				connect()
				if successful := sendEvent(&e); !successful {
					pendingEvents = append(pendingEvents, e)
				}
			}
		case <-ticker:
			if config.Enabled {
				for len(pendingEvents) > 0 {
					connect()
					successful := false //until proven otherwise
					nextEvent := pendingEvents[0]
					if successful = sendEvent(&nextEvent); !successful {
						//refresh connection, if old
						if time.Since(rc.connectedAt) > (5 * time.Minute) {
							rc.disconnect()
							connect()
						}
						time.Sleep(5 * time.Second)
						successful = sendEvent(&nextEvent) //one more try before giving up
					}

					if successful {
						pendingEvents = pendingEvents[1:]
					} else {
						break
					}
				}
			}
		}
	}
}
