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
	"log"
	"os"
	"time"

	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/retry"
	"github.com/sapcc/limes/pkg/core"
)

func init() {
	log.SetOutput(os.Stdout)
	if os.Getenv("LIMES_DEBUG") == "1" {
		logg.ShowDebug = true
	}
}

//Trail is a list of CADF formatted events with log level AUDIT. It has a separate interface
//from the rest of the logging to allow to withhold the logging until DB changes are committed.
type Trail struct {
	events []CADFEvent
}

//Add adds an event to the audit trail.
func (t *Trail) Add(p EventParams) {
	event := p.newEvent()
	t.events = append(t.events, event)
}

//Commit sends the whole audit trail into the log. Call this after tx.Commit().
func (t *Trail) Commit(clusterID string, config core.CADFConfiguration) {
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
