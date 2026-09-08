// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"maps"
	"slices"
	"testing"

	"go.xyrillian.de/gg/assert"
)

func TestConstMapGet(t *testing.T) {
	m := NewConstMap(map[string]int{"a": 1, "b": 2})

	v, ok := m.Get("a")
	assert.Equal(t, ok, true)
	assert.Equal(t, v, 1)

	v, ok = m.Get("missing")
	assert.Equal(t, ok, false)
	assert.Equal(t, v, 0)
}

func TestConstMapGetOrZero(t *testing.T) {
	m := NewConstMap(map[string]int{"a": 1})

	assert.Equal(t, m.GetOrZero("a"), 1)
	assert.Equal(t, m.GetOrZero("missing"), 0)
}

func TestConstMapAll(t *testing.T) {
	m := NewConstMap(map[string]int{"a": 1, "b": 2, "c": 3})

	assert.Equal(t, maps.Collect(m.All()), map[string]int{"a": 1, "b": 2, "c": 3})
}

func TestConstMapKeys(t *testing.T) {
	m := NewConstMap(map[string]int{"b": 2, "a": 1, "c": 3})

	keys := slices.Sorted(m.Keys())
	expected := []string{"a", "b", "c"}
	assert.Equal(t, keys, expected)
}

func TestConstMapValues(t *testing.T) {
	m := NewConstMap(map[string]int{"b": 2, "a": 1, "c": 3})
	values := slices.Sorted(m.Values())
	expected := []int{1, 2, 3}
	assert.Equal(t, values, expected)
}

func TestConstMapLen(t *testing.T) {
	m := NewConstMap(map[string]int{"a": 1, "b": 2})
	assert.Equal(t, m.Len(), 2)

	empty := NewConstMap(map[string]int{})
	assert.Equal(t, empty.Len(), 0)
}

func TestConstMapNil(t *testing.T) {
	m := NewConstMap[string, int](nil)
	assert.Equal(t, m.Len(), 0)
	assert.Equal(t, m.GetOrZero("a"), 0)
	_, ok := m.Get("a")
	assert.Equal(t, ok, false)

	count := 0
	for range m.All() {
		count++
	}
	assert.Equal(t, count, 0)
}

func TestConstMapValueCopySemantics(t *testing.T) {
	type val struct{ X int }
	m := NewConstMap(map[string]val{"a": {X: 1}})

	v, _ := m.Get("a")
	v.X = 999 //nolint:govet // unused write on purpose for testing

	// Original must be unchanged
	orig, _ := m.Get("a")
	assert.Equal(t, orig.X, 1)
}
