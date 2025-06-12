// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package marshal

import (
	"encoding/json"
	"slices"
)

// MapAsList marshals a map type into a flat JSON list, thereby discarding the keys.
func MapAsList[S ~string, T any](vals map[S]T) ([]byte, error) {
	// serialize with ordered keys to ensure testcase stability
	names := make([]S, 0, len(vals))
	for name := range vals {
		names = append(names, name)
	}
	slices.Sort(names)
	list := make([]T, len(vals))
	for idx, name := range names {
		list[idx] = vals[name]
	}
	return json.Marshal(list)
}

// MapFromList unmarshals a flat JSON list into a map, using the provided
// predicate to obtain the keys for each item.
func MapFromList[S ~string, T any](buf []byte, getKey func(T) S) (map[S]T, error) {
	var list []T
	err := json.Unmarshal(buf, &list)
	if err != nil {
		return nil, err
	}
	result := make(map[S]T, len(list))
	for _, item := range list {
		result[getKey(item)] = item
	}
	return result, nil
}
