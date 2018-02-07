/*******************************************************************************
*
* Copyright 2018 SAP SE
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
	"github.com/gophercloud/gophercloud/openstack"
	flavorsmodule "github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

type capacitySapccIronicPlugin struct {
	cfg limes.CapacitorConfiguration
}

var ironicUnmatchedNodesGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_unmatched_ironic_nodes",
		Help: "Number of available/active Ironic nodes without matching flavor.",
	},
	[]string{"os_cluster"},
)

func init() {
	limes.RegisterCapacityPlugin(func(c limes.CapacitorConfiguration) limes.CapacityPlugin {
		return &capacitySapccIronicPlugin{c}
	})
	prometheus.MustRegister(ironicUnmatchedNodesGauge)
}

func (p *capacitySapccIronicPlugin) NovaClient(provider *gophercloud.ProviderClient) (*gophercloud.ServiceClient, error) {
	return openstack.NewComputeV2(provider,
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
}

//ID implements the limes.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) ID() string {
	return "sapcc-ironic"
}

type ironicFlavorInfo struct {
	ID           string
	Name         string
	Cores        uint64
	MemoryMiB    uint64
	DiskGiB      uint64
	Capabilities map[string]string
}

//Scrape implements the limes.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) Scrape(provider *gophercloud.ProviderClient, clusterID string) (map[string]map[string]uint64, error) {
	//collect info about flavors with separate instance quota
	novaClient, err := p.NovaClient(provider)
	if err != nil {
		return nil, err
	}
	flavors, err := collectIronicFlavorInfo(novaClient)
	if err != nil {
		return nil, err
	}

	//we are going to report capacity for all per-flavor instance quotas
	result := make(map[string]uint64)
	for _, flavor := range flavors {
		result["instances_"+flavor.Name] = 0
	}

	//count Ironic nodes
	ironicClient, err := newIronicClient(provider)
	if err != nil {
		return nil, err
	}
	nodes, err := ironicClient.GetNodes()
	if err != nil {
		return nil, err
	}

	unmatchedCounter := 0
	for _, node := range nodes {
		//do not consider nodes that have not been made available for provisioning yet
		if !isAvailableProvisionState[node.StableProvisionState()] {
			continue
		}

		matched := false
		for _, flavor := range flavors {
			if node.Matches(flavor) {
				util.LogDebug("Ironic node %q (%s) matches flavor %s", node.Name, node.ID, flavor.Name)
				result["instances_"+flavor.Name]++
				matched = true
				break
			}
		}
		if !matched {
			util.LogDebug("Ironic node %q (%s) does not match any flavor", node.Name, node.ID)
			unmatchedCounter++
		}
	}

	if unmatchedCounter > 0 {
		util.LogError("%d Ironic nodes do not match any baremetal flavors", unmatchedCounter)
	}
	ironicUnmatchedNodesGauge.With(
		prometheus.Labels{"os_cluster": clusterID},
	).Set(float64(unmatchedCounter))

	return map[string]map[string]uint64{"compute": result}, nil
}

//NOTE: This method is shared with the Nova quota plugin.
func listPerFlavorInstanceResources(novaClient *gophercloud.ServiceClient) ([]string, error) {
	//look at quota class "default" to determine which quotas exist
	url := novaClient.ServiceURL("os-quota-class-sets", "default")
	var result gophercloud.Result
	_, err := novaClient.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	//At SAP Converged Cloud, we use per-flavor instance quotas for baremetal
	//(Ironic) flavors, to control precisely how many baremetal machines can be
	//used by each domain/project. Each such quota has the resource name
	//"instances_${FLAVOR_NAME}".
	var body struct {
		//NOTE: cannot use map[string]int64 here because this object contains the
		//field "id": "default" (curse you, untyped JSON)
		QuotaClassSet map[string]interface{} `json:"quota_class_set"`
	}
	err = result.ExtractInto(&body)
	if err != nil {
		return nil, err
	}

	var resources []string
	for key := range body.QuotaClassSet {
		if strings.HasPrefix(key, "instances_") {
			resources = append(resources, key)
		}
	}

	return resources, nil
}

func collectIronicFlavorInfo(novaClient *gophercloud.ServiceClient) ([]ironicFlavorInfo, error) {
	//which flavors have separate instance quota?
	resources, err := listPerFlavorInstanceResources(novaClient)
	if err != nil {
		return nil, err
	}
	isRelevantFlavorName := make(map[string]bool, len(resources))
	for _, resourceName := range resources {
		flavorName := strings.TrimPrefix(resourceName, "instances_")
		isRelevantFlavorName[flavorName] = true
	}

	//collect basic attributes for flavors
	var result []ironicFlavorInfo
	err = flavorsmodule.ListDetail(novaClient, nil).EachPage(func(page pagination.Page) (bool, error) {
		flavors, err := flavorsmodule.ExtractFlavors(page)
		if err != nil {
			return false, err
		}

		for _, flavor := range flavors {
			if isRelevantFlavorName[flavor.Name] {
				result = append(result, ironicFlavorInfo{
					ID:           flavor.ID,
					Name:         flavor.Name,
					Cores:        uint64(flavor.VCPUs),
					MemoryMiB:    uint64(flavor.RAM),
					DiskGiB:      uint64(flavor.Disk),
					Capabilities: make(map[string]string),
				})
			}
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}

	//retrieve extra specs - the ones in the "capabilities" namespace are
	//relevant for Ironic node selection
	for _, flavor := range result {
		extraSpecs, err := getFlavorExtras(novaClient, flavor.ID)
		if err != nil {
			return nil, err
		}
		for key, value := range extraSpecs {
			if strings.HasPrefix(key, "capabilities:") {
				capability := strings.TrimPrefix(key, "capabilities:")
				flavor.Capabilities[capability] = value
			}
		}
	}
	return result, nil
}

func (n ironicNode) Matches(f ironicFlavorInfo) bool {
	if uint64(n.Properties.Cores) != f.Cores {
		util.LogDebug("core mismatch: %d != %d", n.Properties.Cores, f.Cores)
		return false
	}
	if uint64(n.Properties.MemoryMiB) != f.MemoryMiB {
		util.LogDebug("memory mismatch: %d != %d", n.Properties.MemoryMiB, f.MemoryMiB)
		return false
	}
	if uint64(n.Properties.DiskGiB) != f.DiskGiB {
		util.LogDebug("disk mismatch: %d != %d", n.Properties.DiskGiB, f.DiskGiB)
		return false
	}

	nodeCaps := make(map[string]string)
	if n.Properties.CPUArchitecture != "" {
		nodeCaps["cpu_arch"] = n.Properties.CPUArchitecture
	}
	for _, field := range strings.Split(n.Properties.Capabilities, ",") {
		fields := strings.SplitN(field, ":", 2)
		if len(fields) == 2 {
			nodeCaps[fields[0]] = fields[1]
		}
	}

	for key, flavorValue := range f.Capabilities {
		nodeValue, exists := nodeCaps[key]
		if !exists || nodeValue != flavorValue {
			util.LogDebug("capability %s mismatch: %q != %q", key, nodeValue, flavorValue)
			return false
		}
	}
	return true
}
