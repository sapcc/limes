/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package nova

import "github.com/sapcc/go-bits/regexpext"

// FlavorNameSelection describes a selection of flavors exclusively by name. If
// the entire flavor is available, type FlavorSelection is used to match a
// selection thereof.
//
// This selection method is used for flavor names returned by
// ListFlavorsWithSeparateInstanceQuota.
type FlavorNameSelection []FlavorNameSelectionRule

// FlavorNameSelectionRule appears in type FlavorNameSelection.
type FlavorNameSelectionRule struct {
	NamePattern regexpext.PlainRegexp `yaml:"name_pattern"`
}

// MatchFlavorName returns whether this flavor matches the selection.
func (s FlavorNameSelection) MatchFlavorName(flavorName string) bool {
	for _, rule := range s {
		//NOTE: If `name_pattern` is empty, any `flavorName` will match.
		if rule.NamePattern.MatchString(flavorName) {
			return true
		}
	}
	return false
}
