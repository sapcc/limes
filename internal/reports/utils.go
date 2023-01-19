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

package reports

import (
	"time"

	"github.com/sapcc/go-api-declarations/limes"
)

func mergeMinTime(lhs *limes.UnixEncodedTime, rhs *time.Time) *limes.UnixEncodedTime {
	if rhs == nil {
		return lhs
	}
	if lhs == nil || lhs.After(*rhs) {
		val := limes.UnixEncodedTime{Time: *rhs}
		return &val
	}
	return lhs
}

func mergeMaxTime(lhs *limes.UnixEncodedTime, rhs *time.Time) *limes.UnixEncodedTime {
	if rhs == nil {
		return lhs
	}
	if lhs == nil || lhs.Before(*rhs) {
		val := limes.UnixEncodedTime{Time: *rhs}
		return &val
	}
	return lhs
}
