// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

import "iter"

// ConstMap is a read-only wrapper around a Go map. It only exposes read operations
// (Get, GetOrZero, All, Keys, Len). Since the value type V is typically a
// non-pointer struct, Get() returns a copy of the value, making it safe
// for callers to modify the returned value without affecting the original.
//
// This is useful when sharing map data between goroutines without deep cloning:
// callers can read but not modify the shared maps.
type ConstMap[K comparable, V any] struct {
	m map[K]V
}

// NewConstMap wraps an existing map into a read-only ConstMap. The caller must
// not retain or modify the passed-in map after calling NewConstMap. This does
// not do a deep clone.
func NewConstMap[K comparable, V any](m map[K]V) ConstMap[K, V] {
	return ConstMap[K, V]{m: m}
}

// New2LevelConstMap is like NewConstMap, but with V as ConstMap.
func New2LevelConstMap[K1, K2 comparable, V any](m map[K1]map[K2]V) ConstMap[K1, ConstMap[K2, V]] {
	wrapped := make(map[K1]ConstMap[K2, V], len(m))
	for k, inner := range m {
		wrapped[k] = NewConstMap(inner)
	}
	return NewConstMap(wrapped)
}

// New3LevelConstMap is like NewConstMap, but with V as ConstMap of ConstMap.
func New3LevelConstMap[K1, K2, K3 comparable, V any](m map[K1]map[K2]map[K3]V) ConstMap[K1, ConstMap[K2, ConstMap[K3, V]]] {
	wrapped := make(map[K1]ConstMap[K2, ConstMap[K3, V]], len(m))
	for k, inner := range m {
		wrapped[k] = New2LevelConstMap(inner)
	}
	return NewConstMap(wrapped)
}

// Get returns the value for the given key and whether the key was found.
func (m ConstMap[K, V]) Get(key K) (V, bool) {
	v, ok := m.m[key]
	return v, ok
}

// GetOrZero returns the value for the given key, or the zero value of V if
// the key is not present.
func (m ConstMap[K, V]) GetOrZero(key K) V {
	return m.m[key]
}

// All returns an iterator over all key-value pairs. Iteration order is not
// guaranteed (same as Go's built-in map iteration).
func (m ConstMap[K, V]) All() iter.Seq2[K, V] {
	return func(yield func(K, V) bool) {
		for k, v := range m.m {
			if !yield(k, v) {
				return
			}
		}
	}
}

// Keys returns an iterator over all keys. Iteration order is not guaranteed.
func (m ConstMap[K, V]) Keys() iter.Seq[K] {
	return func(yield func(K) bool) {
		for k := range m.m {
			if !yield(k) {
				return
			}
		}
	}
}

// Values returns an iterator over all values. Iteration order is not guaranteed
func (m ConstMap[K, V]) Values() iter.Seq[V] {
	return func(yield func(V) bool) {
		for _, v := range m.m {
			if !yield(v) {
				return
			}
		}
	}
}

// Len returns the number of entries in the map.
func (m ConstMap[K, V]) Len() int {
	return len(m.m)
}
