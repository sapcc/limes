// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

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
