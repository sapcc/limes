// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package limes

import (
	"encoding/json"
	"time"
)

// UnixEncodedTime is a time.Time that marshals into JSON as a UNIX timestamp.
//
// This is a single-member struct instead of a newtype because the former
// enables directly calling time.Time methods on this type, e.g. t.String()
// instead of time.Time(t).String().
type UnixEncodedTime struct {
	time.Time
}

// MarshalJSON implements the json.Marshaler interface.
func (t UnixEncodedTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.Unix())
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (t *UnixEncodedTime) UnmarshalJSON(buf []byte) error {
	var tst int64
	err := json.Unmarshal(buf, &tst)
	if err == nil {
		t.Time = time.Unix(tst, 0).UTC()
	}
	return err
}
