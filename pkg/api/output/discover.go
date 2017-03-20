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

package output

//NewScope contains data about a newly discovered domain or project in the
//format returned by the API.
type NewScope struct {
	ID string `json:"id"`
}

//NewScopesFromIDList prepares a list of IDs of newly discovered projects and
//domains for JSON serialization.
func NewScopesFromIDList(ids []string) []NewScope {
	result := make([]NewScope, len(ids))
	for idx, id := range ids {
		result[idx].ID = id
	}
	return result
}
