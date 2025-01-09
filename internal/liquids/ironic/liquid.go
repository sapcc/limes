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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/liquids"
	"github.com/sapcc/limes/internal/liquids/nova"
)

type Logic struct {
	// configuration
	WithSubcapacities bool                               `json:"with_subcapacities"`
	WithSubresources  bool                               `json:"with_subresources"`
	NodeToAZOvewrites map[string]liquid.AvailabilityZone `json:"node_to_az_overwrites"`
	// connections
	NovaV2       *gophercloud.ServiceClient `json:"-"`
	IronicV1     *gophercloud.ServiceClient `json:"-"`
	PlacementV1  *gophercloud.ServiceClient `json:"-"`
	OSTypeProber *nova.OSTypeProber         `json:"-"`
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.NovaV2, err = openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}
	l.NovaV2.Microversion = "2.61" // to include extra specs in flavors.ListDetail()

	l.IronicV1, err = openstack.NewBareMetalV1(provider, eo)
	if err != nil {
		return err
	}
	l.IronicV1.Microversion = "1.61" // for node attribute "retired"

	l.PlacementV1, err = openstack.NewPlacementV1(provider, eo)
	if err != nil {
		return err
	}
	l.PlacementV1.Microversion = "1.3" // for query parameter "member_of" in resource provider listing

	// NOTE: Cinder API access is probably not required if, as I'm expecting,
	// Ironic nodes cannot be booted with a network-attached root disk.
	// But because of how the code is structured, the OSTypeProber needs this client anyway.
	cinderV3, err := openstack.NewBlockStorageV3(provider, eo)
	if err != nil {
		return err
	}
	glanceV2, err := openstack.NewImageV2(provider, eo)
	if err != nil {
		return err
	}
	l.OSTypeProber = nova.NewOSTypeProber(l.NovaV2, cinderV3, glanceV2)

	return nil
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	opts := flavors.ListOpts{AccessType: flavors.AllAccess}
	page, err := flavors.ListDetail(l.NovaV2, &opts).AllPages(ctx)
	if err != nil {
		return liquid.ServiceInfo{}, err
	}
	allFlavors, err := flavors.ExtractFlavors(page)
	if err != nil {
		return liquid.ServiceInfo{}, err
	}

	resources := make(map[liquid.ResourceName]liquid.ResourceInfo)
	for _, flavor := range allFlavors {
		if flavor.ExtraSpecs["capabilities:hypervisor_type"] != "ironic" {
			continue
		}

		buf, err := json.Marshal(FlavorAttributes{
			Cores:     liquids.AtLeastZero(flavor.VCPUs),
			MemoryMiB: liquids.AtLeastZero(flavor.RAM),
			DiskGiB:   liquids.AtLeastZero(flavor.Disk),
		})
		if err != nil {
			return liquid.ServiceInfo{}, fmt.Errorf("while serializing FlavorAttributes: %w", err)
		}

		resources[resourceNameForFlavorName(flavor.Name)] = liquid.ResourceInfo{
			Unit:        liquid.UnitNone,
			Topology:    liquid.AZAwareResourceTopology,
			HasCapacity: true,
			HasQuota:    true,
			Attributes:  json.RawMessage(buf),
		}
	}

	return liquid.ServiceInfo{
		Version:   time.Now().Unix(),
		Resources: resources,
		CapacityMetricFamilies: map[liquid.MetricName]liquid.MetricFamilyInfo{
			"limes_retired_ironic_nodes": {
				Type: liquid.MetricTypeGauge,
				Help: "Number of Ironic nodes that are marked for retirement.",
			},
			"limes_unmatched_ironic_nodes": {
				Type: liquid.MetricTypeGauge,
				Help: "Number of available/active Ironic nodes without matching flavor.",
			},
		},
	}, nil
}

// FlavorAttributes is the Attributes payload type for an Ironic resource of the form `instances_$FLAVOR`.
type FlavorAttributes struct {
	Cores     uint64 `json:"cores"`
	MemoryMiB uint64 `json:"ram_mib"`
	DiskGiB   uint64 `json:"disk_gib"`
}

func resourceNameForFlavorName(flavorName string) liquid.ResourceName {
	return liquid.ResourceName("instances_" + flavorName)
}

func flavorNameForResource(resName liquid.ResourceName) string {
	return strings.TrimPrefix(string(resName), "instances_")
}
