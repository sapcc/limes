/*******************************************************************************
*
* Copyright 2022 SAP SE
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

package limes

import (
	"encoding/json"
	"time"
)

// UnixEncodedTime is a time.Time that marshals into JSON as a UNIX timestamp.
//
// This is a single-member struct instead of a newtype because the former
// enables directly calling time.Time methods on this type, e.g. t.String()
// instead of time.Time(t).String().
type UnixEncodedTime struct {
	time.Time
}

// MarshalJSON implements the json.Marshaler interface.
func (t UnixEncodedTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.Unix())
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (t *UnixEncodedTime) UnmarshalJSON(buf []byte) error {
	var tst int64
	err := json.Unmarshal(buf, &tst)
	if err == nil {
		t.Time = time.Unix(tst, 0).UTC()
	}
	return err
}
