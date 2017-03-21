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

import "github.com/prometheus/client_golang/prometheus"

var scrapeSuccessCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_successful_scrapes",
		Help: "Counter for successful scrape operations per Keystone project.",
	},
	[]string{"cluster", "service"},
)

var scrapeFailedCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_failed_scrapes",
		Help: "Counter for failed scrape operations per Keystone project.",
	},
	[]string{"cluster", "service"},
)

func init() {
	prometheus.MustRegister(scrapeSuccessCounter)
	prometheus.MustRegister(scrapeFailedCounter)
}
