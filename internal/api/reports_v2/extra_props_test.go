// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"testing"

	"go.xyrillian.de/gg/assert"
)

func TestHandleProps_AllEnabled(t *testing.T) {
	input := `SELECT col1, $with_timing{{col2,}} $with_stats{{col3,}} col4 FROM table`
	opts := map[string]bool{"timing": true, "stats": true}
	result := handleProps(input, opts)
	assert.Equal(t, result, `SELECT col1, col2, col3, col4 FROM table`)
}

func TestHandleProps_AllDisabled(t *testing.T) {
	input := `SELECT col1, $with_timing{{col2,}} $with_stats{{col3,}} col4 FROM table`
	opts := map[string]bool{"timing": false, "stats": false}
	result := handleProps(input, opts)
	assert.Equal(t, result, `SELECT col1,   col4 FROM table`)
}

func TestHandleProps_Mixed(t *testing.T) {
	input := `SELECT col1, $with_timing{{col2,}} $with_stats{{col3,}} col4 FROM table`
	opts := map[string]bool{"timing": true, "stats": false}
	result := handleProps(input, opts)
	assert.Equal(t, result, `SELECT col1, col2,  col4 FROM table`)
}

func TestHandleProps_NestedBraces(t *testing.T) {
	input := `WITH $with_commitment_stats{{cte AS (SELECT x FROM t WHERE {{az_resource_id = ANY($az_resource_id)}} GROUP BY x),}} main AS (SELECT 1)`
	opts := map[string]bool{"commitment_stats": true}
	result := handleProps(input, opts)
	assert.Equal(t, result, `WITH cte AS (SELECT x FROM t WHERE {{az_resource_id = ANY($az_resource_id)}} GROUP BY x), main AS (SELECT 1)`)
}

func TestHandleProps_NestedBracesDisabled(t *testing.T) {
	input := `WITH $with_commitment_stats{{cte AS (SELECT x FROM t WHERE {{az_resource_id = ANY($az_resource_id)}} GROUP BY x),}} main AS (SELECT 1)`
	opts := map[string]bool{"commitment_stats": false}
	result := handleProps(input, opts)
	assert.Equal(t, result, `WITH  main AS (SELECT 1)`)
}

func TestHandleProps_MultipleNestedBlocks(t *testing.T) {
	input := `SELECT $with_a{{col_a,}} $with_b{{col_b, {{inner}},}} col_c FROM $with_a{{table_a JOIN}} table_b`
	opts := map[string]bool{"a": true, "b": false}
	result := handleProps(input, opts)
	assert.Equal(t, result, `SELECT col_a,  col_c FROM table_a JOIN table_b`)
}

func TestHandleProps_NoMarkers(t *testing.T) {
	input := `SELECT col1, col2 FROM table`
	opts := map[string]bool{}
	result := handleProps(input, opts)
	assert.Equal(t, result, `SELECT col1, col2 FROM table`)
}

func TestHandleProps_UnknownOptionPanics(t *testing.T) {
	input := `SELECT $with_unknown{{col1}} FROM table`
	opts := map[string]bool{"timing": true}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for unknown option, but did not panic")
		}
		msg, ok := r.(string)
		if !ok || msg != "unknown $with_ option: unknown" {
			t.Fatalf("unexpected panic value: %v", r)
		}
	}()
	handleProps(input, opts)
}
