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

import "github.com/sapcc/go-api-declarations/limes"

// ProjectReport contains all data about resource usage in a project.
type ProjectReport struct {
	limes.ProjectInfo
	Services ProjectServiceReports `json:"services"`
}

// ProjectServiceReport is a substructure of ProjectReport containing data for
// a single backend service.
type ProjectServiceReport struct {
	limes.ServiceInfo
	Rates     ProjectRateReports     `json:"rates,omitempty"`
	ScrapedAt *limes.UnixEncodedTime `json:"scraped_at,omitempty"`
}

// ProjectRateReport is a substructure of ProjectServiceReport containing data
// for a single rate.
type ProjectRateReport struct {
	RateInfo
	//NOTE: Both Window fields must have pointer types because omitempty does not
	// work directly on json.Marshaler-implementing types.
	Limit         uint64  `json:"limit,omitempty"`
	Window        *Window `json:"window,omitempty"`
	DefaultLimit  uint64  `json:"default_limit,omitempty"`
	DefaultWindow *Window `json:"default_window,omitempty"`
	UsageAsBigint string  `json:"usage_as_bigint,omitempty"`
}

// ProjectServiceReports provides fast lookup of services using a map, but serializes
// to JSON as a list.
type ProjectServiceReports map[limes.ServiceType]*ProjectServiceReport

// ProjectRateReports provides fast lookup of rates using a map, but serializes
// to JSON as a list.
type ProjectRateReports map[RateName]*ProjectRateReport
