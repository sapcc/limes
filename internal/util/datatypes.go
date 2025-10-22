// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/db"
)

////////////////////////////////////////////////////////////////////////////////

// MarshalableTimeDuration is a time.Duration that can be unmarshaled
// from a YAML string using time.ParseDuration.

type MarshalableTimeDuration time.Duration

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (d *MarshalableTimeDuration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	err := unmarshal(&s)
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

// AZResourceLocation is a tuple identifying an AZ resource within a project.
type AZResourceLocation struct {
	ServiceType      db.ServiceType
	ResourceName     liquid.ResourceName
	AvailabilityZone limes.AvailabilityZone
}
