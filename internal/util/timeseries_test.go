// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"cmp"
	"testing"
	"time"

	"github.com/sapcc/go-bits/assert"
)

func TestTimeSeries(t *testing.T) {
	s := EmptyTimeSeries[float64]()
	expectJSON(t, s, `{}`)

	// pruning an empty time series just does nothing
	s.PruneOldValues(time.Unix(0, 0), 25*time.Second)
	expectJSON(t, s, `{}`)

	// adding a first measurement will never fail
	mustT(t, s.AddMeasurement(time.Unix(10, 0), 42.0))
	expectJSON(t, s, `{"t":[10],"v":[42]}`)

	// adding a second measurement works if the time is larger
	mustT(t, s.AddMeasurement(time.Unix(15, 0), 41.0))
	expectJSON(t, s, `{"t":[10,15],"v":[42,41]}`)

	// adding the same measurement again is a no-op
	mustT(t, s.AddMeasurement(time.Unix(15, 0), 41.0))
	expectJSON(t, s, `{"t":[10,15],"v":[42,41]}`)

	// trying to add an earlier measurement is an error
	err := s.AddMeasurement(time.Unix(13, 0), 41.0)
	mustFailT(t, err, "cannot add value for timestamp 13: already recorded later timestamp 15")

	// trying to add contradictory measurements is an error
	err = s.AddMeasurement(time.Unix(15, 0), 42.0)
	mustFailT(t, err, "ambiguous value for timestamp 15: tried to record 42 now, but already recorded 41")

	// add some more measurements to prepare for the pruning test
	mustT(t, s.AddMeasurement(time.Unix(20, 0), 40.0))
	mustT(t, s.AddMeasurement(time.Unix(25, 0), 45.0))
	mustT(t, s.AddMeasurement(time.Unix(30, 0), 46.0))
	mustT(t, s.AddMeasurement(time.Unix(35, 0), 47.0))
	mustT(t, s.AddMeasurement(time.Unix(40, 0), 48.0))
	expectJSON(t, s, `{"t":[10,15,20,25,30,35,40],"v":[42,41,40,45,46,47,48]}`)

	// test pruning with cutoff aligned to a previous measurement (removes all before the cutoff)
	s.PruneOldValues(time.Unix(40, 0), 25*time.Second)
	expectJSON(t, s, `{"t":[15,20,25,30,35,40],"v":[41,40,45,46,47,48]}`)

	// test pruning with cutoff not aligned (retains one value before the cutoff
	// to cover the span between cutoff and next measurement)
	s.PruneOldValues(time.Unix(40, 0), 13*time.Second)
	expectJSON(t, s, `{"t":[25,30,35,40],"v":[45,46,47,48]}`)

	// test pruning that is a no-op because nothing falls outside the boundary
	s.PruneOldValues(time.Unix(40, 0), 100*time.Second)
	expectJSON(t, s, `{"t":[25,30,35,40],"v":[45,46,47,48]}`)
}

func TestTimeSeriesUnmarshalErrors(t *testing.T) {
	testcases := []struct {
		Representation string
		ExpectedError  string
	}{
		{
			`{"t":[1,2],"v":[1,2,3]}`,
			"cannot unmarshal TimeSeries with inconsistent length: len(t) = 2 != 3 = len(v)",
		},
		{
			`{"t":[2,3,1],"v":[1,2,3]}`,
			"cannot unmarshal TimeSeries with unsorted timestamps",
		},
		{
			`{"t":[1,2,2],"v":[1,2,3]}`,
			"cannot unmarshal TimeSeries with duplicate timestamps: 2 appears more than once",
		},
	}

	for _, tc := range testcases {
		t.Logf("testing unmarshal of `%s`", tc.Representation)
		_, err := ParseTimeSeries[float64](tc.Representation)
		mustFailT(t, err, tc.ExpectedError)
	}
}

func TestTimeSeriesPruningWithOnlyAncientValues(t *testing.T) {
	// This time series is from prod.
	s, err := ParseTimeSeries[uint64](`{"t":[1714649006,1715247668],"v":[5,6]}`)
	mustT(t, err)
	// A few days after the timestamps in the time series...
	now := time.Unix(1715600837, 0)
	// ...the following measurement was added...
	mustT(t, s.AddMeasurement(now, 6))
	// ...with the following retention.
	s.PruneOldValues(now, 48*time.Hour)

	// The bug was that the value 5 was not pruned from the time series as expected.
	expectJSON(t, s, `{"t":[1715247668],"v":[6]}`)
}

func mustT(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func mustFailT(t *testing.T, err error, expected string) {
	t.Helper()
	if err == nil {
		t.Errorf("expected to fail with %q, but got no error", expected)
	} else if err.Error() != expected {
		t.Errorf("expected to fail with %q, but failed with %q", expected, err.Error())
	}
}

func expectJSON[T cmp.Ordered](t *testing.T, value TimeSeries[T], repr string) {
	t.Helper()

	// test that the value marshals to the given JSON representation
	buf, err := value.Serialize()
	if err != nil {
		t.Error("while marshaling: " + err.Error())
	} else {
		assert.DeepEqual(t, "JSON representation", buf, repr)
	}

	// test that the JSON representation unmarshals into an identical value
	unmarshaled, err := ParseTimeSeries[T](repr)
	if err != nil {
		t.Error("while unmarshaling: " + err.Error())
	} else {
		assert.DeepEqual(t, "unmarshaled value", unmarshaled, value)
	}
}
