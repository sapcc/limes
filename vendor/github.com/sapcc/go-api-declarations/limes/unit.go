/*******************************************************************************
*
* Copyright 2017-2020 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package limes

import (
	"fmt"
	"strconv"
	"strings"
)

// Unit enumerates allowed values for the unit a resource's quota/usage is
// measured in.
type Unit string

const (
	//UnitNone is used for countable (rather than measurable) resources.
	UnitNone Unit = ""
	//UnitBytes is exactly that.
	UnitBytes Unit = "B"
	//UnitKibibytes is exactly that.
	UnitKibibytes Unit = "KiB"
	//UnitMebibytes is exactly that.
	UnitMebibytes Unit = "MiB"
	//UnitGibibytes is exactly that.
	UnitGibibytes Unit = "GiB"
	//UnitTebibytes is exactly that.
	UnitTebibytes Unit = "TiB"
	//UnitPebibytes is exactly that.
	UnitPebibytes Unit = "PiB"
	//UnitExbibytes is exactly that.
	UnitExbibytes Unit = "EiB"
	//UnitUnspecified is used as a placeholder when the unit is not known.
	UnitUnspecified Unit = "UNSPECIFIED"
)

var allValidUnits = []Unit{
	UnitNone,
	UnitBytes,
	UnitKibibytes,
	UnitMebibytes,
	UnitGibibytes,
	UnitTebibytes,
	UnitPebibytes,
	UnitExbibytes,
}

// UnmarshalYAML implements the yaml.Unmarshaler interface. This method validates
// that units in the config file actually exist.
func (u *Unit) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	err := unmarshal(&s)
	if err != nil {
		return err
	}
	for _, unit := range allValidUnits {
		if string(unit) == s {
			*u = unit
			return nil
		}
	}
	return fmt.Errorf("unknown unit: %q", s)
}

// Base returns the base unit of this unit. For units defined as a multiple of
// another unit, that unit is the base unit. Otherwise, the same unit and a
// multiple of 1 is returned.
func (u Unit) Base() (Unit, uint64) { //nolint:gocritic // not necessary to name the results
	switch u {
	case UnitKibibytes:
		return UnitBytes, 1 << 10
	case UnitMebibytes:
		return UnitBytes, 1 << 20
	case UnitGibibytes:
		return UnitBytes, 1 << 30
	case UnitTebibytes:
		return UnitBytes, 1 << 40
	case UnitPebibytes:
		return UnitBytes, 1 << 50
	case UnitExbibytes:
		return UnitBytes, 1 << 60
	default:
		return u, 1
	}
}

// Parse parses the string representation of a value with this unit (or any unit
// that can be converted to it).
//
//	UnitMebibytes.Parse("10 MiB") -> 10
//	UnitMebibytes.Parse("10 GiB") -> 10240
//	UnitMebibytes.Parse("10 KiB") -> returns FractionalValueError
//	UnitMebibytes.Parse("10")     -> returns syntax error (missing unit)
//	UnitNone.Parse("42")          -> 42
//	UnitNone.Parse("42 MiB")      -> returns syntax error (unexpected unit)
func (u Unit) Parse(str string) (uint64, error) {
	//for countable resources, expect a number only
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
		//no need to validate unit string here; that will happen implicitly during .ConvertTo()
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
	return "cannot convert value from " + e.Source.toStringForError() +
		" to " + e.Target.toStringForError() +
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

func (u Unit) toStringForError() string {
	if string(u) == "" {
		return "<count>"
	}
	return string(u)
}
