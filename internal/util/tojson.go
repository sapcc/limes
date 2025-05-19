/*******************************************************************************
*
* Copyright 2025 SAP SE
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

package util

import (
	"encoding/json"
	"fmt"
)

// RenderListToJSON is used to fill DB fields containing JSON lists.
// Empty lists are represented as the empty string.
func RenderListToJSON[T any](attribute string, entries []T) (string, error) {
	if len(entries) == 0 {
		return "", nil
	}
	buf, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("could not convert %s to JSON: %w", attribute, err)
	}
	return string(buf), nil
}

// RenderMapToJSON is used to fill DB fields containing JSON maps.
// Empty maps are represented as the empty string.
func RenderMapToJSON[T ~string, U any](attribute string, entries map[T]U) (string, error) {
	if len(entries) == 0 {
		return "", nil
	}
	buf, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("could not convert %s to JSON: %w", attribute, err)
	}
	return string(buf), nil
}
