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
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/sapcc/go-api-declarations/liquid"
)

type Logic struct {
	// configuration
	HypervisorType   string `yaml:"hypervisor_type"`
	WithSubresources bool   `yaml:"with_subresources"`
	// connections
	NovaV2            *gophercloud.ServiceClient `yaml:"-"`
	OSTypeProber      *OSTypeProber              `yaml:"-"`
	ServerGroupProber *ServerGroupProber         `yaml:"-"`
	// computed state
	ignoredFlavorNames []string                                `yaml:"-"`
	hasPooledResource  map[string]map[liquid.ResourceName]bool `yaml:"-"`
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.NovaV2, err = openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}
	l.NovaV2.Microversion = "2.61" // to include extra specs in flavors.ListDetail()
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

	// SAPCC extension: Nova may report quotas with this name pattern in its quota sets and quota class sets.
	// If it does, instances with flavors that have the extra spec `quota:hw_version` set to the first match
	// group of this regexp will count towards those quotas instead of the regular `cores/instances/ram` quotas.
	//
	// This initialization enumerates which such pooled resources exist.
	defaultQuotaClassSet, err := getDefaultQuotaClassSet(ctx, l.NovaV2)
	if err != nil {
		return fmt.Errorf("while enumerating default quotas: %w", err)
	}
	l.hasPooledResource = make(map[string]map[liquid.ResourceName]bool)
	hwVersionResourceRx := regexp.MustCompile(`^hw_version_(\S+)_(cores|instances|ram)$`)
	for resourceName := range defaultQuotaClassSet {
		match := hwVersionResourceRx.FindStringSubmatch(resourceName)
		if match == nil {
			continue
		}
		hwVersion, baseResourceName := match[1], liquid.ResourceName(match[2])

		if l.hasPooledResource[hwVersion] == nil {
			l.hasPooledResource[hwVersion] = make(map[liquid.ResourceName]bool)
		}
		l.hasPooledResource[hwVersion][baseResourceName] = true
	}

	return FlavorSelection{}.ForeachFlavor(ctx, l.NovaV2, func(f flavors.Flavor) error {
		if IsIronicFlavor(f) {
			l.ignoredFlavorNames = append(l.ignoredFlavorNames, f.Name)
		}
		return nil
	})
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
	resources := map[liquid.ResourceName]liquid.ResourceInfo{
		"cores": {
			Unit:        liquid.UnitNone,
			HasCapacity: true,
			HasQuota:    true,
		},
		"instances": {
			Unit:        liquid.UnitNone,
			HasCapacity: true,
			HasQuota:    true,
		},
		"ram": {
			Unit:        liquid.UnitMebibytes,
			HasCapacity: true,
			HasQuota:    true,
		},
		"server_groups": {
			Unit:     liquid.UnitNone,
			HasQuota: true,
		},
		"server_group_members": {
			Unit:     liquid.UnitNone,
			HasQuota: true,
		},
	}

	err := FlavorSelection{}.ForeachFlavor(ctx, l.NovaV2, func(f flavors.Flavor) error {
		if IsIronicFlavor(f) {
			return nil
		}
		if f.ExtraSpecs["quota:separate"] == "true" {
			resources[liquid.ResourceName(f.Name)] = liquid.ResourceInfo{
				Unit:        liquid.UnitNone,
				HasCapacity: true,
				HasQuota:    true,
			}
		}
		return nil
	})
	if err != nil {
		return liquid.ServiceInfo{}, err
	}

	return liquid.ServiceInfo{
		Version:   time.Now().Unix(),
		Resources: resources,
		UsageMetricFamilies: map[liquid.MetricName]liquid.MetricFamilyInfo{
			"liquid_nova_instance_counts_by_hypervisor": {
				Type:      liquid.MetricTypeCounter,                                 // TODO: Counter or Gauge?
				Help:      "Total number of instances, grouped by hypervisor type.", // TODO: Is this correct? Liquid nova only has one hypervisor type if I understand correctly
				LabelKeys: []string{"hypervisor_type"},
			},
			"liquid_nova_instance_counts_bvy_hypervisor_and_az": {
				Type:      liquid.MetricTypeCounter, // TODO: Same as above
				Help:      "Total number of instances in each availability zone, grouped by hypervisor type.",
				LabelKeys: []string{"hypervisor_type", "availability_zone"},
			},
		},
	}, nil
}

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	return liquid.ServiceCapacityReport{}, errors.New("TODO")
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	return errors.New("TODO")
}

func (l *Logic) IgnoreFlavor(flavorName string) bool {
	return slices.Contains(l.ignoredFlavorNames, flavorName)
}
