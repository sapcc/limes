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

package marshal

import (
	"encoding/json"
	"sort"
)

// MapAsList marshals a map type into a flat JSON list, thereby discarding the keys.
func MapAsList[T any](vals map[string]T) ([]byte, error) {
	//serialize with ordered keys to ensure testcase stability
	names := make([]string, 0, len(vals))
	for name := range vals {
		names = append(names, name)
	}
	sort.Strings(names)
	list := make([]T, len(vals))
	for idx, name := range names {
		list[idx] = vals[name]
	}
	return json.Marshal(list)
}

// MapFromList unmarshals a flat JSON list into a map, using the provided
// predicate to obtain the keys for each item.
func MapFromList[T any](buf []byte, getKey func(T) string) (map[string]T, error) {
	var list []T
	err := json.Unmarshal(buf, &list)
	if err != nil {
		return nil, err
	}
	result := make(map[string]T, len(list))
	for _, item := range list {
		result[getKey(item)] = item
	}
	return result, nil
}
