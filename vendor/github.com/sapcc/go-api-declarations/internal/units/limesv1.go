// SPDX-FileCopyrightText: 2017-2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package units

import (
	"fmt"
	"strconv"
	"strings"
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
	if u == UnitNone {
		return strconv.ParseUint(str, 10, 64)
	}

	amount, err := ParseAmount(str, NumberWithUnitFormat)
	if err != nil {
		return 0, err
	}
	value := LimesV1ValueWithUnit{
		Value: amount.Factor,
		Unit:  Unit{Amount{Base: amount.Base, Factor: 1}},
	}
	converted, err := value.ConvertTo(u)
	return converted.Value, err
}

// LimesV1ValueWithUnit is used to represent values with units in subresources.
// As the name implies, this type is only exposed in the Limes v1 API.
type LimesV1ValueWithUnit struct {
	Value uint64 `json:"value"`
	Unit  Unit   `json:"unit"`
}

// String implements the fmt.Stringer interface.
// The value is serialized with the most appropriate unit:
//
//	// prints: 1000000 MiB
//	fmt.Println(LimesV1ValueWithUnit{1000000,UnitMebibytes})
//	// prints: 1 TiB
//	fmt.Println(LimesV1ValueWithUnit{1048576,UnitMebibytes})
func (v LimesV1ValueWithUnit) String() string {
	amount, err := v.Unit.amount.MultiplyBy(v.Value)
	if err == nil {
		return amount.Format(NumberOnlyFormat | NumberWithUnitFormat)
	}

	// fallback: if converting to the base unit would overflow, print without conversion
	valueStr := strconv.FormatUint(v.Value, 10)
	if v.Unit == UnitNone {
		return valueStr
	} else {
		unitStr := v.Unit.amount.Format(UnitOnlyFormat | NumberWithUnitFormat)
		if strings.Contains(unitStr, " ") { // unit has a numeric multiplier by itself, e.g. "4 MiB"
			return valueStr + " x " + unitStr // e.g. "20 x 4 MiB"
		} else {
			return valueStr + " " + unitStr // e.g. "20 MiB"
		}
	}
}

// ConvertTo returns an equal value in the given Unit. IncompatibleUnitsError is
// returned if the source unit cannot be converted into the target unit.
// FractionalValueError is returned if the conversion does not yield an integer
// value in the new unit.
func (v LimesV1ValueWithUnit) ConvertTo(u Unit) (LimesV1ValueWithUnit, error) {
	if v.Unit == u {
		return v, nil
	}

	base, sourceMultiple := v.Unit.Base()
	base2, targetMultiple := u.Base()
	if base != base2 {
		return LimesV1ValueWithUnit{}, IncompatibleUnitsError{Source: v.Unit, Target: u}
	}

	valueInBase := v.Value * sourceMultiple
	if valueInBase%targetMultiple != 0 {
		return LimesV1ValueWithUnit{}, FractionalValueError{Source: v, Target: u}
	}

	return LimesV1ValueWithUnit{
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
	Source LimesV1ValueWithUnit
	Target Unit
}

// Error implements the error interface.
func (e FractionalValueError) Error() string {
	return fmt.Sprintf("value of %s cannot be represented as integer number of %s",
		e.Source.String(), toStringForError(e.Target),
	)
}

func toStringForError(u Unit) string {
	if u == UnitNone {
		return "<count>"
	}
	return u.String()
}
