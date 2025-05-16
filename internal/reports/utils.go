// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

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
