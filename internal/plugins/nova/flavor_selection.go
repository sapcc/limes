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

import (
	"fmt"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
)

// FlavorSelection describes a set of public flavors.
//
// This is used for matching flavors that we enumerate via the flavor API
// itself (so we know things like extra specs). For matching flavors just by
// name, type FlavorNameSelection is used.
type FlavorSelection struct {
	// Only match flavors that have all of these extra specs.
	RequiredExtraSpecs map[string]string `yaml:"required_extra_specs"`
	// Exclude flavors that have any of these extra specs.
	ExcludedExtraSpecs map[string]string `yaml:"excluded_extra_specs"`
}

func (s FlavorSelection) matchesExtraSpecs(specs map[string]string) bool {
	for key, value := range s.RequiredExtraSpecs {
		if value != specs[key] {
			return false
		}
	}
	for key, value := range s.ExcludedExtraSpecs {
		if value == specs[key] {
			return false
		}
	}
	return true
}

// ForeachFlavor lists all public flavors matching this FlavorSelection, and
// calls the given callback once for each of them.
func (s FlavorSelection) ForeachFlavor(novaV2 *gophercloud.ServiceClient, action func(FullFlavor) error) error {
	opts := flavors.ListOpts{AccessType: flavors.AllAccess}
	page, err := flavors.ListDetail(novaV2, &opts).AllPages()
	if err != nil {
		return fmt.Errorf("while listing public flavors: %w", err)
	}
	allFlavors, err := flavors.ExtractFlavors(page)
	if err != nil {
		return fmt.Errorf("while listing public flavors: %w", err)
	}

	for _, flavor := range allFlavors {
		specs, err := flavors.ListExtraSpecs(novaV2, flavor.ID).Extract()
		if err != nil {
			return fmt.Errorf("while listing extra specs of public flavor %q: %w", flavor.Name, err)
		}
		if s.matchesExtraSpecs(specs) {
			err = action(FullFlavor{flavor, specs})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// FullFlavor is a flavor plus its set of extra specs.
type FullFlavor struct {
	Flavor     flavors.Flavor
	ExtraSpecs map[string]string
}

// MatchesHypervisor returns true if instances of this flavor can be placed on the given hypervisor.
func (f FullFlavor) MatchesHypervisor(mh MatchingHypervisor) bool {
	// extra specs like `"trait:FOO": "required"` or `"trait:BAR": "forbidden"`
	// are used by the Nova scheduler to ignore hypervisors that do not (or do)
	// have the respective traits
	for key, value := range f.ExtraSpecs {
		trait, matches := strings.CutPrefix(key, "trait:")
		if !matches {
			continue
		}
		hasTrait := slices.Contains(mh.Traits, trait)
		if value == "required" && !hasTrait {
			return false
		}
		if value == "forbidden" && hasTrait {
			return false
		}
	}
	return true
}
