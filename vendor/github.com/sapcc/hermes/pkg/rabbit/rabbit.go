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

package rabbit

import (
	"encoding/json"
	"errors"

	"github.com/sapcc/hermes/pkg/cadf"
	"github.com/streadway/amqp"
)

// DeclareQueue is a wrapper around *amqp.Channel.QueueDeclare. It declares a
// Queue with parameters expected by Hermes' RabbitMQ deployment.
func DeclareQueue(ch *amqp.Channel, queueName string) (amqp.Queue, error) {
	return ch.QueueDeclare(
		queueName, // name of the queue
		false,     // durable: queue should survive cluster reset (or broker restart)
		false,     // autodelete when unused
		false,     // exclusive: queue only accessible by connection that declares and deleted when the connection closes
		false,     // noWait: the queue will assume to be declared on the server
		nil,       // arguments for advanced config
	)
}

// PublishEvent is a wrapper around *amqp.Channel.Publish. It publishes a
// cadf.Event to the specified Queue with parameters expected by Hermes'
// RabbitMQ deployment.
// A nil pointer for event parameter will result in an error.
func PublishEvent(ch *amqp.Channel, queueName string, event *cadf.Event) error {
	if event == nil {
		return errors.New("rabbit: could not publish event: got a nil pointer for 'event' parameter")
	}
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return ch.Publish(
		"",        // exchange: publish to default
		queueName, // routing key: same as queue name
		false,     // mandatory: don't publish if no queue is bound that matches the routing key
		false,     // immediate: don't publish if no consumer on the matched queue is ready to accept the delivery
		amqp.Publishing{
			ContentType: "text/plain",
			Body:        b,
		},
	)
}
