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
	"encoding/json"
	"regexp"
	"strconv"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	flavorsmodule "github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/pkg/core"
)

type capacitySapccIronicPlugin struct {
	cfg                 core.CapacitorConfiguration
	ftt                 novaFlavorTranslationTable
	reportSubcapacities bool
}

type capacitySapccIronicSerializedMetrics struct {
	UnmatchedNodeCount uint64 `json:"unmatched_nodes"`
}

func init() {
	core.RegisterCapacityPlugin(func(c core.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) core.CapacityPlugin {
		ftt := newNovaFlavorTranslationTable(c.SAPCCIronic.FlavorAliases)
		return &capacitySapccIronicPlugin{c, ftt, scrapeSubcapacities["compute"]["instances-baremetal"]}
	})
}

// Init implements the core.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

// Type implements the core.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) Type() string {
	return "sapcc-ironic"
}

type ironicNode struct {
	ID                   string  `json:"uuid"`
	Name                 string  `json:"name"`
	ProvisionState       string  `json:"provision_state"`
	TargetProvisionState *string `json:"target_provision_state"`
	InstanceID           *string `json:"instance_uuid"`
	ResourceClass        *string `json:"resource_class"`
	Properties           struct {
		Cores           veryFlexibleUint64 `json:"cpus"`
		DiskGiB         veryFlexibleUint64 `json:"local_gb"`
		MemoryMiB       veryFlexibleUint64 `json:"memory_mb"`
		CPUArchitecture string             `json:"cpu_arch"`
		Capabilities    string             `json:"capabilities"` //e.g. "cpu_txt:true,cpu_aes:true"
		SerialNumber    string             `json:"serial"`
	} `json:"properties"`
}

func (n ironicNode) StableProvisionState() string {
	if n.TargetProvisionState != nil {
		return *n.TargetProvisionState
	}
	return n.ProvisionState
}

////////////////////////////////////////////////////////////////////////////////
// OpenStack is being inconsistent with itself again

// For fields that are sometimes missing, sometimes an integer, sometimes a string.
type veryFlexibleUint64 uint64

// UnmarshalJSON implements the json.Unmarshaler interface.
func (value *veryFlexibleUint64) UnmarshalJSON(buf []byte) error {
	if string(buf) == "null" {
		*value = 0
		return nil
	}

	if buf[0] == '"' {
		var str string
		err := json.Unmarshal(buf, &str)
		if err != nil {
			return err
		}
		val, err := strconv.ParseUint(str, 10, 64)
		*value = veryFlexibleUint64(val)
		return err
	}

	var val uint64
	err := json.Unmarshal(buf, &val)
	*value = veryFlexibleUint64(val)
	return err
}

// Reference:
//
//	Hosts are expected to be in one of the following format:
//	  - "nova-compute-xxxx"
//	  - "nova-compute-ironic-xxxx"
//	where "xxxx" is unique among all hosts.
var computeHostStubRx = regexp.MustCompile(`^nova-compute-(?:ironic-)?([a-zA-Z0-9]+)$`)

// Node names are expected to be in the form "nodeXXX-bmYYY" or "nodeXXX-bbYYY"
// or "nodeXXX-apYYY" or "nodeXXX-mdYYY", where the second half is the host stub
// (the match group from above).
var nodeNameRx = regexp.MustCompile(`^node(?:swift)?\d+-((?:b[bm]|ap|md|st|swf)\d+)$`)

// As a special case, nodes in the control plane do not belong to any
// user-accessible Nova aggregates, so we cannot establish an AZ association.
// However, we don't really need the AZ association anyway: AZ capacities are
// presented to the customer as a sort of manual scheduling hint, but CP nodes
// are earmarked for internal use and thus are not relevant there.
var cpNodeNameRx = regexp.MustCompile(`^node(?:swift)?\d+-(cp\d+)$`)

// Scrape implements the core.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (map[string]map[string]core.CapacityData, string, error) {
	//collect info about flavors with separate instance quota
	novaClient, err := openstack.NewComputeV2(provider, eo)
	if err != nil {
		return nil, "", err
	}
	flavors, err := p.getIronicFlavorNames(novaClient)
	if err != nil {
		return nil, "", err
	}

	//we are going to report capacity for all per-flavor instance quotas
	result := make(map[string]*core.CapacityData)
	for _, flavor := range flavors {
		result[p.ftt.LimesResourceNameForFlavor(flavor)] = &core.CapacityData{
			Capacity:      0,
			CapacityPerAZ: map[string]*core.CapacityDataForAZ{},
		}
	}

	//count Ironic nodes
	ironicClient, err := openstack.NewBareMetalV1(provider, eo)
	if err != nil {
		return nil, "", err
	}
	ironicClient.Microversion = "1.22"
	allPages, err := ironicNodesListDetail(ironicClient).AllPages()
	if err != nil {
		return nil, "", err
	}
	var allNodes []ironicNode
	err = ironicExtractNodesInto(allPages, &allNodes)
	if err != nil {
		return nil, "", err
	}

	//Ironic bPods are expected to be listed as compute hosts assigned to
	//host aggregates in the format: "nova-compute-ironic-xxxx".
	azs, _, err := getAggregates(novaClient)
	if err != nil {
		return nil, "", err
	}
	azForHostStub := make(map[string]string)
	for azName, az := range azs {
		for host := range az.ContainsComputeHost {
			if host == "nova-compute-ironic" {
				azForHostStub["bpod001"] = azName //hardcoded for the few nodes using legacy naming convention
			} else {
				match := computeHostStubRx.FindStringSubmatch(host)
				if match == nil {
					logg.Error(`compute host %q does not match the "nova-compute-(ironic-)xxxx" naming convention`, host)
				} else {
					azForHostStub[match[1]] = azName
				}
			}
		}
	}

	unmatchedCounter := uint64(0)
	for _, node := range allNodes {
		//do not consider nodes that have not been made available for provisioning yet
		if !isAvailableProvisionState[node.StableProvisionState()] {
			continue
		}

		matched := false
		for _, flavor := range flavors {
			if node.Matches(flavor) {
				logg.Debug("Ironic node %q (%s) matches flavor %s", node.Name, node.ID, flavor)

				data := result[p.ftt.LimesResourceNameForFlavor(flavor)]
				data.Capacity++

				var nodeAZ string
				if match := cpNodeNameRx.FindStringSubmatch(node.Name); match != nil {
					//special case as explained above (near definition of `cpNodeNameRx`)
					nodeAZ = match[1]
				} else if match := nodeNameRx.FindStringSubmatch(node.Name); match != nil {
					nodeAZ = azForHostStub[match[1]]
					if nodeAZ == "" {
						logg.Info("Ironic node %q (%s) does not match any compute host from host aggregates", node.Name, node.ID)
						nodeAZ = "unknown"
					}
				} else {
					logg.Error(`Ironic node %q (%s) does not match the "nodeXXX-{bm,bb,ap,md,st,swf}YYY" naming convention`, node.Name, node.ID)
				}

				if _, ok := data.CapacityPerAZ[nodeAZ]; !ok {
					data.CapacityPerAZ[nodeAZ] = &core.CapacityDataForAZ{}
				}
				data.CapacityPerAZ[nodeAZ].Capacity++
				if node.StableProvisionState() == "active" {
					data.CapacityPerAZ[nodeAZ].Usage++
				}

				if p.reportSubcapacities {
					sub := map[string]interface{}{
						"id":              node.ID,
						"name":            node.Name,
						"provision_state": node.ProvisionState,
					}
					if node.TargetProvisionState != nil && *node.TargetProvisionState != "" {
						sub["target_provision_state"] = *node.TargetProvisionState
					}
					if nodeAZ != "" {
						sub["availability_zone"] = nodeAZ
					}
					if node.Properties.MemoryMiB > 0 {
						sub["ram"] = limes.ValueWithUnit{Unit: limes.UnitMebibytes, Value: uint64(node.Properties.MemoryMiB)}
					}
					if node.Properties.DiskGiB > 0 {
						sub["disk"] = limes.ValueWithUnit{Unit: limes.UnitGibibytes, Value: uint64(node.Properties.DiskGiB)}
					}
					if node.Properties.Cores > 0 {
						sub["cores"] = uint64(node.Properties.Cores)
					}
					if node.Properties.SerialNumber != "" {
						sub["serial"] = node.Properties.SerialNumber
					}
					if node.InstanceID != nil && *node.InstanceID != "" {
						sub["instance_id"] = *node.InstanceID
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

	//remove pointers from `result`
	result2 := make(map[string]core.CapacityData, len(result))
	for resourceName, data := range result {
		result2[resourceName] = *data
	}

	serializedMetrics, err := json.Marshal(capacitySapccIronicSerializedMetrics{
		UnmatchedNodeCount: unmatchedCounter,
	})
	if err != nil {
		return nil, "", err
	}

	return map[string]map[string]core.CapacityData{"compute": result2}, string(serializedMetrics), nil
}

var ironicUnmatchedNodesGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_unmatched_ironic_nodes",
		Help: "Number of available/active Ironic nodes without matching flavor.",
	},
	[]string{"os_cluster"},
)

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	ironicUnmatchedNodesGauge.Describe(ch)
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID, serializedMetrics string) error {
	if serializedMetrics == "" {
		return nil
	}
	var metrics capacitySapccIronicSerializedMetrics
	err := json.Unmarshal([]byte(serializedMetrics), &metrics)
	if err != nil {
		return err
	}

	descCh := make(chan *prometheus.Desc, 1)
	ironicUnmatchedNodesGauge.Describe(descCh)
	ironicUnmatchedNodesDesc := <-descCh

	ch <- prometheus.MustNewConstMetric(
		ironicUnmatchedNodesDesc,
		prometheus.GaugeValue, float64(metrics.UnmatchedNodeCount),
		clusterID,
	)
	return nil
}

func (p *capacitySapccIronicPlugin) getIronicFlavorNames(novaClient *gophercloud.ServiceClient) ([]string, error) {
	//which flavors have separate instance quota?
	flavorNames, err := p.ftt.ListFlavorsWithSeparateInstanceQuota(novaClient)
	if err != nil {
		return nil, err
	}
	isRelevantFlavorName := make(map[string]bool, len(flavorNames))
	for _, flavorName := range flavorNames {
		isRelevantFlavorName[flavorName] = true
	}

	//check flavor relevancy
	var result []string
	err = flavorsmodule.ListDetail(novaClient, nil).EachPage(func(page pagination.Page) (bool, error) {
		flavors, err := flavorsmodule.ExtractFlavors(page)
		if err != nil {
			return false, err
		}

		for _, flavor := range flavors {
			if isRelevantFlavorName[flavor.Name] {
				result = append(result, flavor.Name)
			}
		}
		return true, nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (n ironicNode) Matches(flavorName string) bool {
	if n.ResourceClass != nil {
		return *n.ResourceClass == flavorName
	}
	return false
}
