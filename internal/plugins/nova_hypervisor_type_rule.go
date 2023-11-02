/*******************************************************************************
*
* Copyright 2017-2023 SAP SE
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
	"strings"

	"github.com/sapcc/go-bits/regexpext"
)

////////////////////////////////////////////////////////////////////////////////
// hypervisor type rules

type novaHypervisorTypeRule struct {
	Key            string                `yaml:"match"`
	Pattern        regexpext.PlainRegexp `yaml:"pattern"`
	HypervisorType string                `yaml:"type"`
}

func (r novaHypervisorTypeRule) Validate() error {
	//the format of rule.Key is built for future extensibility, e.g. if it
	//later becomes required to match against image capabilities
	switch {
	case r.Key == "flavor-name":
		return nil
	case strings.HasPrefix(r.Key, "extra-spec:"):
		return nil
	default:
		return fmt.Errorf(
			"key %q for hypervisor type rule must be \"flavor-name\" or start with \"extra-spec:\"",
			r.Key,
		)
	}
}

func (r novaHypervisorTypeRule) AppliesTo(flavor novaFlavorInfo) bool {
	switch {
	case r.Key == "flavor-name":
		return r.Pattern.MatchString(flavor.OriginalName)
	case strings.HasPrefix(r.Key, "extra-spec:"):
		specName := strings.TrimPrefix(r.Key, "extra-spec:")
		return r.Pattern.MatchString(flavor.ExtraSpecs[specName])
	default:
		return false
	}
}

type novaHypervisorTypeRules []novaHypervisorTypeRule

func (rules novaHypervisorTypeRules) Evaluate(flavor novaFlavorInfo) string {
	for _, r := range rules {
		if r.AppliesTo(flavor) {
			return r.HypervisorType
		}
	}
	return "unknown"
}
