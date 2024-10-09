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

package nova

import (
	"context"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/sapcc/go-api-declarations/liquid"
)

// FlavorTranslationTable is used in situations where certain flavors can
// have more than one name in Nova, to translate between the names preferred by
// Nova and those preferred by Limes.
type FlavorTranslationTable struct {
	Entries []*FlavorTranslationEntry
}

// FlavorTranslationEntry is an entry for one particular flavor in type
// FlavorTranslationTable.
type FlavorTranslationEntry struct {
	// All possible names for this flavor, including the preferred names that have
	// their separate fields below.
	Aliases []string
	// The name that Limes prefers for this flavor. The resource name for a
	// separate instance quota is derived from this name, if needed. Also, this
	// name is what we show on instance subresources.
	LimesPreferredName string
	// The name that Nova prefers for this flavor, or an empty string if we don't
	// know yet which name Nova prefers. This is the name that gets used in API
	// calls to get or set separate instance quotas.
	NovaPreferredName string
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (t *FlavorTranslationTable) UnmarshalYAML(unmarshal func(any) error) error {
	// in plugin configuration, an FTT is encoded as map[string][]string where
	// each key is the LimesPreferredName and the list of values contains the
	// other aliases of the same flavor
	var data map[string][]string
	err := unmarshal(&data)
	if err != nil {
		return err
	}

	t.Entries = make([]*FlavorTranslationEntry, 0, len(data))
	for preferred, aliases := range data {
		t.Entries = append(t.Entries, &FlavorTranslationEntry{
			Aliases:            append([]string{preferred}, aliases...),
			LimesPreferredName: preferred,
			NovaPreferredName:  "", // will be filled in first call to SeparateInstanceQuotaToLimesName
		})
	}
	return nil
}

// NewFlavorTranslationTable builds a FlavorTranslationEntry from the format
// found within plugin configuration.
func NewFlavorTranslationTable(flavorAliases map[string][]string) FlavorTranslationTable {
	var entries []*FlavorTranslationEntry
	for preferred, aliases := range flavorAliases {
		entries = append(entries, &FlavorTranslationEntry{
			Aliases:            append([]string{preferred}, aliases...),
			LimesPreferredName: preferred,
			NovaPreferredName:  "", // will be filled in first call to SeparateInstanceQuotaToLimesName
		})
	}
	return FlavorTranslationTable{entries}
}

func (t FlavorTranslationTable) findEntry(flavorName string) *FlavorTranslationEntry {
	for _, e := range t.Entries {
		if slices.Contains(e.Aliases, flavorName) {
			return e
		}
	}
	return nil
}

// Used by ListFlavorsWithSeparateInstanceQuota() to record the fact that the
// given `flavorName` is used by Nova for a separate instance quota.
func (t FlavorTranslationTable) recordNovaPreferredName(flavorName string) {
	entry := t.findEntry(flavorName)
	if entry != nil {
		entry.NovaPreferredName = flavorName
	}
}

// LimesResourceNameForFlavor returns the Limes resource name for a flavor with
// a separate instance quota.
func (t FlavorTranslationTable) LimesResourceNameForFlavor(flavorName string) liquid.ResourceName {
	entry := t.findEntry(flavorName)
	if entry == nil {
		return liquid.ResourceName("instances_" + flavorName)
	}
	return liquid.ResourceName("instances_" + entry.LimesPreferredName)
}

// NovaQuotaNameForLimesResourceName returns the Nova quota name for the given
// Limes resource name, or "" if the given resource name does not refer to a
// separate instance quota.
func (t FlavorTranslationTable) NovaQuotaNameForLimesResourceName(resourceName liquid.ResourceName) string {
	//NOTE: Know the difference!
	//  novaQuotaName = "instances_${novaPreferredName}"
	//  resourceName = "instances_${limesPreferredName}"

	limesFlavorName, ok := strings.CutPrefix(string(resourceName), "instances_")
	if !ok {
		return ""
	}

	entry := t.findEntry(limesFlavorName)
	if entry == nil || entry.NovaPreferredName == "" {
		return "instances_" + limesFlavorName
	}

	return "instances_" + entry.NovaPreferredName
}

// ListFlavorsWithSeparateInstanceQuota queries Nova for all separate instance
// quotas, and returns the flavor names that Nova prefers for each.
func (t FlavorTranslationTable) ListFlavorsWithSeparateInstanceQuota(ctx context.Context, computeV2 *gophercloud.ServiceClient) ([]string, error) {
	var flavorNames []string
	err := FlavorSelection{}.ForeachFlavor(ctx, computeV2, func(f flavors.Flavor) error {
		if f.ExtraSpecs["quota:separate"] == "true" {
			flavorNames = append(flavorNames, f.Name)
			t.recordNovaPreferredName(f.Name)
		}
		return nil
	})
	return flavorNames, err
}
