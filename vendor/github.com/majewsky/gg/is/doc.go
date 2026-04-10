// SPDX-FileCopyrightText: 2025 Stefan Majewsky <majewsky@gmx.net>
// SPDX-License-Identifier: Apache-2.0

// Package is contains functions to express binary operations in a curried style.
// For example, "foo < bar" can be rewritten as "is.LessThan(bar)(foo)".
//
// This is not useful on its own, but may significantly improve readability
// when replacing function literals. Consider the following example:
//
//	import . "github.com/majewsky/gg/option"
//
//	func checkNewVolumeSize(size, usage uint64, maxSize Option[uint64]) error {
//		switch {
//		case size < usage:
//			return errors.New("size cannot be smaller than usage")
//		case maxSize.IsSomeAnd(func(value uint64) bool { return maxSize < size }):
//			return errors.New("size cannot be larger than maximum")
//		}
//	}
//
// The IsSomeAnd() check is difficult to read because function literals in Go are clunky.
// This can be rewritten in a clearer way using is.LessThan():
//
//	// original
//	case maxSize.IsSomeAnd(func(value uint64) bool { return maxSize < size }):
//	// rewritten
//	case maxSize.IsSomeAnd(is.LessThan(size)):
package is
