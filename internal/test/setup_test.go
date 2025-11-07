// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"testing"

	"github.com/majewsky/gg/jsonmatch"
)

func TestRemoveCommentsFromJSON(t *testing.T) {
	jsonStr := `{
    "name": "test", // This is an inline comment
    // This is a single line comment
    "value": 42, // Another inline comment
    /* This is a multiline
      comment that spans
      multiple lines */
    "enabled": true, // Final inline comment
    // Another single line comment
    "config": {
      "debug": false /* inline multiline comment */
    }
  }`

	expected := jsonmatch.Object{
		"name":    "test",
		"value":   42,
		"enabled": true,
		"config": jsonmatch.Object{
			"debug": false,
		},
	}

	result := RemoveCommentsFromJSON(jsonStr)

	for _, diff := range expected.DiffAgainst([]byte(result)) {
		if diff.Pointer == "" {
			t.Errorf("%s: expected %s, but got %s", diff.Kind, diff.ExpectedJSON, diff.ActualJSON)
		} else {
			t.Errorf("%s at %s: expected %s, but got %s", diff.Kind, diff.Pointer, diff.ExpectedJSON, diff.ActualJSON)
		}
	}
}
