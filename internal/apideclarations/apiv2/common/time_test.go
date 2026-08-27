// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMarshalRFC3339Time(t *testing.T) {
	tst := time.Date(2024, 3, 15, 12, 30, 45, 500000000, time.UTC) // test that marshalling ignores the subsecond part
	u := RFC3339EncodedTime{Time: tst}

	buf, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err.Error())
	}

	actual := string(buf)
	expected := `"2024-03-15T12:30:45Z"`
	if actual != expected {
		t.Fatalf("expected %#v to serialize as %s, but got %s", u, expected, actual)
	}
}

func TestUnmarshalRFC3339Time(t *testing.T) {
	input := `"2024-03-15T12:30:45Z"`

	var u RFC3339EncodedTime
	err := json.Unmarshal([]byte(input), &u)
	if err != nil {
		t.Fatal(err.Error())
	}

	expected := time.Date(2024, 3, 15, 12, 30, 45, 0, time.UTC)
	actual := u.Time
	if actual != expected {
		t.Fatalf("expected %s to deserialize into %#v, but got %#v", input, expected, actual)
	}
}

func TestMarshalRFC3339NanoTime(t *testing.T) {
	tst := time.Date(2024, 3, 15, 12, 30, 45, 123456789, time.UTC)
	u := RFC3339NanoEncodedTime{Time: tst}

	buf, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err.Error())
	}

	actual := string(buf)
	expected := `"2024-03-15T12:30:45.123456789Z"`
	if actual != expected {
		t.Fatalf("expected %#v to serialize as %s, but got %s", u, expected, actual)
	}
}

func TestUnmarshalRFC3339NanoTime(t *testing.T) {
	input := `"2024-03-15T12:30:45.123456789Z"`

	var u RFC3339NanoEncodedTime
	err := json.Unmarshal([]byte(input), &u)
	if err != nil {
		t.Fatal(err.Error())
	}

	expected := time.Date(2024, 3, 15, 12, 30, 45, 123456789, time.UTC)
	actual := u.Time
	if actual != expected {
		t.Fatalf("expected %s to deserialize into %#v, but got %#v", input, expected, actual)
	}
}
