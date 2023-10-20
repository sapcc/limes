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

package reports

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
)

func pointerTo[T any](value T) *T {
	return &value
}

func unwrapOrDefault[T any](value *T, defaultValue T) T {
	if value == nil {
		return defaultValue
	}
	return *value
}

func mergeMinTime(lhs *limes.UnixEncodedTime, rhs *time.Time) *limes.UnixEncodedTime {
	if rhs == nil {
		return lhs
	}
	if lhs == nil || lhs.After(*rhs) {
		val := limes.UnixEncodedTime{Time: *rhs}
		return &val
	}
	return lhs
}

func mergeMaxTime(lhs *limes.UnixEncodedTime, rhs *time.Time) *limes.UnixEncodedTime {
	if rhs == nil {
		return lhs
	}
	if lhs == nil || lhs.Before(*rhs) {
		val := limes.UnixEncodedTime{Time: *rhs}
		return &val
	}
	return lhs
}

// Merges subcapacities and subresources across AZs.
// Each successive `input` is a JSON list without whitespace like
//
//	[{"entry":1},{"entry":2}]
//
// and the target is a similar JSON list with all entries concatenated in order.
// This could also be done by unmarshalling into []any, concatenating and remarshalling,
// but this implementation is more efficient.
func mergeJSONListInto(target *json.RawMessage, input string) {
	if input == "" || input == "[]" {
		return
	}
	if string(*target) == "" || string(*target) == "[]" {
		*target = json.RawMessage(input)
		return
	}
	*target = json.RawMessage(fmt.Sprintf("%s,%s",
		strings.TrimSuffix(string(*target), "]"),
		strings.TrimPrefix(input, "["),
	))
}
