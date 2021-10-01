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
	"regexp"
	"strings"

	"github.com/sapcc/limes/pkg/core"
)

func compileManilaShareTypeSpecs(specs []core.ManilaShareTypeSpec) (err error) {
	for _, spec := range specs {
		for _, rule := range spec.MappingRules {
			namePatternFull := fmt.Sprintf(`^%s$`, rule.NamePattern)
			rule.NameRx, err = regexp.Compile(namePatternFull)
			if err != nil {
				return fmt.Errorf("while compiling regex %q: %w", namePatternFull, err)
			}
		}
	}
	return nil
}

//Given a virtual share type, returns the actual share type name that we have
//to use on the Manila API for this particular project, or "" if this share
//type shall be skipped for this project.
func resolveManilaShareType(spec core.ManilaShareTypeSpec, project core.KeystoneProject) string {
	fullName := fmt.Sprintf(`%s@%s`, project.Name, project.Domain.Name)
	for _, rule := range spec.MappingRules {
		if rule.NameRx.MatchString(fullName) {
			return rule.ShareType
		}
	}
	return spec.Name
}

//Given a virtual share type, list all share type names that can be used on the
//Manila API to set quota or read usage for it.
func getAllManilaShareTypes(spec core.ManilaShareTypeSpec) []string {
	var result []string
	for _, rule := range spec.MappingRules {
		result = append(result, rule.ShareType)

		//if there is a catch-all rule, no rules afterwards will have any effect
		namePattern := strings.TrimPrefix(rule.NamePattern, `^`)
		namePattern = strings.TrimSuffix(namePattern, `$`)
		if namePattern == `.*` || namePattern == `.+` {
			return result
		}
	}

	//if there is no pattern like `.*`, projects that do not match any of the
	//mapping rules will use the name of the virtual share type as the actual
	//Manila-level share type
	return append(result, spec.Name)
}
