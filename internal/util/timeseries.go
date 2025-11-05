// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

// TimeSeries is a minimalistic implementation of a time series
// (a representation of a metric that assumes different values over time),
// tailored towards the exact needs of the project_az_resources.historical_usage attribute.
type TimeSeries[T cmp.Ordered] struct {
	// NOTE: This does not use the obvious encoding,
	// and instead reformats the data into a columnar pattern:
	//
	//   obvious: [{"t":123,"v":1.5},{"t":456,"v":2.5},{"t":789,"v":3.5}]
	//   columnar: {"t":[123,456,789],"v":[1.5,2.5,3.5]}
	//
	// Like in this example, this removes a substantial amount of repetition and
	// squeezes a good amount of bytes out of the representation.
	// Postgres always stores strings uncompressed as long they are shorter than
	// 2 KiB, which these time series might very well be, but the large number of
	// records makes this still worthwhile in my opinion.
	timestamps []int64
	values     []T

	// This type maintains three invariants:
	// 1. Both lists are always of equal length.
	// 2. Measurements are always ordered chronologically.
	// 3. Each measurement has a unique timestamp.
}

// EmptyTimeSeries constructs a new empty TimeSeries.
func EmptyTimeSeries[T cmp.Ordered]() TimeSeries[T] {
	return TimeSeries[T]{timestamps: nil, values: nil}
}

// MinOr returns the minimum of this time series, or the provided fallback value if it is empty.
func (s TimeSeries[T]) MinOr(fallback T) T {
	if len(s.values) == 0 {
		return fallback
	}
	return slices.Min(s.values)
}

// MaxOr returns the maximum of this time series, or the provided fallback value if it is empty.
func (s TimeSeries[T]) MaxOr(fallback T) T {
	if len(s.values) == 0 {
		return fallback
	}
	return slices.Max(s.values)
}

// AddMeasurement adds a new point to this time series, unless the previous
// point in time has the same value. An error is returned if the time series
// already contains measurements from a time after `now`.
func (s *TimeSeries[T]) AddMeasurement(now time.Time, value T) error {
	timestamp := now.Unix()

	if len(s.timestamps) > 0 {
		lastIndex := len(s.timestamps) - 1
		lastTimestamp := s.timestamps[lastIndex]
		lastValue := s.values[lastIndex]

		// check the ordering invariant
		if lastTimestamp > timestamp {
			return fmt.Errorf(
				"cannot add value for timestamp %d: already recorded later timestamp %d",
				timestamp, lastTimestamp)
		}

		// do not record redundant measurements
		if lastValue == value {
			return nil
		}

		// check the uniqueness invariant
		if lastTimestamp == timestamp {
			return fmt.Errorf(
				"ambiguous value for timestamp %d: tried to record %v now, but already recorded %v",
				lastTimestamp, value, lastValue)
		}
	}

	// record the new measurement
	s.timestamps = append(s.timestamps, timestamp)
	s.values = append(s.values, value)
	return nil
}

// PruneOldValues removes all measurements that fall before `now.add(-retentionPeriod)`.
func (s *TimeSeries[T]) PruneOldValues(now time.Time, retentionPeriod time.Duration) {
	cutoff := s.findCutoffIndex(now, retentionPeriod)
	if cutoff > 0 {
		s.timestamps = slices.Clone(s.timestamps[cutoff:])
		s.values = slices.Clone(s.values[cutoff:])
	}
}

// Helper function for PruneOldValues: Returns the first index that must be retained.
// If `idx` is returned, the range [0:idx] will be pruned.
func (s *TimeSeries[T]) findCutoffIndex(now time.Time, retentionPeriod time.Duration) int {
	cutoffTimestamp := now.Add(-retentionPeriod).Unix()

	// Note that, unless a measurement falls exactly at the cutoff, the newest
	// measurement from before that point will be retained.
	// Each measurement is thought to apply not only to its own timestamp, but to
	// the entire span of time until the next measurement.
	// Values are retained if this span overlaps with the retention period.
	for idx, timestamp := range s.timestamps {
		if timestamp == cutoffTimestamp {
			return idx
		} else if timestamp > cutoffTimestamp {
			return max(idx-1, 0)
		}
	}

	// If all measurements fall before `cutoffTimestamp`, the loop above will not return.
	// In this case, only the most recent of those ancient measurements is
	// relevant to describing the behavior within our retention period.
	return max(len(s.timestamps)-1, 0)
}

// The JSON representation of type TimeSeries. This is a separate type because
// json.Marshal/Unmarshal cannot access the private fields of the original type.
type timeSeriesRepr[T cmp.Ordered] struct {
	Timestamps []int64 `json:"t"`
	Values     []T     `json:"v"`
}

// ParseTimeSeries parses the JSON representation of a time series,
// or returns an empty time series if the input is the empty string.
//
// NOTE: We do not implement UnmarshalJSON since this function offers a more
// convenient interface for our actual usecases.
func ParseTimeSeries[T cmp.Ordered](input string) (TimeSeries[T], error) {
	if input == "" {
		return EmptyTimeSeries[T](), nil
	}

	var repr timeSeriesRepr[T]
	err := json.Unmarshal([]byte(input), &repr)
	if err != nil {
		return EmptyTimeSeries[T](), err
	}
	ts := TimeSeries[T]{
		timestamps: repr.Timestamps,
		values:     repr.Values,
	}

	if len(ts.timestamps) != len(ts.values) {
		return EmptyTimeSeries[T](), fmt.Errorf(
			"cannot unmarshal TimeSeries with inconsistent length: len(t) = %d != %d = len(v)",
			len(ts.timestamps), len(ts.values))
	}
	if !slices.IsSorted(ts.timestamps) {
		return EmptyTimeSeries[T](), errors.New("cannot unmarshal TimeSeries with unsorted timestamps")
	}
	for idx := 1; idx < len(ts.timestamps); idx++ {
		if ts.timestamps[idx-1] == ts.timestamps[idx] {
			return EmptyTimeSeries[T](), fmt.Errorf(
				"cannot unmarshal TimeSeries with duplicate timestamps: %d appears more than once",
				ts.timestamps[idx])
		}
	}

	return ts, nil
}

// Serialize returns the JSON representation of this timeseries.
//
// NOTE: We do not implement MarshalJSON since this function offers a more
// convenient interface for our actual usecases.
func (s TimeSeries[T]) Serialize() (string, error) {
	if len(s.timestamps) == 0 {
		return "{}", nil
	}

	buf, err := json.Marshal(timeSeriesRepr[T]{
		Timestamps: s.timestamps,
		Values:     s.values,
	})
	return string(buf), err
}
