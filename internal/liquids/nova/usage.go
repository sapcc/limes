// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package nova

import (
	"context"
	"fmt"

	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/limits"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
)

func (l *Logic) pooledResourceName(hwVersion string, base liquid.ResourceName) liquid.ResourceName {
	// `base` is one of "cores", "instances" or "ram"
	if hwVersion == "" {
		return base
	}

	// if we saw a "quota:hw_version" extra spec on the instance's flavor, use the appropriate resource if it exists
	if l.hasPooledResource.Get()[hwVersion][base] {
		return liquid.ResourceName(fmt.Sprintf("hw_version_%s_%s", hwVersion, base))
	}
	return base
}

func (l *Logic) ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error) {
	var limitsData struct {
		Limits struct {
			Absolute struct {
				MaxTotalCores         int64  `json:"maxTotalCores"`
				MaxTotalInstances     int64  `json:"maxTotalInstances"`
				MaxTotalRAMSize       int64  `json:"maxTotalRAMSize"`
				MaxServerGroups       int64  `json:"maxServerGroups"`
				MaxServerGroupMembers int64  `json:"maxServerGroupMembers"`
				TotalCoresUsed        uint64 `json:"totalCoresUsed"`
				TotalInstancesUsed    uint64 `json:"totalInstancesUsed"`
				TotalRAMUsed          uint64 `json:"totalRAMUsed"`
				TotalServerGroupsUsed uint64 `json:"totalServerGroupsUsed"`
			} `json:"absolute"`
			AbsolutePerFlavor map[string]struct {
				MaxTotalInstances  int64  `json:"maxTotalInstances"`
				TotalInstancesUsed uint64 `json:"totalInstancesUsed"`
			} `json:"absolutePerFlavor"`
			AbsolutePerHWVersion map[string]struct {
				MaxTotalCores      int64  `json:"maxTotalCores"`
				MaxTotalInstances  int64  `json:"maxTotalInstances"`
				MaxTotalRAMSize    int64  `json:"maxTotalRAMSize"`
				TotalCoresUsed     uint64 `json:"totalCoresUsed"`
				TotalInstancesUsed uint64 `json:"totalInstancesUsed"`
				TotalRAMUsed       uint64 `json:"totalRAMUsed"`
			} `json:"absolutePerHwVersion"`
		} `json:"limits"`
	}
	err := limits.Get(ctx, l.NovaV2, limits.GetOpts{TenantID: projectUUID}).ExtractInto(&limitsData)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}
	absoluteLimits := limitsData.Limits.Absolute
	var totalServerGroupMembersUsed uint64
	if absoluteLimits.TotalServerGroupsUsed > 0 {
		totalServerGroupMembersUsed, err = l.ServerGroupProber.GetMemberUsageForProject(ctx, projectUUID)
		if err != nil {
			return liquid.ServiceUsageReport{}, err
		}
	}

	// initialize `Resources`
	resources := map[liquid.ResourceName]*liquid.ResourceUsageReport{
		"cores": {
			Quota: Some(absoluteLimits.MaxTotalCores),
			PerAZ: liquid.AZResourceUsageReport{Usage: absoluteLimits.TotalCoresUsed}.PrepareForBreakdownInto(req.AllAZs),
		},
		"instances": {
			Quota: Some(absoluteLimits.MaxTotalInstances),
			PerAZ: liquid.AZResourceUsageReport{Usage: absoluteLimits.TotalInstancesUsed}.PrepareForBreakdownInto(req.AllAZs),
		},
		"ram": {
			Quota: Some(absoluteLimits.MaxTotalRAMSize),
			PerAZ: liquid.AZResourceUsageReport{Usage: absoluteLimits.TotalRAMUsed}.PrepareForBreakdownInto(req.AllAZs),
		},
		"server_groups": {
			Quota: Some(absoluteLimits.MaxServerGroups),
			PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: absoluteLimits.TotalServerGroupsUsed}),
		},
		"server_group_members": {
			Quota: Some(absoluteLimits.MaxServerGroupMembers),
			PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: totalServerGroupMembersUsed}),
		},
	}
	for flavorName, flavorLimits := range limitsData.Limits.AbsolutePerFlavor {
		if l.IsIgnoredFlavor(flavorName) {
			continue
		}
		resourceName := ResourceNameForFlavor(flavorName)
		resources[resourceName] = &liquid.ResourceUsageReport{
			Quota: Some(flavorLimits.MaxTotalInstances),
			PerAZ: liquid.AZResourceUsageReport{Usage: flavorLimits.TotalInstancesUsed}.PrepareForBreakdownInto(req.AllAZs),
		}
	}
	for hwVersion, limits := range limitsData.Limits.AbsolutePerHWVersion {
		if l.hasPooledResource.Get()[hwVersion]["cores"] {
			resources[l.pooledResourceName(hwVersion, "cores")] = &liquid.ResourceUsageReport{
				Quota: Some(limits.MaxTotalCores),
				PerAZ: liquid.AZResourceUsageReport{Usage: limits.TotalCoresUsed}.PrepareForBreakdownInto(req.AllAZs),
			}
		}
		if l.hasPooledResource.Get()[hwVersion]["instances"] {
			resources[l.pooledResourceName(hwVersion, "instances")] = &liquid.ResourceUsageReport{
				Quota: Some(limits.MaxTotalInstances),
				PerAZ: liquid.AZResourceUsageReport{Usage: limits.TotalInstancesUsed}.PrepareForBreakdownInto(req.AllAZs),
			}
		}
		if l.hasPooledResource.Get()[hwVersion]["ram"] {
			resources[l.pooledResourceName(hwVersion, "ram")] = &liquid.ResourceUsageReport{
				Quota: Some(limits.MaxTotalRAMSize),
				PerAZ: liquid.AZResourceUsageReport{Usage: limits.TotalRAMUsed}.PrepareForBreakdownInto(req.AllAZs),
			}
		}
	}

	// Nova does not have a native API for AZ-aware usage reporting,
	// so we will obtain AZ-aware usage stats by counting up all subresources,
	// even if we don't end up showing them in the API
	allSubresourceBuilders, err := l.buildInstanceSubresources(ctx, projectUUID, req.AllAZs)
	if err != nil {
		return liquid.ServiceUsageReport{}, fmt.Errorf("while collecting instance data: %w", err)
	}

	for _, subresBuilder := range allSubresourceBuilders {
		attrs := subresBuilder.Attributes

		az := attrs.AZ

		if l.IsIgnoredFlavor(attrs.Flavor.Name) {
			continue
		}

		// use separate instance resource if we have a matching "instances_$FLAVOR" resource
		instanceResourceName := ResourceNameForFlavor(attrs.Flavor.Name)
		isPooled := false
		if _, exists := resources[instanceResourceName]; !exists {
			// otherwise used the appropriate pooled instance resource
			isPooled = true
			instanceResourceName = l.pooledResourceName(attrs.Flavor.HWVersion, "instances")
		}

		// count subresource towards "instances" (or separate instance resource)
		resources[instanceResourceName].AddLocalizedUsage(az, 1)
		if l.WithSubresources {
			azData := resources[instanceResourceName].PerAZ[az]
			subres, err := subresBuilder.Finalize()
			if err != nil {
				return liquid.ServiceUsageReport{}, fmt.Errorf("could not serialze attributes of subresource: %w", err)
			}
			azData.Subresources = append(azData.Subresources, subres)
		}

		// if counted towards separate instance resource, do not count towards "cores" and "ram"
		if !isPooled {
			continue
		}

		// count towards "cores" and "ram" under the appropriate pooled resource
		resources[l.pooledResourceName(attrs.Flavor.HWVersion, "cores")].AddLocalizedUsage(az, attrs.Flavor.VCPUs)
		resources[l.pooledResourceName(attrs.Flavor.HWVersion, "ram")].AddLocalizedUsage(az, attrs.Flavor.MemoryMiB)
	}

	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources:   resources,
	}, nil
}
