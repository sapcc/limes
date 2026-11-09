// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"encoding/json"
	"testing"
	"time"

	"go.xyrillian.de/gg/assert"

	. "go.xyrillian.de/gg/option"
)

func TestMarshalRFC3339Time(t *testing.T) {
	tst := time.Date(2024, 3, 15, 12, 30, 45, 500000000, time.UTC) // test that marshalling ignores the subsecond part
	u := RFC3339EncodedTime{Time: tst}

	buf, err := json.Marshal(u)
	if assert.ErrEqual(t, err, nil) {
		assert.Equal(t, string(buf), `"2024-03-15T12:30:45Z"`)
	}
}

func TestUnmarshalRFC3339Time(t *testing.T) {
	input := `"2024-03-15T12:30:45Z"`

	var u RFC3339EncodedTime
	err := json.Unmarshal([]byte(input), &u)
	if assert.ErrEqual(t, err, nil) {
		assert.Equal(t, u.Time, time.Date(2024, 3, 15, 12, 30, 45, 0, time.UTC))
	}
}

func TestMarshalRFC3339NanoTime(t *testing.T) {
	tst := time.Date(2024, 3, 15, 12, 30, 45, 123456789, time.UTC)
	u := RFC3339NanoEncodedTime{Time: tst}

	buf, err := json.Marshal(u)
	if assert.ErrEqual(t, err, nil) {
		assert.Equal(t, string(buf), `"2024-03-15T12:30:45.123456789Z"`)
	}
}

func TestUnmarshalRFC3339NanoTime(t *testing.T) {
	input := `"2024-03-15T12:30:45.123456789Z"`

	var u RFC3339NanoEncodedTime
	err := json.Unmarshal([]byte(input), &u)
	if assert.ErrEqual(t, err, nil) {
		assert.Equal(t, u.Time, time.Date(2024, 3, 15, 12, 30, 45, 123456789, time.UTC))
	}
}

func TestMarshalUint64OrNegativeOne(t *testing.T) {
	u := Uint64OrNegativeOne(Some(uint64(12345)))

	buf, err := json.Marshal(u)
	if assert.ErrEqual(t, err, nil) {
		assert.Equal(t, string(buf), `12345`)
	}

	u = Uint64OrNegativeOne(None[uint64]())

	buf, err = json.Marshal(u)
	if assert.ErrEqual(t, err, nil) {
		assert.Equal(t, string(buf), `-1`)
	}
}

func TestUnmarshalUint64OrNegativeOne(t *testing.T) {
	input := "12345"

	var u Uint64OrNegativeOne
	err := json.Unmarshal([]byte(input), &u)
	if assert.ErrEqual(t, err, nil) {
		assert.Equal(t, u.AsOption(), Some[uint64](12345))
	}

	input = "-1"
	err = json.Unmarshal([]byte(input), &u)
	if assert.ErrEqual(t, err, nil) {
		assert.Equal(t, u.AsOption(), None[uint64]())
	}

	input = "0"
	err = json.Unmarshal([]byte(input), &u)
	if assert.ErrEqual(t, err, nil) {
		assert.Equal(t, u.AsOption(), Some[uint64](0))
	}

	input = "-1234"
	err = json.Unmarshal([]byte(input), &u)
	assert.ErrEqual(t, err, `Uint64OrNegativeOne must be >= -1 (was "-1234")`)

	input = "null"
	err = json.Unmarshal([]byte(input), &u)
	assert.ErrEqual(t, err, `cannot unmarshal null to Uint64OrNegativeOne`)

	input = `""`
	err = json.Unmarshal([]byte(input), &u)
	assert.ErrEqual(t, err, `cannot unmarshal to Uint64OrNegativeOne: json: cannot unmarshal string "" into Go value of type json.Number: invalid syntax`)
}
