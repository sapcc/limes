/*******************************************************************************
*
* Copyright 2018-2019 SAP SE
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
	"time"

	"github.com/sapcc/hermes/pkg/rabbit"
	"github.com/streadway/amqp"
)

//rabbitConnection represents a unique connection to some RabbitMQ server with
//an open Channel and a declared Queue.
type rabbitConnection struct {
	conn *amqp.Connection
	ch   *amqp.Channel
	q    amqp.Queue

	isConnected bool
	connectedAt time.Time
}

func (r *rabbitConnection) connect(uri, queueName string) error {
	var err error

	//establish a connection with the RabbitMQ server
	r.conn, err = amqp.Dial(uri)
	if err != nil {
		return fmt.Errorf("RabbitMQ: failed to establish a connection with the server: %s", err.Error())
	}
	r.connectedAt = time.Now()

	//open a unique, concurrent server channel to process the bulk of AMQP messages
	r.ch, err = r.conn.Channel()
	if err != nil {
		return fmt.Errorf("RabbitMQ: failed to open a channel: %s", err.Error())
	}

	//declare a queue to hold and deliver messages to consumers
	r.q, err = rabbit.DeclareQueue(r.ch, queueName)
	if err != nil {
		return fmt.Errorf("RabbitMQ: failed to declare a queue: %s", err.Error())
	}

	r.isConnected = true

	return nil
}

func (r *rabbitConnection) disconnect() {
	r.ch.Close()
	r.conn.Close()
	r.isConnected = false
}
