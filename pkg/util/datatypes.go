/*******************************************************************************
*
* Copyright 2017 SAP SE
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
	"fmt"
	"time"

	"encoding/json"
)

//Time is like time.Time, but can be scanned from a SQLite query where the
//result is an int64 (a UNIX timestamp).
type Time time.Time

//Scan implements the sql.Scanner interface.
func (t *Time) Scan(src interface{}) error {
	switch val := src.(type) {
	case int64:
		*t = Time(time.Unix(val, 0))
		return nil
	case time.Time:
		*t = Time(val)
		return nil
	default:
		return fmt.Errorf("cannot scan %t into util.Time", val)
	}
}

//Float64OrUnknown extracts a value of type float64 or unknown from a json
//result is an float64
type Float64OrUnknown float64

//UnmarshalJSON implements the json.Unmarshaler interface
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

//JSONString is a string containing JSON, that is not serialized further during
//json.Marshal().
type JSONString string

//MarshalJSON implements the json.Marshaler interface.
func (s JSONString) MarshalJSON() ([]byte, error) {
	return []byte(s), nil
}

//UnmarshalJSON implements the json.Unmarshaler interface
func (s *JSONString) UnmarshalJSON(b []byte) error {
	*s = JSONString(b)
	return nil
}
