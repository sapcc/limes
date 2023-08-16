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

package test

import (
	"strings"

	policy "github.com/databus23/goslo.policy"
)

// PolicyEnforcer is a gopherpolicy.Enforcer implementation for API tests.
type PolicyEnforcer struct {
	AllowRaise            bool
	AllowRaiseLP          bool
	AllowLower            bool
	AllowLowerLP          bool
	AllowRaiseCentralized bool
	AllowLowerCentralized bool
	RejectServiceType     string
}

// Enforce implements the gopherpolicy.Enforcer interface.
func (e *PolicyEnforcer) Enforce(rule string, ctx policy.Context) bool {
	if e.RejectServiceType != "" && ctx.Request["service_type"] == e.RejectServiceType {
		return false
	}
	fields := strings.Split(rule, ":")
	switch fields[len(fields)-1] {
	case "raise":
		return e.AllowRaise
	case "raise_lowpriv":
		return e.AllowRaiseLP
	case "raise_centralized":
		return e.AllowRaiseCentralized
	case "lower":
		return e.AllowLower
	case "lower_lowpriv":
		return e.AllowLowerLP
	case "lower_centralized":
		return e.AllowLowerCentralized
	default:
		return true
	}
}
