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
	"encoding/json"

	"gopkg.in/yaml.v2"
)

// Float64OrUnknown extracts a value of type float64 or unknown from a json
// result is an float64
type Float64OrUnknown float64

// UnmarshalJSON implements the json.Unmarshaler interface
func (f *Float64OrUnknown) UnmarshalJSON(buffer []byte) error {
	if buffer[0] == '"' {
		*f = 0
		return nil
	}
	var x float64
	err := json.Unmarshal(buffer, &x)
	*f = Float64OrUnknown(x)
	return err
}

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
