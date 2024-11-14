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
	// flags by scope
	AllowCluster bool
	AllowDomain  bool
	AllowProject bool
	// flags by action
	AllowView         bool
	AllowEdit         bool
	AllowEditMaxQuota bool
	AllowUncommit     bool
	// match by request attribute
	RejectServiceType string
}

// Enforce implements the gopherpolicy.Enforcer interface.
func (e *PolicyEnforcer) Enforce(rule string, ctx policy.Context) bool {
	if e.RejectServiceType != "" && ctx.Request["service_type"] == e.RejectServiceType {
		return false
	}
	fields := strings.Split(rule, ":")
	if len(fields) != 2 {
		return false
	}
	return e.allowScope(fields[0]) && e.allowAction(fields[1])
}

func (e *PolicyEnforcer) allowScope(scope string) bool {
	switch scope {
	case "project":
		return e.AllowProject
	case "domain":
		return e.AllowDomain
	case "cluster":
		return e.AllowCluster
	default:
		return false
	}
}

func (e *PolicyEnforcer) allowAction(action string) bool {
	switch action {
	case "list", "show":
		return e.AllowView
	case "edit":
		return e.AllowEdit
	case "edit_as_outside_admin":
		return e.AllowEditMaxQuota
	case "uncommit":
		return e.AllowUncommit
	default:
		return true
	}
}
