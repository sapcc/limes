/*******************************************************************************
*
* Copyright 2017-2018 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

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
