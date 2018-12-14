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
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/core"
)

type capacitySapccIronicPlugin struct {
	cfg                 core.CapacitorConfiguration
	reportSubcapacities bool
}

var ironicUnmatchedNodesGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_unmatched_ironic_nodes",
		Help: "Number of available/active Ironic nodes without matching flavor.",
	},
	[]string{"os_cluster"},
)

func init() {
	core.RegisterCapacityPlugin(func(c core.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) core.CapacityPlugin {
		return &capacitySapccIronicPlugin{c, scrapeSubcapacities["compute"]["instances-baremetal"]}
	})
	prometheus.MustRegister(ironicUnmatchedNodesGauge)
}

//ID implements the core.CapacityPlugin interface.
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

//Scrape implements the core.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID string) (map[string]map[string]core.CapacityData, error) {
	//collect info about flavors with separate instance quota
	novaClient, err := openstack.NewComputeV2(provider, eo)
	if err != nil {
		return nil, err
	}
	flavors, err := collectIronicFlavorInfo(novaClient)
	if err != nil {
		return nil, err
	}

	//we are going to report capacity for all per-flavor instance quotas
	result := make(map[string]*core.CapacityData)
	for _, flavor := range flavors {
		result["instances_"+flavor.Name] = &core.CapacityData{Capacity: 0}
	}

	//count Ironic nodes
	ironicClient, err := newIronicClient(provider, eo)
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
				logg.Debug("Ironic node %q (%s) matches flavor %s", node.Name, node.ID, flavor.Name)
				data := result["instances_"+flavor.Name]
				data.Capacity++
				if p.reportSubcapacities {
					sub := map[string]interface{}{
						"id":   node.ID,
						"name": node.Name,
					}
					if node.Properties.MemoryMiB > 0 {
						sub["ram"] = core.ValueWithUnit{Unit: core.UnitMebibytes, Value: uint64(node.Properties.MemoryMiB)}
					}
					if node.Properties.DiskGiB > 0 {
						sub["disk"] = core.ValueWithUnit{Unit: core.UnitGibibytes, Value: uint64(node.Properties.DiskGiB)}
					}
					if node.Properties.Cores > 0 {
						sub["cores"] = uint64(node.Properties.Cores)
					}
					if node.Properties.SerialNumber != "" {
						sub["serial"] = node.Properties.SerialNumber
					}
					data.Subcapacities = append(data.Subcapacities, sub)
				}

				matched = true
				break
			}
		}
		if !matched {
			logg.Error("Ironic node %q (%s) does not match any baremetal flavor", node.Name, node.ID)
			unmatchedCounter++
		}
	}

	ironicUnmatchedNodesGauge.With(
		prometheus.Labels{"os_cluster": clusterID},
	).Set(float64(unmatchedCounter))

	//remove pointers from `result`
	result2 := make(map[string]core.CapacityData, len(result))
	for resourceName, data := range result {
		result2[resourceName] = *data
	}

	return map[string]map[string]core.CapacityData{"compute": result2}, nil
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
		logg.Debug("core mismatch: %d != %d", n.Properties.Cores, f.Cores)
		return false
	}
	if uint64(n.Properties.MemoryMiB) != f.MemoryMiB {
		logg.Debug("memory mismatch: %d != %d", n.Properties.MemoryMiB, f.MemoryMiB)
		return false
	}
	if uint64(n.Properties.DiskGiB) != f.DiskGiB {
		logg.Debug("disk mismatch: %d != %d", n.Properties.DiskGiB, f.DiskGiB)
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
			logg.Debug("capability %s mismatch: %q != %q", key, nodeValue, flavorValue)
			return false
		}
	}
	return true
}
