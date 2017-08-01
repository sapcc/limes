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
	"time"

	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

//Collector provides methods that implement the collection jobs performed by
//limes-collect. The struct contains references to the driver used, the plugin
//(which defines the service type to be targeted), and a few other things;
//basically everything that needs to be replaced by a mock implementation for
//the collector's unit tests.
type Collector struct {
	Cluster *limes.Cluster
	Plugin  limes.QuotaPlugin
	//Usually util.LogError, but can be changed inside unit tests.
	LogError func(msg string, args ...interface{})
	//Usually time.Now, but can be changed inside unit tests.
	TimeNow func() time.Time
	//When set to true, suppresses the usual non-returning behavior of
	//collector jobs.
	Once bool
}

//NewCollector creates a Collector instance.
func NewCollector(cluster *limes.Cluster, plugin limes.QuotaPlugin, cfg limes.CollectorConfiguration) *Collector {
	return &Collector{
		Cluster:  cluster,
		Plugin:   plugin,
		LogError: util.LogError,
		TimeNow:  time.Now,
		Once:     false,
	}
}
