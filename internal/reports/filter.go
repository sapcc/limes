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

package reports

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"

	"github.com/sapcc/limes/internal/db"
)

// Filter describes query parameters that can be sent to various GET endpoints
// to filter the reports generated by this package.
type Filter struct {
	ServiceTypes  []limes.ServiceType
	ResourceNames []limesresources.ResourceName

	WithSubresources  bool
	WithSubcapacities bool
	WithAZBreakdown   bool

	IsSubcapacityAllowed func(serviceType limes.ServiceType, resourceName limesresources.ResourceName) bool
}

// ReadFilter extracts a Filter from the given Request.
func ReadFilter(r *http.Request, getServiceTypesForArea func(string) []limes.ServiceType) Filter {
	queryValues := r.URL.Query()
	f := Filter{
		ServiceTypes:  castStringsTo[limes.ServiceType](queryValues["service"]),
		ResourceNames: castStringsTo[limesresources.ResourceName](queryValues["resource"]),
	}
	if _, ok := r.URL.Query()["detail"]; ok {
		f.WithSubresources = ok
		f.WithSubcapacities = ok
	}
	f.WithAZBreakdown = strings.Contains(r.Header.Get("X-Limes-V2-API-Preview"), "per-az")

	if areas, ok := queryValues["area"]; ok {
		var areaServices []limes.ServiceType
		for _, area := range areas {
			areaServices = append(areaServices, getServiceTypesForArea(area)...)
		}

		if len(f.ServiceTypes) == 0 {
			// convert area filter into service filter by finding all services in these areas
			f.ServiceTypes = areaServices
		} else {
			// restrict services filter using the area filter
			isAreaService := make(map[limes.ServiceType]bool, len(areaServices))
			for _, serviceType := range areaServices {
				isAreaService[serviceType] = true
			}
			var filteredServiceTypes []limes.ServiceType
			for _, serviceType := range f.ServiceTypes {
				if isAreaService[serviceType] {
					filteredServiceTypes = append(filteredServiceTypes, serviceType)
				}
			}
			f.ServiceTypes = filteredServiceTypes
		}

		// if the given areas do not exist, insert a bogus service type now because
		// `f.serviceTypes == nil` will be misinterpreted as "no filter"
		if len(f.ServiceTypes) == 0 {
			f.ServiceTypes = []limes.ServiceType{""}
		}
	}

	// by default, all subcapacities can be included, but the caller can restrict this based on AuthZ
	f.IsSubcapacityAllowed = func(limes.ServiceType, limesresources.ResourceName) bool { return true }

	return f
}

func castStringsTo[O ~string, I ~string](input []I) (output []O) {
	output = make([]O, len(input))
	for idx, val := range input {
		output[idx] = O(val)
	}
	return
}

var filterPrepareRx = regexp.MustCompile(`{{AND ([a-z._]+) = \$(service_type|resource_name)}}`)

// PrepareQuery takes a SQL query string, and replaces the following
// placeholders with the values in this Filter:
//
//	{{AND some_table.some_field = $service_type}}
//	{{AND some_table.some_field = $resource_name}}
func (f Filter) PrepareQuery(query string) (preparedQuery string, args []any) {
	preparedQuery = filterPrepareRx.ReplaceAllStringFunc(query, func(matchStr string) string {
		match := filterPrepareRx.FindStringSubmatch(matchStr)
		values := castStringsTo[string](f.ServiceTypes)
		if match[2] == "resource_name" {
			values = castStringsTo[string](f.ResourceNames)
		}

		if len(values) == 0 {
			return ""
		}

		whereStr, queryArgs := db.BuildSimpleWhereClause(map[string]any{match[1]: values}, len(args))
		args = append(args, queryArgs...)
		return "AND " + whereStr
	})

	return
}
