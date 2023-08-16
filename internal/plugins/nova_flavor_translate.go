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
	"strings"

	"github.com/gophercloud/gophercloud"
)

// novaFlavorTranslationTable is used in situations where certain flavors can
// have more than one name in Nova, to translate between the names preferred by
// Nova and those preferred by Limes.
type novaFlavorTranslationTable struct {
	Entries []*novaFlavorTranslationEntry
}

// novaFlavorTranslationEntry is an entry for one particular flavor in type
// novaFlavorTranslationTable.
type novaFlavorTranslationEntry struct {
	//All possible names for this flavor, including the preferred names that have
	//their separate fields below.
	Aliases []string
	//The name that Limes prefers for this flavor. The resource name for a
	//separate instance quota is derived from this name, if needed. Also, this
	//name is what we show on instance subresources.
	LimesPreferredName string
	//The name that Nova prefers for this flavor, or an empty string if we don't
	//know yet which name Nova prefers. This is the name that gets used in API
	//calls to get or set separate instance quotas.
	NovaPreferredName string
}

func newNovaFlavorTranslationTable(flavorAliases map[string][]string) novaFlavorTranslationTable {
	var entries []*novaFlavorTranslationEntry
	for preferred, aliases := range flavorAliases {
		entries = append(entries, &novaFlavorTranslationEntry{
			Aliases:            append([]string{preferred}, aliases...),
			LimesPreferredName: preferred,
			NovaPreferredName:  "", //will be filled in first call to SeparateInstanceQuotaToLimesName
		})
	}
	return novaFlavorTranslationTable{entries}
}

func (t novaFlavorTranslationTable) findEntry(flavorName string) *novaFlavorTranslationEntry {
	for _, e := range t.Entries {
		for _, a := range e.Aliases {
			if a == flavorName {
				return e
			}
		}
	}
	return nil
}

// Used by ListFlavorsWithSeparateInstanceQuota() to record the fact that the
// given `flavorName` is used by Nova for a separate instance quota.
func (t novaFlavorTranslationTable) recordNovaPreferredName(flavorName string) {
	entry := t.findEntry(flavorName)
	if entry != nil {
		entry.NovaPreferredName = flavorName
	}
}

// Returns the Limes resource name for a flavor with a separate instance quota.
func (t novaFlavorTranslationTable) LimesResourceNameForFlavor(flavorName string) string {
	entry := t.findEntry(flavorName)
	if entry == nil {
		return "instances_" + flavorName
	}
	return "instances_" + entry.LimesPreferredName
}

// Returns the Nova quota name for the given Limes resource name, or "" if the
// given resource name does not refer to a separate instance quota.
func (t novaFlavorTranslationTable) NovaQuotaNameForLimesResourceName(resourceName string) string {
	//NOTE: Know the difference!
	//  novaQuotaName = "instances_${novaPreferredName}"
	//  resourceName = "instances_${limesPreferredName}"

	if !strings.HasPrefix(resourceName, "instances_") {
		return ""
	}

	limesFlavorName := strings.TrimPrefix(resourceName, "instances_")
	entry := t.findEntry(limesFlavorName)
	if entry == nil || entry.NovaPreferredName == "" {
		return "instances_" + limesFlavorName
	}

	return "instances_" + entry.NovaPreferredName
}

// Queries Nova for all separate instance quotas, and returns the flavor names
// that Nova prefers for each.
func (t novaFlavorTranslationTable) ListFlavorsWithSeparateInstanceQuota(computeV2 *gophercloud.ServiceClient) ([]string, error) {
	//look at the magical quota class "flavors" to determine which quotas exist
	url := computeV2.ServiceURL("os-quota-class-sets", "flavors")
	var result gophercloud.Result
	_, err := computeV2.Get(url, &result.Body, nil) //nolint:bodyclose // already closed by gophercloud
	if err != nil {
		return nil, err
	}

	//At SAP Converged Cloud, we use separate instance quotas for baremetal
	//(Ironic) flavors, to control precisely how many baremetal machines can be
	//used by each domain/project. Each such quota has the resource name
	//"instances_${FLAVOR_NAME}".
	var body struct {
		//NOTE: cannot use map[string]int64 here because this object contains the
		//field "id": "default" (curse you, untyped JSON)
		QuotaClassSet map[string]any `json:"quota_class_set"`
	}
	err = result.ExtractInto(&body)
	if err != nil {
		return nil, err
	}

	var flavorNames []string
	for key := range body.QuotaClassSet {
		if !strings.HasPrefix(key, "instances_") {
			continue
		}
		flavorName := strings.TrimPrefix(key, "instances_")
		flavorNames = append(flavorNames, flavorName)
		t.recordNovaPreferredName(flavorName)
	}

	return flavorNames, nil
}
