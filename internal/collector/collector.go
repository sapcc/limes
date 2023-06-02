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

package collector

import (
	"math/rand"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
)

// Collector provides methods that implement the collection jobs performed by
// limes-collect. The struct contains references to the driver used, the plugin
// (which defines the service type to be targeted), and a few other things;
// basically everything that needs to be replaced by a mock implementation for
// the collector's unit tests.
type Collector struct {
	Cluster *core.Cluster
	DB      *gorp.DbMap
	//Usually logg.Error, but can be changed inside unit tests.
	LogError func(msg string, args ...interface{})
	//Usually time.Now, but can be changed inside unit tests.
	TimeNow func() time.Time
	//Usually addJitter, but can be changed inside unit tests.
	AddJitter func(time.Duration) time.Duration
	//When set to true, suppresses the usual non-returning behavior of
	//collector jobs.
	Once bool
}

// NewCollector creates a Collector instance.
func NewCollector(cluster *core.Cluster, dbm *gorp.DbMap) *Collector {
	return &Collector{
		Cluster:   cluster,
		DB:        dbm,
		LogError:  logg.Error,
		TimeNow:   time.Now,
		AddJitter: addJitter,
		Once:      false,
	}
}

// addJitter returns a random duration within +/- 10% of the requested value.
// This can be used to even out the load on a scheduled job over time, by
// spreading jobs that would normally be scheduled right next to each other out
// over time without corrupting the individual schedules too much.
func addJitter(duration time.Duration) time.Duration {
	//nolint:gosec // This is not crypto-relevant, so math/rand is okay.
	r := rand.Float64() //NOTE: 0 <= r < 1
	return time.Duration(float64(duration) * (0.9 + 0.2*r))
}
