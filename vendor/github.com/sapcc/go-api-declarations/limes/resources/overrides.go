/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package limesresources

import (
	"encoding/json"
	"fmt"

	"github.com/sapcc/go-api-declarations/limes"
)

// ParseQuotaOverrides parses the contents of a quota-overrides.json file.
// This is the format expected by Limes at $LIMES_QUOTA_OVERRIDES_PATH.
// This code lives here because it is also used in `limesctl validate-quota-overrides`.
func ParseQuotaOverrides(buf []byte, getUnit func(limes.ServiceType, ResourceName) (limes.Unit, error)) (result map[string]map[string]map[limes.ServiceType]map[ResourceName]uint64, errs []error) {
	var parsed map[string]map[string]map[limes.ServiceType]map[ResourceName]json.RawMessage
	err := json.Unmarshal(buf, &parsed)
	if err != nil {
		return nil, []error{err}
	}

	result = make(map[string]map[string]map[limes.ServiceType]map[ResourceName]uint64)
	for domainName, domainInputs := range parsed {
		domainResult := make(map[string]map[limes.ServiceType]map[ResourceName]uint64)
		for projectName, projectInputs := range domainInputs {
			projectResult := make(map[limes.ServiceType]map[ResourceName]uint64)
			for serviceType, serviceInputs := range projectInputs {
				serviceResult := make(map[ResourceName]uint64)
				for resourceName, inputJSON := range serviceInputs {
					unit, err := getUnit(serviceType, resourceName)
					if err != nil {
						errs = append(errs, err)
						continue
					}
					value, err := parseSingleQuotaOverrideValue(inputJSON, serviceType, resourceName, unit)
					if err == nil {
						serviceResult[resourceName] = value
					} else {
						errs = append(errs, err)
					}
				}
				projectResult[serviceType] = serviceResult
			}
			domainResult[projectName] = projectResult
		}
		result[domainName] = domainResult
	}
	return result, errs
}

func parseSingleQuotaOverrideValue(input json.RawMessage, serviceType limes.ServiceType, resourceName ResourceName, unit limes.Unit) (uint64, error) {
	// case 1: counted resources represent quota as a single number
	if unit == limes.UnitNone {
		var value uint64
		err := json.Unmarshal([]byte(input), &value)
		if err != nil {
			return 0, fmt.Errorf("expected uint64 value for %s/%s, but got %q", serviceType, resourceName, string(input))
		}
		return value, nil
	}

	// case 2: measured resources represent quota as a string of value with unit
	var value string
	err := json.Unmarshal([]byte(input), &value)
	if err != nil {
		return 0, fmt.Errorf("expected string field for %s/%s, but got %q", serviceType, resourceName, string(input))
	}
	parsedValue, err := unit.Parse(value)
	if err != nil {
		return 0, fmt.Errorf("in value for %s/%s: %w", serviceType, resourceName, err)
	}
	return parsedValue, nil
}
