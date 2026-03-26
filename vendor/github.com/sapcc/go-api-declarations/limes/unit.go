// SPDX-FileCopyrightText: 2017-2020 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package limes

import (
	"github.com/sapcc/go-api-declarations/internal/units"
)

// Unit represents the unit a resource or rate is measured in.
type Unit = units.Unit

var (
	// UnitNone is used for countable (rather than measurable) resources or rates.
	UnitNone = units.UnitNone

	// UnitBytes is exactly that. Its MultiplyBy() method can be used to instantiate non-standard units.
	UnitBytes = units.UnitBytes
	// UnitKibibytes is exactly that. Its MultiplyBy() method can be used to instantiate non-standard units.
	UnitKibibytes = units.UnitKibibytes
	// UnitMebibytes is exactly that. Its MultiplyBy() method can be used to instantiate non-standard units.
	UnitMebibytes = units.UnitMebibytes
	// UnitGibibytes is exactly that. Its MultiplyBy() method can be used to instantiate non-standard units.
	UnitGibibytes = units.UnitGibibytes
	// UnitTebibytes is exactly that. Its MultiplyBy() method can be used to instantiate non-standard units.
	UnitTebibytes = units.UnitTebibytes
	// UnitPebibytes is exactly that. Its MultiplyBy() method can be used to instantiate non-standard units.
	UnitPebibytes = units.UnitPebibytes
	// UnitExbibytes is exactly that. Its MultiplyBy() method can be used to instantiate non-standard units.
	UnitExbibytes = units.UnitExbibytes
)

// ParseInUnit parses the string representation of a value with this unit
// (or any unit that can be converted to it).
//
//	ParseInUnit(UnitMebibytes, "10 MiB") -> 10
//	ParseInUnit(UnitMebibytes, "10 GiB") -> 10240
//	ParseInUnit(UnitMebibytes, "10 KiB") -> error: incompatible unit
//	ParseInUnit(UnitMebibytes, "10")     -> error: missing unit
//	ParseInUnit(UnitNone, "42")          -> 42
//	ParseInUnit(UnitNone, "42 MiB")      -> error: unexpected unit
func ParseInUnit(u Unit, str string) (uint64, error) {
	return units.ParseInUnit(u, str)
}

// ValueWithUnit is used to represent values with units in subresources.
type ValueWithUnit = units.LimesV1ValueWithUnit
