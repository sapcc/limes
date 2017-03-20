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

import "github.com/sapcc/limes/pkg/limes"

//Resource contains data about a backend service in the format returned by the API.
type Resource struct {
	Name  string     `json:"name"`
	Unit  limes.Unit `json:"unit,omitempty"`
	Quota int64      `json:"quota,keepempty"`
	Usage uint64     `json:"usage,keepempty"`
	//This is a pointer to a value to enable precise control over whether this field is rendered in output.
	BackendQuota *int64 `json:"backend_quota,omitempty"`
}

//Set writes the given quota and usage values into this Resource.
func (r *Resource) Set(quota int64, usage uint64, backendQuota int64) {
	r.Quota = quota
	r.Usage = usage
	if backendQuota == quota {
		r.BackendQuota = nil
	} else {
		r.BackendQuota = &backendQuota
	}
}
