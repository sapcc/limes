// SPDX-FileCopyrightText: 2025 Stefan Majewsky <majewsky@gmx.net>
// SPDX-License-Identifier: Apache-2.0

package is

import "time"

// After(b)(a) is the same as a .After(b).
func After(rhs time.Time) func(time.Time) bool {
	return func(lhs time.Time) bool {
		return lhs.After(rhs)
	}
}

// Before(b)(a) is the same as a.Before(b).
func Before(rhs time.Time) func(time.Time) bool {
	return func(lhs time.Time) bool {
		return lhs.Before(rhs)
	}
}

// NotAfter(b)(a) is the same as !a.After(b).
func NotAfter(rhs time.Time) func(time.Time) bool {
	return func(lhs time.Time) bool {
		return !lhs.After(rhs)
	}
}

// NotBefore(b)(a) is the same as !a.Before(b).
func NotBefore(rhs time.Time) func(time.Time) bool {
	return func(lhs time.Time) bool {
		return !lhs.Before(rhs)
	}
}
