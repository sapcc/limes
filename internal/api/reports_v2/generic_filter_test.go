// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"testing"

	"go.xyrillian.de/gg/assert"
	. "go.xyrillian.de/gg/option"
)

func TestHandleGenericFilters_AllEnabled(t *testing.T) {
	input := `SELECT * FROM table WHERE {{col1 = $foo}} AND {{col2 = $bar}}`
	opts := map[string]Option[string]{"foo": Some("bla"), "bar": Some("blub")}
	query, args := handleGenericFilters(opts, input, nil)
	assert.Equal(t, query, `SELECT * FROM table WHERE col1 = $1 AND col2 = $2`)
	assert.Equal(t, args, []any{"bla", "blub"})
}

func TestHandleGenericFilters_AllDisabled(t *testing.T) {
	input := `SELECT * FROM table WHERE {{col1 = $foo}} AND {{col2 = $bar}}`
	opts := map[string]Option[string]{"foo": None[string](), "bar": None[string]()}
	query, args := handleGenericFilters(opts, input, nil)
	assert.Equal(t, query, `SELECT * FROM table WHERE TRUE = TRUE AND TRUE = TRUE`)
	assert.Equal(t, args, []any(nil))
}

func TestHandleGenericFilters_Mixed(t *testing.T) {
	input := `SELECT * FROM table WHERE {{col1 = $foo}} AND {{col2 = $bar}}`
	opts := map[string]Option[string]{"foo": Some("bla"), "bar": None[string]()}
	query, args := handleGenericFilters(opts, input, nil)
	assert.Equal(t, query, `SELECT * FROM table WHERE col1 = $1 AND TRUE = TRUE`)
	assert.Equal(t, args, []any{"bla"})
}
