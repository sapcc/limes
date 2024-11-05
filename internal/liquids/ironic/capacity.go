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

package ironic

import (
	"context"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/baremetal/v1/nodes"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/aggregates"
	"github.com/gophercloud/gophercloud/v2/openstack/placement/v1/resourceproviders"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"
)

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	// enumerate aggregates (these contain AZ information)
	page, err := aggregates.List(l.NovaV2).AllPages(ctx)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}
	allAggregates, err := ExtractAggregates(page)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}

	// enumerate resource providers per aggregate
	//
	// Each Ironic node is a resource provider in Placement,
	// so we can use this to establish an AZ-to-node relationship.
	azForResourceProviderUUID := make(map[string]liquid.AvailabilityZone)
	for _, aggr := range allAggregates {
		az := liquid.AvailabilityZone(aggr.AvailabilityZone)
		if az == "" || !slices.Contains(req.AllAZs, az) {
			// we are only interested in aggregates that are connected to AZs that we know
			continue
		}

		opts := resourceproviders.ListOpts{
			MemberOf: aggr.UUID,
		}
		page, err := resourceproviders.List(l.PlacementV1, opts).AllPages(ctx)
		if err != nil {
			return liquid.ServiceCapacityReport{}, err
		}
		allResourceProviders, err := resourceproviders.ExtractResourceProviders(page)
		if err != nil {
			return liquid.ServiceCapacityReport{}, err
		}
		for _, rp := range allResourceProviders {
			azForResourceProviderUUID[rp.UUID] = az
		}
	}

	// enumerate Ironic nodes and sort by resource class (which should contain the flavor name)
	allPages, err := ListNodesDetail(l.IronicV1).AllPages(ctx)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}
	var allNodes []Node
	err = ExtractNodesInto(allPages, &allNodes)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}
	nodesByFlavorName := make(map[string][]Node)
	for _, node := range allNodes {
		nodesByFlavorName[node.ResourceClass] = append(nodesByFlavorName[node.ResourceClass], node)
	}

	// build result
	var (
		resources        = make(map[liquid.ResourceName]*liquid.ResourceCapacityReport, len(serviceInfo.Resources))
		retiredNodeCount = 0
	)
	for resName := range serviceInfo.Resources {
		flavorName := flavorNameForResource(resName)
		matchingNodes := nodesByFlavorName[flavorName]
		delete(nodesByFlavorName, flavorName)

		perAZ := make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport)
		for _, node := range matchingNodes {
			logg.Debug("Ironic node %q (%s) matches flavor %s", node.Name, node.UUID, flavorName)

			// do not consider nodes that have not been made available for provisioning yet
			if !isAvailableProvisionState[node.StableProvisionState()] {
				logg.Info("ignoring Ironic node %q (%s) because of state %q", node.Name, node.UUID, node.StableProvisionState())
				continue
			}

			// do not consider nodes that are slated for decommissioning
			// (no quota should be given out for this capacity anymore)
			if node.Retired {
				logg.Info("should be ignoring Ironic node %q (%s) because it is marked for retirement", node.Name, node.UUID)
				retiredNodeCount++
				//NOTE: Ignoring of retired capacity is currently disabled pending clarification with billing/controlling on how to proceed.
				// continue
			}

			az := azForResourceProviderUUID[node.UUID]
			if !slices.Contains(req.AllAZs, az) {
				az = liquid.AvailabilityZoneUnknown
			}
			if perAZ[az] == nil {
				perAZ[az] = &liquid.AZResourceCapacityReport{Capacity: 0}
			}
			perAZ[az].Capacity++

			if l.WithSubcapacities {
				subcapacity, err := liquid.SubcapacityBuilder[NodeAttributes]{
					ID:       node.UUID,
					Name:     node.Name,
					Capacity: 1,
					Attributes: NodeAttributes{
						ProvisionState:       node.ProvisionState,
						TargetProvisionState: node.TargetProvisionState,
						SerialNumber:         node.Properties.SerialNumber,
						InstanceID:           node.InstanceID,
					},
				}.Finalize()
				if err != nil {
					return liquid.ServiceCapacityReport{}, err
				}
				perAZ[az].Subcapacities = append(perAZ[az].Subcapacities, subcapacity)
			}
		}
		resources[resName] = &liquid.ResourceCapacityReport{PerAZ: perAZ}
	}

	// count nodes that could not be matched to a flavor
	unmatchedNodeCount := 0
	for flavorName, nodes := range nodesByFlavorName {
		for _, node := range nodes {
			logg.Error("Ironic node %q (%s) does not match any baremetal flavor (resource_class = %q)", node.Name, node.UUID, flavorName)
			unmatchedNodeCount++
		}
	}

	return liquid.ServiceCapacityReport{
		InfoVersion: serviceInfo.Version,
		Resources:   resources,
		Metrics: map[liquid.MetricName][]liquid.Metric{
			"limes_retired_ironic_nodes":   {{Value: float64(retiredNodeCount)}},
			"limes_unmatched_ironic_nodes": {{Value: float64(unmatchedNodeCount)}},
		},
	}, nil
}

////////////////////////////////////////////////////////////////////////////////
// internal types for capacity reporting

// NodeAttributes is the Attributes payload type for an Ironic subcapacity.
type NodeAttributes struct {
	ProvisionState       string  `json:"provision_state"`
	TargetProvisionState *string `json:"target_provision_state,omitempty"`
	SerialNumber         string  `json:"serial_number,omitempty"`
	InstanceID           *string `json:"instance_id,omitempty"`
}

// This is a list of all *stable* provisioning states of an Ironic node.
// States with map to false will cause that node to not be considered when counting capacity.
//
// Reference: https://github.com/openstack/ironic/blob/master/ironic/common/states.py
var isAvailableProvisionState = map[string]bool{
	"enroll":     false,
	"manageable": false,
	"available":  true,
	"active":     true,
	"error":      true, // occurs during delete or rebuild, so node was active before
	"rescue":     true,
}

////////////////////////////////////////////////////////////////////////////////
// custom types for Ironic APIs

// Aggregate is like `aggregates.Aggregate`, but contains attributes missing there.
type Aggregate struct {
	AvailabilityZone string `json:"availability_zone"`
	UUID             string `json:"uuid"`
}

// ExtractAggregates is like `aggregates.ExtractAggregates()`, but using our custom Aggregate type.
func ExtractAggregates(p pagination.Page) ([]Aggregate, error) {
	var a struct {
		Aggregates []Aggregate `json:"aggregates"`
	}
	err := (p.(aggregates.AggregatesPage)).ExtractInto(&a)
	return a.Aggregates, err
}

// Node is like `nodes.Node`, but contains attributes missing there.
type Node struct {
	UUID                 string  `json:"uuid"`
	Name                 string  `json:"name"`
	ProvisionState       string  `json:"provision_state"`
	TargetProvisionState *string `json:"target_provision_state"`
	InstanceID           *string `json:"instance_uuid"`
	ResourceClass        string  `json:"resource_class"`
	Retired              bool    `json:"retired"`
	Properties           struct {
		SerialNumber string `json:"serial"`
	} `json:"properties"`
}

func (n Node) StableProvisionState() string {
	if n.TargetProvisionState != nil {
		return *n.TargetProvisionState
	}
	return n.ProvisionState
}

// ListNodesDetail is like `nodes.ListDetail(client, nil)`,
// but works around <https://github.com/gophercloud/gophercloud/issues/2431>.
func ListNodesDetail(client *gophercloud.ServiceClient) pagination.Pager {
	url := client.ServiceURL("nodes", "detail")
	return pagination.NewPager(client, url, func(r pagination.PageResult) pagination.Page {
		return nodePage{nodes.NodePage{LinkedPageBase: pagination.LinkedPageBase{PageResult: r}}}
	})
}

// ExtractNodesInto is like `nodes.ExtractNodesInto()`, but using our custom page type.
func ExtractNodesInto(r pagination.Page, v any) error {
	return r.(nodePage).Result.ExtractIntoSlicePtr(v, "nodes")
}

type nodePage struct {
	nodes.NodePage
}

// NextPageURL uses the response's embedded link reference to navigate to the
// next page of results.
func (r nodePage) NextPageURL() (string, error) {
	var s struct {
		Next string `json:"next"`
	}
	err := r.ExtractInto(&s)
	if err != nil {
		return "", err
	}
	if s.Next != "" {
		return s.Next, nil
	}
	// fallback
	return r.NodePage.NextPageURL()
}