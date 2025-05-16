// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"time"

	"gopkg.in/yaml.v2"
)

// YamlRawMessage is like json.RawMessage: During yaml.Unmarshal(), it will
// just collect the provided YAML representation instead of parsing it into a
// specific datatype. It can be used to defer parsing when the concrete target
// type is not yet known when the YAML input is initially unmarshalled.
type YamlRawMessage []byte

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (m *YamlRawMessage) UnmarshalYAML(unmarshal func(any) error) error {
	var data any
	err := unmarshal(&data)
	if err != nil {
		return err
	}
	*m, err = yaml.Marshal(data)
	return err
}

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
