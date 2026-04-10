// SPDX-FileCopyrightText: 2025 Stefan Majewsky <majewsky@gmx.net>
// SPDX-License-Identifier: Apache-2.0

package is

// EqualTo(b)(a) is the same as a == b.
func EqualTo[T comparable](rhs T) func(T) bool {
	return func(lhs T) bool {
		return lhs == rhs
	}
}

// DifferentFrom(b)(a) is the same as a != b.
func DifferentFrom[T comparable](rhs T) func(T) bool {
	return func(lhs T) bool {
		return lhs != rhs
	}
}
