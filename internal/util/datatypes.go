// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"encoding/json"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
)

////////////////////////////////////////////////////////////////////////////////

// MarshalableTimeDuration is a time.Duration that can be unmarshaled
// from a JSON string using time.ParseDuration.
type MarshalableTimeDuration time.Duration

// UnmarshalJSON implements the json.Unmarshaler interface.
func (d *MarshalableTimeDuration) UnmarshalJSON(data []byte) error {
	var s string
	err := json.Unmarshal(data, &s)
	if err != nil {
		return err
	}
	result, err := time.ParseDuration(s)
	*d = MarshalableTimeDuration(result)
	return err
}

// Into is a short-hand for casting into time.Duration.
func (d MarshalableTimeDuration) Into() time.Duration {
	return time.Duration(d)
}

////////////////////////////////////////////////////////////////////////////////

// IntoUnixEncodedTime converts a time to a UnixEncodedTime, used for storage in the DB.
func IntoUnixEncodedTime(t time.Time) limes.UnixEncodedTime {
	return limes.UnixEncodedTime{Time: t}
}

// FromUnixEncodedTime converts a time to a UnixEncodedTime, used for reading from the DB.
func FromUnixEncodedTime(t limes.UnixEncodedTime) time.Time {
	return t.Time
}

////////////////////////////////////////////////////////////////////////////////

// CommitmentStatusDeleted is used for soft-deleting commitments before they get hard-deleted after a grace period.
// It is defined here instead of in the liquid package, because in liquid we model deletions as status=None.
const CommitmentStatusDeleted liquid.CommitmentStatus = "deleted"
