/*******************************************************************************
*
* Copyright 2021 SAP SE
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

package plugins

import (
	"fmt"
	"maps"

	"github.com/sapcc/go-bits/regexpext"

	"github.com/sapcc/limes/internal/core"
)

// ManilaMappingRule appears in both ServiceConfiguration and CapacitorConfiguration.
type ManilaShareTypeSpec struct {
	Name               string `yaml:"name"`
	ReplicationEnabled bool   `yaml:"replication_enabled"` // only used by QuotaPlugin
	MappingRules       []*struct {
		NameRx    regexpext.BoundedRegexp `yaml:"name_pattern"`
		ShareType string                  `yaml:"share_type"`
	} `yaml:"mapping_rules"`
}

// Given a virtual share type, returns the actual share type name that we have
// to use on the Manila API for this particular project, or "" if this share
// type shall be skipped for this project.
func resolveManilaShareType(spec ManilaShareTypeSpec, project core.KeystoneProject) string {
	fullName := fmt.Sprintf(`%s@%s`, project.Name, project.Domain.Name)
	for _, rule := range spec.MappingRules {
		if rule.NameRx.MatchString(fullName) {
			return rule.ShareType
		}
	}
	return spec.Name
}

// Given a virtual share type, list all share type names that can be used on the
// Manila API to set quota or read usage for it.
func getAllManilaShareTypes(spec ManilaShareTypeSpec) []string {
	resultSet := make(map[string]bool)
	for _, rule := range spec.MappingRules {
		// rules that make the share type inaccessible should not be considered
		if rule.ShareType == "" {
			continue
		}
		resultSet[rule.ShareType] = true

		// if there is a catch-all rule, no rules afterwards will have any effect
		if rule.NameRx == `.*` || rule.NameRx == `.+` {
			return maps.Keys(resultSet)
		}
	}

	// if there is no pattern like `.*`, projects that do not match any of the
	// mapping rules will use the name of the virtual share type as the actual
	// Manila-level share type
	resultSet[spec.Name] = true
	return maps.Keys(resultSet)
}
