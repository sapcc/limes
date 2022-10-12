/*******************************************************************************
*
* Copyright 2022 SAP SE
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

package limesrates

import (
	"github.com/sapcc/go-api-declarations/limes"
)

// ClusterReport contains aggregated data about resource usage in a cluster.
// It is returned by GET endpoints for clusters.
type ClusterReport struct {
	limes.ClusterInfo
	Services ClusterServiceReports `json:"services"`
}

// ClusterServiceReport is a substructure of ClusterReport containing data for
// a single backend service.
type ClusterServiceReport struct {
	limes.ServiceInfo
	Rates        ClusterRateReports     `json:"rates,omitempty"`
	MaxScrapedAt *limes.UnixEncodedTime `json:"max_scraped_at,omitempty"`
	MinScrapedAt *limes.UnixEncodedTime `json:"min_scraped_at,omitempty"`
}

// ClusterRateReport is a substructure of ClusterServiceReport containing data
// for a single rate.
type ClusterRateReport struct {
	RateInfo
	Limit  uint64 `json:"limit,omitempty"`
	Window Window `json:"window,omitempty"`
}

// ClusterServiceReports provides fast lookup of services by service type, but
// serializes to JSON as a list.
type ClusterServiceReports map[string]*ClusterServiceReport

// ClusterRateReports provides fast lookup of rates using a map, but
// serializes to JSON as a list.
type ClusterRateReports map[string]*ClusterRateReport
