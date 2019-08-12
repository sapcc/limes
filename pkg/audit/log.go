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

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/hermes/pkg/cadf"
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
		if config.Enabled {
			labels := prometheus.Labels{
				"os_cluster": clusterID,
			}
			eventPublishSuccessCounter.With(labels).Add(0)
			eventPublishFailedCounter.With(labels).Add(0)

			onSuccessFunc := func() {
				eventPublishSuccessCounter.With(labels).Inc()
			}
			onFailFunc := func() {
				eventPublishFailedCounter.With(labels).Inc()
			}
			eventSink := make(chan cadf.Event, 20)
			EventSinkPerCluster[clusterID] = eventSink

			go audittools.AuditTrail{
				EventSink:           eventSink,
				OnSuccessfulPublish: onSuccessFunc,
				OnFailedPublish:     onFailFunc,
			}.Commit(config.RabbitMQ.URL, config.RabbitMQ.QueueName)
		}
	}
}

//LogAndPublishEvent logs the audit event to stdout and publishes it to a RabbitMQ server.
func LogAndPublishEvent(clusterID string, event cadf.Event) {
	if showAuditOnStdout {
		msg, _ := json.Marshal(event)
		logg.Other("AUDIT", string(msg))
	}

	s := EventSinkPerCluster[clusterID]
	if s != nil {
		s <- event
	}
}
