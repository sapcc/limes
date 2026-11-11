// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	. "go.xyrillian.de/gg/option"
)

// RFC3339EncodedTime is a time.Time that marshals into JSON as RFC3339 timestamp.
type RFC3339EncodedTime struct {
	time.Time
}

// MarshalJSON implements the json.Marshaler interface.
func (t RFC3339EncodedTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format(time.RFC3339))
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (t *RFC3339EncodedTime) UnmarshalJSON(buf []byte) error {
	var s string
	err := json.Unmarshal(buf, &s)
	if err != nil {
		return err
	}
	t.Time, err = time.Parse(time.RFC3339, s)
	return err
}

// RFC3339NanoEncodedTime is a time.Time that marshals into JSON as RFC3339Nano timestamp.
type RFC3339NanoEncodedTime struct {
	time.Time
}

// IntoRFC3339EncodedTime converts a time.Time to an RFC3339EncodedTime.
func IntoRFC3339EncodedTime(t time.Time) RFC3339EncodedTime {
	return RFC3339EncodedTime{Time: t}
}

// FromRFC3339EncodedTime converts an RFC3339EncodedTime to a time.Time.
func FromRFC3339EncodedTime(t RFC3339EncodedTime) time.Time {
	return t.Time
}

// MarshalJSON implements the json.Marshaler interface.
func (t RFC3339NanoEncodedTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.UTC().Format(time.RFC3339Nano))
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (t *RFC3339NanoEncodedTime) UnmarshalJSON(buf []byte) error {
	var s string
	err := json.Unmarshal(buf, &s)
	if err != nil {
		return err
	}
	t.Time, err = time.Parse(time.RFC3339Nano, s)
	return err
}

// Uint64OrNegativeOne has all values of a uint64 and -1.
// All values < -1 will fail parsing.
type Uint64OrNegativeOne Option[uint64]

// UnmarshalJSON implements the json.Unmarshaler interface.
func (t *Uint64OrNegativeOne) UnmarshalJSON(buf []byte) error {
	if string(buf) == "null" {
		return errors.New("cannot unmarshal null to Uint64OrNegativeOne")
	}
	var value json.Number
	err := json.Unmarshal(buf, &value)
	if err != nil {
		return fmt.Errorf("cannot unmarshal to Uint64OrNegativeOne: %w", err)
	}
	stringValue := value.String()
	if stringValue != "" && stringValue[0] == '-' {
		negativeValue, err := strconv.ParseInt(stringValue, 10, 64)
		if err != nil {
			return fmt.Errorf("Uint64OrNegativeOne was negative but not parsable as int64 (was %q)", value)
		}
		if negativeValue == 0 {
			*t = Uint64OrNegativeOne(Some(uint64(negativeValue)))
			return nil
		}
		if negativeValue < -1 {
			return fmt.Errorf("Uint64OrNegativeOne must be >= -1 (was %q)", value)
		}
		*t = Uint64OrNegativeOne(None[uint64]())
	} else {
		positiveValue, err := strconv.ParseUint(stringValue, 10, 64)
		if err != nil {
			return fmt.Errorf("Uint64OrNegativeOne was not parsable as uint64 (was %q)", value)
		}
		*t = Uint64OrNegativeOne(Some(positiveValue))
	}
	return nil
}

// MarshalJSON implements the json.Marshaler interface.
func (t Uint64OrNegativeOne) MarshalJSON() ([]byte, error) {
	if value, ok := t.AsOption().Unpack(); ok {
		return json.Marshal(value)
	}
	return []byte("-1"), nil
}

// AsOption converts the value to an Option[uint64]:
// None represents "infinite / no limit" (-1 on the wire).
func (t Uint64OrNegativeOne) AsOption() Option[uint64] {
	return Option[uint64](t)
}
