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
	"sort"
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
	Driver   limes.Driver
	Plugin   limes.Plugin
	logError func(msg string, args ...interface{})
	timeNow  func() time.Time
	//once can be set to false to suppress the usual non-returning behavior of
	//collector jobs
	once bool
}

//NewCollector creates a Collector instance.
func NewCollector(driver limes.Driver, plugin limes.Plugin) *Collector {
	return &Collector{
		Driver:   driver,
		Plugin:   plugin,
		logError: util.LogError,
		timeNow:  time.Now,
		once:     false,
	}
}

func (c *Collector) enumerateEnabledServices() (asList []string, asMap map[string]bool) {
	asMap = make(map[string]bool)
	for _, service := range c.Driver.Cluster().Services {
		asMap[service.Type] = true
		asList = append(asList, service.Type)
	}
	sort.Strings(asList) //determinism is useful for unit tests
	return
}
