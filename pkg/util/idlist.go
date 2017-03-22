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

//IDsToJSON embeds a list of string IDs in a data structure that serializes to
//JSON like [{"id":"first"},{"id":"second"}].
func IDsToJSON(ids []string) interface{} {
	type id struct {
		ID string `json:"id"`
	}
	result := make([]id, len(ids))
	for idx, str := range ids {
		result[idx].ID = str
	}
	return result
}
