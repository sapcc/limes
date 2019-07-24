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
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/hermes/pkg/cadf"
	"github.com/sapcc/hermes/pkg/rabbit"
	"github.com/sapcc/limes/pkg/core"
	"github.com/streadway/amqp"
)

//sendEvents sends audit events to a RabbitMQ server.
func sendEvents(clusterID string, config core.CADFConfiguration, events []cadf.Event) error {
	labels := prometheus.Labels{
		"os_cluster": clusterID,
	}
	eventPublishSuccessCounter.With(labels).Add(0)
	eventPublishFailedCounter.With(labels).Add(0)

	//establish a connection with the RabbitMQ server
	conn, err := amqp.Dial(config.RabbitMQ.URL)
	if err != nil {
		eventPublishFailedCounter.With(labels).Inc()
		return fmt.Errorf("RabbitMQ -- %s -- Failed to establish a connection with the server: %s", events[0].ID, err)
	}
	defer conn.Close()

	//open a unique, concurrent server channel to process the bulk of AMQP messages
	ch, err := conn.Channel()
	if err != nil {
		eventPublishFailedCounter.With(labels).Inc()
		return fmt.Errorf("RabbitMQ -- %s -- Failed to open a channel: %s", events[0].ID, err)
	}
	defer ch.Close()

	//declare a queue to hold and deliver messages to consumers
	q, err := rabbit.DeclareQueue(ch, config.RabbitMQ.QueueName)
	if err != nil {
		eventPublishFailedCounter.With(labels).Inc()
		return fmt.Errorf("RabbitMQ -- %s -- Failed to declare a queue: %s", events[0].ID, err)
	}

	//publish the events to an exchange on the server
	for _, event := range events {
		err := rabbit.PublishEvent(ch, q.Name, &event)
		if err != nil {
			eventPublishFailedCounter.With(labels).Inc()
			return fmt.Errorf("RabbitMQ -- %s -- Failed to publish the audit event: %s", event.ID, err)
		}
		eventPublishSuccessCounter.With(labels).Inc()
	}

	return err
}
