// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"encoding/json"
	"time"
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
