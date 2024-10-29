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
	"fmt"
	"slices"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/limits"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/liquids"
)

// ScanUsage implements the liquidapi.Logic interface.
func (l *Logic) ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error) {
	// collect quota and usage from Nova
	var limitsData struct {
		Limits struct {
			AbsolutePerFlavor map[string]struct {
				MaxTotalInstances  int64  `json:"maxTotalInstances"`
				TotalInstancesUsed uint64 `json:"totalInstancesUsed"`
			} `json:"absolutePerFlavor"`
		} `json:"limits"`
	}
	err := limits.Get(ctx, l.NovaV2, limits.GetOpts{TenantID: projectUUID}).ExtractInto(&limitsData)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	// build basic report structure
	resources := make(map[liquid.ResourceName]*liquid.ResourceUsageReport, len(serviceInfo.Resources))
	hasUsage := false
	for resName := range serviceInfo.Resources {
		flavorName := flavorNameForResource(resName)
		flavorLimits := limitsData.Limits.AbsolutePerFlavor[flavorName]

		resReport := liquid.ResourceUsageReport{
			Quota: liquids.PointerTo(flavorLimits.MaxTotalInstances),
			PerAZ: liquid.AZResourceUsageReport{Usage: flavorLimits.TotalInstancesUsed}.PrepareForBreakdownInto(req.AllAZs),
		}
		if flavorLimits.TotalInstancesUsed > 0 {
			hasUsage = true
		}
		resources[resName] = &resReport
	}

	// add AZ breakdown and subresources
	//
	// NOTE 1: As an optimization, we're only querying Nova for an instance list
	// if the usage data suggests that there are any relevant instances. This
	// skips a lot of work for the vast majority of projects where there are only
	// virtualized instances and no baremetal usage.
	if hasUsage {
		// NOTE 2: This query style still retrieves information on virtualized
		// instances which is useless to us. It would be more efficient to
		// `GET /servers?flavor=$ID` for each baremetal flavor with `usage > 0`,
		// but this filter gets translated into a filter on the flavor's current
		// instance type ID. If there are instances that were created with an earlier
		// version of the same flavor, they might have a different instance type ID
		// and thus get missed. The implementation as seen here prioritizes precision
		// over performance.
		opts := servers.ListOpts{
			AllTenants: true,
			TenantID:   projectUUID,
		}
		err := servers.List(l.NovaV2, opts).EachPage(ctx, func(ctx context.Context, page pagination.Page) (bool, error) {
			var instances []servers.Server
			err := servers.ExtractServersInto(page, &instances)
			if err != nil {
				return false, err
			}
			for _, instance := range instances {
				err = l.addInstanceToReport(ctx, resources, instance, req.AllAZs)
				if err != nil {
					return false, fmt.Errorf("while inspecting instance %s: %w", instance.ID, err)
				}
			}
			return true, nil
		})
		if err != nil {
			return liquid.ServiceUsageReport{}, err
		}
	}

	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources:   resources,
	}, nil
}

func (l *Logic) addInstanceToReport(ctx context.Context, resources map[liquid.ResourceName]*liquid.ResourceUsageReport, instance servers.Server, allAZs []liquid.AvailabilityZone) error {
	// we only care about instances clearly belonging to one of the baremetal flavors
	flavorName, ok := instance.Flavor["original_name"].(string)
	if !ok {
		return nil
	}
	resReport, ok := resources[resourceNameForFlavorName(flavorName)]
	if !ok {
		return nil
	}

	// count this instance for the AZ breakdown
	az := liquid.AvailabilityZone(instance.AvailabilityZone)
	if !slices.Contains(allAZs, az) {
		az = liquid.AvailabilityZoneUnknown
	}
	resReport.AddLocalizedUsage(az, 1)

	// add subresource if requested
	if l.WithSubresources {
		builder := liquid.SubresourceBuilder[InstanceAttributes]{
			ID:   instance.ID,
			Name: instance.Name,
			Attributes: InstanceAttributes{
				Status:   instance.Status,
				Metadata: instance.Metadata,
				OSType:   l.OSTypeProber.Get(ctx, instance),
			},
		}
		if instance.Tags != nil {
			builder.Attributes.Tags = *instance.Tags
		}
		subresource, err := builder.Finalize()
		if err != nil {
			return err
		}
		resReport.PerAZ[az].Subresources = append(resReport.PerAZ[az].Subresources, subresource)
	}

	return nil
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	opts := make(novaQuotaUpdateOpts, len(serviceInfo.Resources))
	for resName := range serviceInfo.Resources {
		flavorName := flavorNameForResource(resName)
		opts["instances_"+flavorName] = req.Resources[resName].Quota
	}
	return quotasets.Update(ctx, l.NovaV2, projectUUID, opts).Err
}

////////////////////////////////////////////////////////////////////////////////
// internal types for capacity reporting

// InstanceAttributes is the Attributes payload type for an Ironic-based Nova instance subresource.
type InstanceAttributes struct {
	// base metadata
	Status   string            `json:"status"`
	Metadata map[string]string `json:"metadata"`
	Tags     []string          `json:"tags"`
	// information from image
	OSType string `json:"os_type"`
}

////////////////////////////////////////////////////////////////////////////////
// custom types for OpenStack APIs

type novaQuotaUpdateOpts map[string]uint64

func (opts novaQuotaUpdateOpts) ToComputeQuotaUpdateMap() (map[string]any, error) {
	return map[string]any{"quota_set": opts}, nil
}
