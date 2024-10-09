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
	"context"
	"encoding/json"
	"regexp"
	"strconv"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/aggregates"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/plugins/nova"
)

type capacitySapccIronicPlugin struct {
	// configuration
	FlavorNameSelection nova.FlavorNameSelection    `yaml:"flavor_name_selection"`
	FlavorAliases       nova.FlavorTranslationTable `yaml:"flavor_aliases"`
	WithSubcapacities   bool                        `yaml:"with_subcapacities"`
	// connections
	NovaV2   *gophercloud.ServiceClient `yaml:"-"`
	IronicV1 *gophercloud.ServiceClient `yaml:"-"`
}

type capacitySapccIronicSerializedMetrics struct {
	//NOTE: We only report the node counts to Prometheus. The node names are only
	// serialized into the DB, so that operators can pull reports or double-check
	// manually when necessary.
	UnmatchedNodeNames []string `json:"unmatched_node_names"`
	RetiredNodeNames   []string `json:"retired_node_names"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacitySapccIronicPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	p.NovaV2, err = openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}
	p.NovaV2.Microversion = "2.61" // to include extra specs in flavors.ListDetail()

	p.IronicV1, err = openstack.NewBareMetalV1(provider, eo)
	if err != nil {
		return err
	}
	p.IronicV1.Microversion = "1.61" // for node attribute "retired"
	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) PluginTypeID() string {
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
		Capabilities    string             `json:"capabilities"` // e.g. "cpu_txt:true,cpu_aes:true"
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
var nodeNameRx = regexp.MustCompile(`^node(?:swift)?\d+-((?:b[bm]|ap|md|st|swf|gp)\d+)$`)

// As a special case, nodes in the control plane do not belong to any
// user-accessible Nova aggregates, so we cannot establish an AZ association.
// However, we don't really need the AZ association anyway: AZ capacities are
// presented to the customer as a sort of manual scheduling hint, but CP nodes
// are earmarked for internal use and thus are not relevant there.
var cpNodeNameRx = regexp.MustCompile(`^node(?:swift)?\d+-(cp\d+)$`)

// Scrape implements the core.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) Scrape(ctx context.Context, _ core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (result map[db.ServiceType]map[liquid.ResourceName]core.PerAZ[core.CapacityData], serializedMetrics []byte, err error) {
	// collect info about flavors with separate instance quota
	flavorNames, err := p.FlavorAliases.ListFlavorsWithSeparateInstanceQuota(ctx, p.NovaV2)
	if err != nil {
		return nil, nil, err
	}

	// we are going to report capacity for all per-flavor instance quotas
	resultCompute := make(map[liquid.ResourceName]core.PerAZ[core.CapacityData])
	for _, flavorName := range flavorNames {
		if p.FlavorNameSelection.MatchFlavorName(flavorName) {
			resName := p.FlavorAliases.LimesResourceNameForFlavor(flavorName)
			resultCompute[resName] = make(core.PerAZ[core.CapacityData])
		}
	}

	// count Ironic nodes
	allPages, err := ironicNodesListDetail(p.IronicV1).AllPages(ctx)
	if err != nil {
		return nil, nil, err
	}
	var allNodes []ironicNode
	err = ironicExtractNodesInto(allPages, &allNodes)
	if err != nil {
		return nil, nil, err
	}

	// enumerate aggregates for establishing the hypervisor <-> AZ mapping
	page, err := aggregates.List(p.NovaV2).AllPages(ctx)
	if err != nil {
		return nil, nil, err
	}
	allAggregates, err := aggregates.ExtractAggregates(page)
	if err != nil {
		return nil, nil, err
	}

	// Ironic bPods are expected to be listed as compute hosts assigned to
	// host aggregates in the format: "nova-compute-ironic-xxxx".
	azForHostStub := make(map[string]limes.AvailabilityZone)
	for _, aggr := range allAggregates {
		az := limes.AvailabilityZone(aggr.AvailabilityZone)
		if az == "" {
			continue
		}
		for _, host := range aggr.Hosts {
			if host == "nova-compute-ironic" {
				azForHostStub["bpod001"] = az // hardcoded for the few nodes using legacy naming convention
			} else {
				match := computeHostStubRx.FindStringSubmatch(host)
				if match == nil {
					logg.Error(`compute host %q does not match the "nova-compute-(ironic-)xxxx" naming convention`, host)
				} else {
					azForHostStub[match[1]] = az
				}
			}
		}
	}

	var metrics capacitySapccIronicSerializedMetrics
	for _, node := range allNodes {
		// do not consider nodes that have not been made available for provisioning yet
		if !isAvailableProvisionState[node.StableProvisionState()] {
			continue
		}

		// do not consider nodes that are slated for decommissioning
		// (no domain quota should be given out for that capacity anymore)
		var nodeInfo struct {
			Retired bool `json:"retired"`
		}
		err := nodes.Get(ctx, p.IronicV1, node.ID).ExtractInto(&nodeInfo)
		if err != nil {
			return nil, nil, err
		}
		if nodeInfo.Retired {
			logg.Debug("ignoring Ironic node %q (%s) because it is marked for retirement", node.Name, node.ID)
			metrics.RetiredNodeNames = append(metrics.RetiredNodeNames, node.Name)
			//NOTE: Ignoring of retired capacity is currently disabled pending clarification with billing/controlling on how to proceed.
			// continue
		}

		matched := false
		for _, flavorName := range flavorNames {
			if node.Matches(flavorName) {
				logg.Debug("Ironic node %q (%s) matches flavor %s", node.Name, node.ID, flavorName)

				var nodeAZ limes.AvailabilityZone
				if match := cpNodeNameRx.FindStringSubmatch(node.Name); match != nil {
					// special case as explained above (near definition of `cpNodeNameRx`)
					nodeAZ = limes.AvailabilityZone(match[1])
				} else if match := nodeNameRx.FindStringSubmatch(node.Name); match != nil {
					nodeAZ = azForHostStub[match[1]]
					if nodeAZ == "" {
						logg.Info("Ironic node %q (%s) does not match any compute host from host aggregates", node.Name, node.ID)
						nodeAZ = limes.AvailabilityZoneUnknown
					}
				} else {
					logg.Error(`Ironic node %q (%s) does not match the "nodeXXX-{bm,bb,ap,md,st,swf}YYY" naming convention`, node.Name, node.ID)
				}

				resName := p.FlavorAliases.LimesResourceNameForFlavor(flavorName)
				data := resultCompute[resName][nodeAZ]
				if data == nil {
					data = &core.CapacityData{}
					resultCompute[resName][nodeAZ] = data
				}

				data.Capacity++
				if node.StableProvisionState() == "active" {
					if data.Usage == nil {
						data.Usage = p2u64(0)
					}
					*data.Usage++
				}

				if p.WithSubcapacities {
					sub := map[string]any{
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
			metrics.UnmatchedNodeNames = append(metrics.UnmatchedNodeNames, node.Name)
		}
	}

	serializedMetrics, err = json.Marshal(metrics)
	return map[db.ServiceType]map[liquid.ResourceName]core.PerAZ[core.CapacityData]{"compute": resultCompute}, serializedMetrics, err
}

var (
	ironicUnmatchedNodesGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "limes_unmatched_ironic_nodes",
		Help: "Number of available/active Ironic nodes without matching flavor.",
	})
	ironicRetiredNodesGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "limes_retired_ironic_nodes",
		Help: "Number of Ironic nodes that are marked for retirement.",
	})
)

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	ironicRetiredNodesGauge.Describe(ch)
	ironicUnmatchedNodesGauge.Describe(ch)
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacitySapccIronicPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte, capacitorID string) error {
	if len(serializedMetrics) == 0 {
		return nil
	}
	var metrics capacitySapccIronicSerializedMetrics
	err := json.Unmarshal(serializedMetrics, &metrics)
	if err != nil {
		return err
	}

	descCh := make(chan *prometheus.Desc, 1)
	ironicUnmatchedNodesGauge.Describe(descCh)
	ironicUnmatchedNodesDesc := <-descCh
	ironicRetiredNodesGauge.Describe(descCh)
	ironicRetiredNodesDesc := <-descCh

	ch <- prometheus.MustNewConstMetric(
		ironicUnmatchedNodesDesc,
		prometheus.GaugeValue, float64(len(metrics.UnmatchedNodeNames)),
	)
	ch <- prometheus.MustNewConstMetric(
		ironicRetiredNodesDesc,
		prometheus.GaugeValue, float64(len(metrics.RetiredNodeNames)),
	)
	return nil
}

func (n ironicNode) Matches(flavorName string) bool {
	if n.ResourceClass != nil {
		return *n.ResourceClass == flavorName
	}
	return false
}
