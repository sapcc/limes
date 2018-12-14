/*******************************************************************************
*
* Copyright 2018 SAP SE
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
