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

package limesrates

import "github.com/sapcc/go-api-declarations/limes"

// RateInfo contains the metadata for a rate (i.e. some type of event that can
// be rate-limited and for which there may a way to retrieve a count of past
// events from a backend service).
type RateInfo struct {
	Name string     `json:"name"`
	Unit limes.Unit `json:"unit,omitempty"`
}
