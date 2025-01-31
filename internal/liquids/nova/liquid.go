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
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
)

type Logic struct {
	// configuration
	HypervisorSelection HypervisorSelection `json:"hypervisor_selection"`
	FlavorSelection     FlavorSelection     `json:"flavor_selection"`
	WithSubresources    bool                `json:"with_subresources"`
	WithSubcapacities   bool                `json:"with_subcapacities"`
	BinpackBehavior     BinpackBehavior     `json:"binpack_behavior"`
	IgnoredTraits       []string            `json:"ignored_traits"`
	// connections
	NovaV2            *gophercloud.ServiceClient `json:"-"`
	PlacementV1       *gophercloud.ServiceClient `json:"-"`
	OSTypeProber      *OSTypeProber              `json:"-"`
	ServerGroupProber *ServerGroupProber         `json:"-"`
	// computed state
	ignoredFlavorNames liquidapi.State[[]string]                                `json:"-"`
	hasPooledResource  liquidapi.State[map[string]map[liquid.ResourceName]bool] `json:"-"`
	hwVersionResources liquidapi.State[[]liquid.ResourceName]                   `json:"-"`
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.NovaV2, err = openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}
	l.NovaV2.Microversion = "2.61" // to include extra specs in flavors.ListDetail()

	l.PlacementV1, err = openstack.NewPlacementV1(provider, eo)
	if err != nil {
		return err
	}
	l.PlacementV1.Microversion = "1.6" // for traits endpoint

	cinderV3, err := openstack.NewBlockStorageV3(provider, eo)
	if err != nil {
		return err
	}

	glanceV2, err := openstack.NewImageV2(provider, eo)
	if err != nil {
		return err
	}
	l.OSTypeProber = NewOSTypeProber(l.NovaV2, cinderV3, glanceV2)
	l.ServerGroupProber = NewServerGroupProber(l.NovaV2)

	return nil
}

func getDefaultQuotaClassSet(ctx context.Context, novaV2 *gophercloud.ServiceClient) (map[string]any, error) {
	url := novaV2.ServiceURL("os-quota-class-sets", "default")
	var result gophercloud.Result
	_, err := novaV2.Get(ctx, url, &result.Body, nil) //nolint:bodyclose
	if err != nil {
		return nil, err
	}

	var body struct {
		//NOTE: cannot use map[string]int64 here because this object contains the
		// field "id": "default" (curse you, untyped JSON)
		QuotaClassSet map[string]any `json:"quota_class_set"`
	}
	err = result.ExtractInto(&body)
	return body.QuotaClassSet, err
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	// SAPCC extension: Nova may report quotas with this name pattern in its quota sets and quota class sets.
	// If it does, instances with flavors that have the extra spec `quota:hw_version` set to the first match
	// group of this regexp will count towards those quotas instead of the regular `cores/instances/ram` quotas.
	//
	// This initialization enumerates which such pooled resources exist.
	defaultQuotaClassSet, err := getDefaultQuotaClassSet(ctx, l.NovaV2)
	if err != nil {
		return liquid.ServiceInfo{}, fmt.Errorf("while enumerating default quotas: %w", err)
	}
	hasPooledResource := make(map[string]map[liquid.ResourceName]bool)
	var hwVersionResources []liquid.ResourceName
	hwVersionResourceRx := regexp.MustCompile(`^hw_version_(\S+)_(cores|instances|ram)$`)
	for resourceName := range defaultQuotaClassSet {
		match := hwVersionResourceRx.FindStringSubmatch(resourceName)
		if match == nil {
			continue
		}
		hwVersion, baseResourceName := match[1], liquid.ResourceName(match[2])

		hwVersionResources = append(hwVersionResources, liquid.ResourceName(resourceName))

		if hasPooledResource[hwVersion] == nil {
			hasPooledResource[hwVersion] = make(map[liquid.ResourceName]bool)
		}
		hasPooledResource[hwVersion][baseResourceName] = true
	}

	var ignoredFlavorNames []string
	err = FlavorSelection{}.ForeachFlavor(ctx, l.NovaV2, func(f flavors.Flavor) error {
		if IsIronicFlavor(f) {
			ignoredFlavorNames = append(ignoredFlavorNames, f.Name)
		}
		return nil
	})
	if err != nil {
		return liquid.ServiceInfo{}, err
	}
	resources := map[liquid.ResourceName]liquid.ResourceInfo{
		"cores": {
			Unit:                liquid.UnitNone,
			Topology:            liquid.AZAwareResourceTopology,
			HasCapacity:         true,
			HasQuota:            true,
			NeedsResourceDemand: true,
		},
		"instances": {
			Unit:                liquid.UnitNone,
			Topology:            liquid.AZAwareResourceTopology,
			HasCapacity:         true,
			HasQuota:            true,
			NeedsResourceDemand: true,
		},
		"ram": {
			Unit:                liquid.UnitMebibytes,
			Topology:            liquid.AZAwareResourceTopology,
			HasCapacity:         true,
			HasQuota:            true,
			NeedsResourceDemand: true,
		},
		"server_groups": {
			Unit:     liquid.UnitNone,
			Topology: liquid.FlatResourceTopology,
			HasQuota: true,
		},
		"server_group_members": {
			Unit:     liquid.UnitNone,
			Topology: liquid.FlatResourceTopology,
			HasQuota: true,
		},
	}

	err = FlavorSelection{}.ForeachFlavor(ctx, l.NovaV2, func(f flavors.Flavor) error {
		if IsIronicFlavor(f) {
			return nil
		}
		if IsSplitFlavor(f) {
			resources[ResourceNameForFlavor(f.Name)] = liquid.ResourceInfo{
				Unit:                liquid.UnitNone,
				Topology:            liquid.AZAwareResourceTopology,
				HasCapacity:         true,
				HasQuota:            true,
				NeedsResourceDemand: true,
			}
		}
		return nil
	})
	if err != nil {
		return liquid.ServiceInfo{}, err
	}

	for _, resourceName := range hwVersionResources {
		unit := liquid.UnitNone
		if strings.HasSuffix(string(resourceName), "ram") {
			unit = liquid.UnitMebibytes
		}
		resources[resourceName] = liquid.ResourceInfo{
			Unit:     unit,
			HasQuota: true,
		}
	}

	l.hasPooledResource.Set(hasPooledResource)
	l.hwVersionResources.Set(hwVersionResources)
	l.ignoredFlavorNames.Set(ignoredFlavorNames)

	return liquid.ServiceInfo{
		Version:   time.Now().Unix(),
		Resources: resources,
	}, nil
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	opts := make(novaQuotaUpdateOpts, len(serviceInfo.Resources))
	for resName := range serviceInfo.Resources {
		opts[string(resName)] = req.Resources[resName].Quota
	}
	return quotasets.Update(ctx, l.NovaV2, projectUUID, opts).Err
}

func (l *Logic) IsIgnoredFlavor(flavorName string) bool {
	return slices.Contains(l.ignoredFlavorNames.Get(), flavorName)
}

////////////////////////////////////////////////////////////////////////////////
// custom types for OpenStack APIs

type novaQuotaUpdateOpts map[string]uint64

func (opts novaQuotaUpdateOpts) ToComputeQuotaUpdateMap() (map[string]any, error) {
	return map[string]any{"quota_set": opts}, nil
}
