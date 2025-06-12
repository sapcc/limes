// SPDX-FileCopyrightText: 2017-2020 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package limes

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sapcc/go-api-declarations/liquid"
)

// Unit enumerates allowed values for the unit a resource's quota/usage is
// measured in.
type Unit = liquid.Unit

const (
	UnitNone        Unit = liquid.UnitNone
	UnitBytes       Unit = liquid.UnitBytes
	UnitKibibytes   Unit = liquid.UnitKibibytes
	UnitMebibytes   Unit = liquid.UnitMebibytes
	UnitGibibytes   Unit = liquid.UnitGibibytes
	UnitTebibytes   Unit = liquid.UnitTebibytes
	UnitPebibytes   Unit = liquid.UnitPebibytes
	UnitExbibytes   Unit = liquid.UnitExbibytes
	UnitUnspecified Unit = liquid.UnitUnspecified
)

// ParseInUnit parses the string representation of a value with this unit
// (or any unit that can be converted to it).
//
//	ParseInUnit(UnitMebibytes, "10 MiB") -> 10
//	ParseInUnit(UnitMebibytes, "10 GiB") -> 10240
//	ParseInUnit(UnitMebibytes, "10 KiB") -> returns FractionalValueError
//	ParseInUnit(UnitMebibytes, "10")     -> returns syntax error (missing unit)
//	ParseInUnit(UnitNone, "42")          -> 42
//	ParseInUnit(UnitNone, "42 MiB")      -> returns syntax error (unexpected unit)
func ParseInUnit(u Unit, str string) (uint64, error) {
	// for countable resources, expect a number only
	if u == UnitNone {
		return strconv.ParseUint(strings.TrimSpace(str), 10, 64)
	}

	fields := strings.Fields(str)
	// Measured resources are a number and unit with space.
	if len(fields) != 2 {
		return 0, fmt.Errorf("value %q does not match expected format \"<number> <unit>\"", str)
	}

	number, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q: %s", str, err.Error())
	}
	value := ValueWithUnit{
		Value: number,
		// no need to validate unit string here; that will happen implicitly during .ConvertTo()
		Unit: Unit(fields[1]),
	}
	converted, err := value.ConvertTo(u)
	return converted.Value, err
}

// ValueWithUnit is used to represent values with units in subresources.
type ValueWithUnit struct {
	Value uint64 `json:"value" yaml:"value"`
	Unit  Unit   `json:"unit"  yaml:"unit"`
}

// String appends the unit (if any) to the given value. This should only be used
// for error messages; actual UIs should be more clever about formatting values
// (e.g. ValueWithUnit{1048576,UnitMebibytes}.String() returns "1048576 MiB"
// where "1 TiB" would be more appropriate).
func (v ValueWithUnit) String() string {
	str := strconv.FormatUint(v.Value, 10)
	if v.Unit == UnitNone {
		return str
	}
	return str + " " + string(v.Unit)
}

// ConvertTo returns an equal value in the given Unit. IncompatibleUnitsError is
// returned if the source unit cannot be converted into the target unit.
// FractionalValueError is returned if the conversion does not yield an integer
// value in the new unit.
func (v ValueWithUnit) ConvertTo(u Unit) (ValueWithUnit, error) {
	if v.Unit == u {
		return v, nil
	}

	base, sourceMultiple := v.Unit.Base()
	base2, targetMultiple := u.Base()
	if base != base2 {
		return ValueWithUnit{}, IncompatibleUnitsError{Source: v.Unit, Target: u}
	}

	valueInBase := v.Value * sourceMultiple
	if valueInBase%targetMultiple != 0 {
		return ValueWithUnit{}, FractionalValueError{Source: v, Target: u}
	}

	return ValueWithUnit{
		Value: valueInBase / targetMultiple,
		Unit:  u,
	}, nil
}

// IncompatibleUnitsError is returned by ValueWithUnit.ConvertTo() when the
// original and target unit are incompatible and cannot be converted into each
// other.
type IncompatibleUnitsError struct {
	Source Unit
	Target Unit
}

// Error implements the error interface.
func (e IncompatibleUnitsError) Error() string {
	return "cannot convert value from " + toStringForError(e.Source) +
		" to " + toStringForError(e.Target) +
		" because units are incompatible"
}

// FractionalValueError is returned by ValueWithUnit.ConvertTo() when the
// conversion would yield a fractional value in the target unit.
type FractionalValueError struct {
	Source ValueWithUnit
	Target Unit
}

// Error implements the error interface.
func (e FractionalValueError) Error() string {
	return fmt.Sprintf("value of %s cannot be represented as integer number of %s",
		e.Source.String(), e.Target,
	)
}

func toStringForError(u Unit) string {
	if string(u) == "" {
		return "<count>"
	}
	return string(u)
}
