// SPDX-FileCopyrightText: 2025 Stefan Majewsky <majewsky@gmx.net>
// SPDX-License-Identifier: Apache-2.0

package is

import "cmp"

// Above(b)(a) is the same as a > b.
func Above[T cmp.Ordered](rhs T) func(T) bool {
	return func(lhs T) bool {
		return lhs > rhs
	}
}

// Below(b)(a) is the same as a < b.
func Below[T cmp.Ordered](rhs T) func(T) bool {
	return func(lhs T) bool {
		return lhs < rhs
	}
}

// NotAbove(b)(a) is the same as a <= b.
func NotAbove[T cmp.Ordered](rhs T) func(T) bool {
	return func(lhs T) bool {
		return lhs <= rhs
	}
}

// NotBelow(b)(a) is the same as a >= b.
func NotBelow[T cmp.Ordered](rhs T) func(T) bool {
	return func(lhs T) bool {
		return lhs >= rhs
	}
}
