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

package nova

import (
	"fmt"
	"strings"

	"github.com/sapcc/go-bits/regexpext"
)

// HypervisorTypeRules is a set of rules that allows to compute the
// HypervisorType attribute of a Nova instance subresource from its FlavorInfo.
type HypervisorTypeRules []HypervisorTypeRule

// Validate returns an error if this rule is not valid.
func (rules HypervisorTypeRules) Validate() error {
	for _, r := range rules {
		err := r.validate()
		if err != nil {
			return err
		}
	}
	return nil
}

// Evaluate returns the HypervisorType string for the given instance flavor.
func (rules HypervisorTypeRules) Evaluate(flavor FlavorInfo) string {
	for _, r := range rules {
		if r.appliesTo(flavor) {
			return r.HypervisorType
		}
	}
	return "unknown"
}

// HypervisorTypeRule is a single entry in type HypervisorTypeRules.
type HypervisorTypeRule struct {
	Key            string                `yaml:"match"`
	Pattern        regexpext.PlainRegexp `yaml:"pattern"`
	HypervisorType string                `yaml:"type"`
}

func (r HypervisorTypeRule) validate() error {
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

func (r HypervisorTypeRule) appliesTo(flavor FlavorInfo) bool {
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
