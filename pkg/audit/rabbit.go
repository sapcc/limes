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
	"time"

	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/streadway/amqp"
)

//sendEvents sends audit events to a RabbitMQ server
func sendEvents(config limes.CADFConfiguration, events []CADFevent) error {
	// establish a connection with the RabbitMQ server
	conn, err := amqp.Dial(config.RabbitMQ.URL)
	if err != nil {
		return fmt.Errorf("%s -- Failed to establish a connection with the server: %s", events[0].ID, err)
	}
	defer conn.Close()

	// open a unique, concurrent server channel to process the bulk of AMQP messages.
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("%s -- Failed to open a channel: %s", events[0].ID, err)
	}
	defer ch.Close()

	// declare a queue to hold and deliver messages to consumers.
	q, err := ch.QueueDeclare(
		config.RabbitMQ.QueueName, // name of the queue
		true,  // durable: queue should survive cluster reset (or broker restart)
		false, // autodelete when unused
		false, // exclusive: queue only accessible by connection that declares and deleted when the connection closes?
		false, // noWait: the queue will assume to be declared on the server
		nil,   // arguments for advanced config
	)
	if err != nil {
		return fmt.Errorf("%s -- Failed to declare a queue: %s", events[0].ID, err)
	}

	// publish the events to an exchange on the server
	for _, event := range events {
		body, _ := json.Marshal(event)
		err = ch.Publish(
			"",     // exchange: publish to default
			q.Name, // routing key: same as queue name
			false,  // mandatory: don't publish if no queue is bound that matches the routing key
			false,  // immediate: don't publish if no consumer on the matched queue is ready to accept the delivery
			amqp.Publishing{
				ContentType: "text/plain",
				Body:        []byte(body),
			},
		)
		if err != nil {
			return fmt.Errorf("%s -- Failed to publish the audit event: %s", event.ID, err)
		}
	}

	return err
}

//backoff creates a retry loop with an exponential backoff
func backoff(action func() error) {
	duration := time.Second
	for {
		err := action()
		if err != nil {
			logg.Error("RabbitMQ -- %s", err)
			if duration > 5*time.Minute {
				duration = 5 * time.Minute
			} else {
				duration *= 2
			}
			time.Sleep(duration)
			continue
		}
		break
	}
}
