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

	"github.com/sapcc/limes/internal/db"
)

// Filter describes query parameters that can be sent to various GET endpoints
// to filter the reports generated by this package.
type Filter struct {
	ServiceTypes  []string
	ResourceNames []string

	WithSubresources  bool
	WithSubcapacities bool
	WithAZBreakdown   bool

	IsSubcapacityAllowed func(serviceType, resourceName string) bool
}

// ReadFilter extracts a Filter from the given Request.
func ReadFilter(r *http.Request, getServiceTypesForArea func(string) []string) Filter {
	var (
		f  Filter
		ok bool
	)
	queryValues := r.URL.Query()
	if f.ServiceTypes, ok = queryValues["service"]; !ok {
		f.ServiceTypes = nil
	}
	if f.ResourceNames, ok = queryValues["resource"]; !ok {
		f.ResourceNames = nil
	}
	if _, ok := r.URL.Query()["detail"]; ok {
		f.WithSubresources = ok
		f.WithSubcapacities = ok
	}
	f.WithAZBreakdown = strings.Contains(r.Header.Get("X-Limes-V2-API-Preview"), "per-az")

	if areas, ok := queryValues["area"]; ok {
		var areaServices []string
		for _, area := range areas {
			areaServices = append(areaServices, getServiceTypesForArea(area)...)
		}

		if len(f.ServiceTypes) == 0 {
			//convert area filter into service filter by finding all services in these areas
			f.ServiceTypes = areaServices
		} else {
			//restrict services filter using the area filter
			isAreaService := make(map[string]bool, len(areaServices))
			for _, serviceType := range areaServices {
				isAreaService[serviceType] = true
			}
			var filteredServiceTypes []string
			for _, serviceType := range f.ServiceTypes {
				if isAreaService[serviceType] {
					filteredServiceTypes = append(filteredServiceTypes, serviceType)
				}
			}
			f.ServiceTypes = filteredServiceTypes
		}

		//if the given areas do not exist, insert a bogus service type now because
		//`f.serviceTypes == nil` will be misinterpreted as "no filter"
		if len(f.ServiceTypes) == 0 {
			f.ServiceTypes = []string{""}
		}
	}

	//by default, all subcapacities can be included, but the caller can restrict this based on AuthZ
	f.IsSubcapacityAllowed = func(string, string) bool { return true }

	return f
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
		values := f.ServiceTypes
		if match[2] == "resource_name" {
			values = f.ResourceNames
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
