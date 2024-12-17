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
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/sapcc/go-api-declarations/liquid"
)

type Logic struct {
	NovaV2 *gophercloud.ServiceClient `json:"-"`
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.NovaV2, err = openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}
	l.NovaV2.Microversion = "2.61" // to include extra specs in flavors.ListDetail()

	return nil
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
			resources[liquid.ResourceName("instances_"+f.Name)] = liquid.ResourceInfo{
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
	}, nil
}

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	return liquid.ServiceCapacityReport{}, errors.New("TODO")
}

// ScanUsage implements the liquidapi.Logic interface.
func (l *Logic) ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error) {
	return liquid.ServiceUsageReport{}, errors.New("TODO")
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	return errors.New("TODO")
}
