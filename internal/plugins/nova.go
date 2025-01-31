/*******************************************************************************
*
* Copyright 2017-2020 SAP SE
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
	"fmt"
	"maps"
	"math/big"
	"regexp"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/limits"
	"github.com/gophercloud/gophercloud/v2/openstack/compute/v2/quotasets"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/regexpext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/liquids/nova"
)

type novaPlugin struct {
	// configuration
	HypervisorType         string `yaml:"hypervisor_type"`
	SeparateInstanceQuotas struct {
		FlavorNamePattern regexpext.PlainRegexp `yaml:"flavor_name_pattern"`
	} `yaml:"separate_instance_quotas"`
	WithSubresources bool `yaml:"with_subresources"`
	// computed state
	resources          map[liquid.ResourceName]liquid.ResourceInfo `yaml:"-"`
	hasPooledResource  map[string]map[liquid.ResourceName]bool     `yaml:"-"`
	ignoredFlavorNames []string                                    `yaml:"-"`
	// connections
	NovaV2            *gophercloud.ServiceClient `yaml:"-"`
	OSTypeProber      *nova.OSTypeProber         `yaml:"-"`
	ServerGroupProber *nova.ServerGroupProber    `yaml:"-"`
}

var novaDefaultResources = map[liquid.ResourceName]liquid.ResourceInfo{
	"cores":                {Unit: limes.UnitNone, HasQuota: true, Topology: liquid.AZAwareResourceTopology},
	"instances":            {Unit: limes.UnitNone, HasQuota: true, Topology: liquid.AZAwareResourceTopology},
	"ram":                  {Unit: limes.UnitMebibytes, HasQuota: true, Topology: liquid.AZAwareResourceTopology},
	"server_groups":        {Unit: limes.UnitNone, HasQuota: true, Topology: liquid.FlatResourceTopology},
	"server_group_members": {Unit: limes.UnitNone, HasQuota: true, Topology: liquid.FlatResourceTopology},
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &novaPlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *novaPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, serviceType db.ServiceType) (err error) {
	p.resources = maps.Clone(novaDefaultResources)

	p.NovaV2, err = openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}
	p.NovaV2.Microversion = "2.61" // to include extra specs in flavors.ListDetail()
	cinderV3, err := openstack.NewBlockStorageV3(provider, eo)
	if err != nil {
		return err
	}
	glanceV2, err := openstack.NewImageV2(provider, eo)
	if err != nil {
		return err
	}
	p.OSTypeProber = nova.NewOSTypeProber(p.NovaV2, cinderV3, glanceV2)
	p.ServerGroupProber = nova.NewServerGroupProber(p.NovaV2)

	// SAPCC extension: Nova may report quotas with this name pattern in its quota sets and quota class sets.
	// If it does, instances with flavors that have the extra spec `quota:hw_version` set to the first match
	// group of this regexp will count towards those quotas instead of the regular `cores/instances/ram` quotas.
	//
	// This initialization enumerates which such pooled resources exist.
	defaultQuotaClassSet, err := getDefaultQuotaClassSet(ctx, p.NovaV2)
	if err != nil {
		return fmt.Errorf("while enumerating default quotas: %w", err)
	}
	p.hasPooledResource = make(map[string]map[liquid.ResourceName]bool)
	hwVersionResourceRx := regexp.MustCompile(`^hw_version_(\S+)_(cores|instances|ram)$`)
	for resourceName := range defaultQuotaClassSet {
		match := hwVersionResourceRx.FindStringSubmatch(resourceName)
		if match == nil {
			continue
		}
		hwVersion, baseResourceName := match[1], liquid.ResourceName(match[2])

		if p.hasPooledResource[hwVersion] == nil {
			p.hasPooledResource[hwVersion] = make(map[liquid.ResourceName]bool)
		}
		p.hasPooledResource[hwVersion][baseResourceName] = true

		unit := limes.UnitNone
		if baseResourceName == "ram" {
			unit = limes.UnitMebibytes
		}
		p.resources[liquid.ResourceName(resourceName)] = liquid.ResourceInfo{
			Unit:     unit,
			HasQuota: true,
			Topology: liquid.AZAwareResourceTopology,
		}
	}

	// find per-flavor instance resources
	return nova.FlavorSelection{}.ForeachFlavor(ctx, p.NovaV2, func(f flavors.Flavor) error {
		if p.SeparateInstanceQuotas.FlavorNamePattern.MatchString(f.Name) {
			if nova.IsIronicFlavor(f) {
				p.ignoredFlavorNames = append(p.ignoredFlavorNames, f.Name)
			} else if nova.IsSplitFlavor(f) {
				resName := nova.ResourceNameForFlavor(f.Name)
				p.resources[resName] = liquid.ResourceInfo{
					Unit:     limes.UnitNone,
					HasQuota: true,
					Topology: liquid.AZAwareResourceTopology,
				}
			}
		}
		return nil
	})
}

func getDefaultQuotaClassSet(ctx context.Context, novaV2 *gophercloud.ServiceClient) (map[string]any, error) {
	url := novaV2.ServiceURL("os-quota-class-sets", "default")
	var result gophercloud.Result
	_, err := novaV2.Get(ctx, url, &result.Body, nil)
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

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *novaPlugin) PluginTypeID() string {
	return "compute"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *novaPlugin) ServiceInfo() core.ServiceInfo {
	return core.ServiceInfo{
		ProductName: "nova",
		Area:        "compute",
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *novaPlugin) Resources() map[liquid.ResourceName]liquid.ResourceInfo {
	return p.resources
}

// Rates implements the core.QuotaPlugin interface.
func (p *novaPlugin) Rates() map[liquid.RateName]liquid.RateInfo {
	return nil
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *novaPlugin) ScrapeRates(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone, prevSerializedState string) (result map[liquid.RateName]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *novaPlugin) Scrape(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[liquid.ResourceName]core.ResourceData, serializedMetrics []byte, err error) {
	// collect quota and usage from Nova
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
	err = limits.Get(ctx, p.NovaV2, limits.GetOpts{TenantID: project.UUID}).ExtractInto(&limitsData)
	if err != nil {
		return nil, nil, err
	}
	absoluteLimits := limitsData.Limits.Absolute
	var totalServerGroupMembersUsed uint64
	if absoluteLimits.TotalServerGroupsUsed > 0 {
		totalServerGroupMembersUsed, err = p.ServerGroupProber.GetMemberUsageForProject(ctx, project.UUID)
		if err != nil {
			return nil, nil, err
		}
	}

	// initialize `result`
	result = map[liquid.ResourceName]core.ResourceData{
		"cores": {
			Quota:     absoluteLimits.MaxTotalCores,
			UsageData: core.InUnknownAZUnlessEmpty(core.UsageData{Usage: absoluteLimits.TotalCoresUsed}).AndZeroInTheseAZs(allAZs),
		},
		"instances": {
			Quota:     absoluteLimits.MaxTotalInstances,
			UsageData: core.InUnknownAZUnlessEmpty(core.UsageData{Usage: absoluteLimits.TotalInstancesUsed}).AndZeroInTheseAZs(allAZs),
		},
		"ram": {
			Quota:     absoluteLimits.MaxTotalRAMSize,
			UsageData: core.InUnknownAZUnlessEmpty(core.UsageData{Usage: absoluteLimits.TotalRAMUsed}).AndZeroInTheseAZs(allAZs),
		},
		"server_groups": {
			Quota:     absoluteLimits.MaxServerGroups,
			UsageData: core.InAnyAZ(core.UsageData{Usage: absoluteLimits.TotalServerGroupsUsed}),
		},
		"server_group_members": {
			Quota:     absoluteLimits.MaxServerGroupMembers,
			UsageData: core.InAnyAZ(core.UsageData{Usage: totalServerGroupMembersUsed}),
		},
	}
	for flavorName, flavorLimits := range limitsData.Limits.AbsolutePerFlavor {
		if slices.Contains(p.ignoredFlavorNames, flavorName) {
			continue
		}
		resName := nova.ResourceNameForFlavor(flavorName)
		result[resName] = core.ResourceData{
			Quota:     flavorLimits.MaxTotalInstances,
			UsageData: core.InUnknownAZUnlessEmpty(core.UsageData{Usage: flavorLimits.TotalInstancesUsed}).AndZeroInTheseAZs(allAZs),
		}
	}
	for hwVersion, limits := range limitsData.Limits.AbsolutePerHWVersion {
		if p.hasPooledResource[hwVersion]["cores"] {
			result[p.pooledResourceName(hwVersion, "cores")] = core.ResourceData{
				Quota:     limits.MaxTotalCores,
				UsageData: core.InUnknownAZUnlessEmpty(core.UsageData{Usage: limits.TotalCoresUsed}).AndZeroInTheseAZs(allAZs),
			}
		}
		if p.hasPooledResource[hwVersion]["instances"] {
			result[p.pooledResourceName(hwVersion, "instances")] = core.ResourceData{
				Quota:     limits.MaxTotalInstances,
				UsageData: core.InUnknownAZUnlessEmpty(core.UsageData{Usage: limits.TotalInstancesUsed}).AndZeroInTheseAZs(allAZs),
			}
		}
		if p.hasPooledResource[hwVersion]["ram"] {
			result[p.pooledResourceName(hwVersion, "ram")] = core.ResourceData{
				Quota:     limits.MaxTotalRAMSize,
				UsageData: core.InUnknownAZUnlessEmpty(core.UsageData{Usage: limits.TotalRAMUsed}).AndZeroInTheseAZs(allAZs),
			}
		}
	}

	// Nova does not have a native API for AZ-aware usage reporting,
	// so we will obtain AZ-aware usage stats by counting up all subresources,
	// even if we don't end up showing them in the API
	allSubresources, err := p.buildInstanceSubresources(ctx, project)
	if err != nil {
		return nil, nil, fmt.Errorf("while collecting instance data: %w", err)
	}

	for _, subres := range allSubresources {
		az := subres.AZ

		if slices.Contains(p.ignoredFlavorNames, subres.FlavorName) {
			continue
		}

		// use separate instance resource if we have a matching "instances_$FLAVOR" resource
		instanceResourceName := nova.ResourceNameForFlavor(subres.FlavorName)
		isPooled := false
		if _, exists := result[instanceResourceName]; !exists {
			// otherwise used the appropriate pooled instance resource
			isPooled = true
			instanceResourceName = p.pooledResourceName(subres.HWVersion, "instances")
		}

		// count subresource towards "instances" (or separate instance resource)
		result[instanceResourceName].AddLocalizedUsage(az, 1)
		if p.WithSubresources {
			azData := result[instanceResourceName].UsageInAZ(az)
			azData.Subresources = append(azData.Subresources, subres)
		}

		// if counted towards separate instance resource, do not count towards "cores" and "ram"
		if !isPooled {
			continue
		}

		// count towards "cores" and "ram" under the appropriate pooled resource
		result[p.pooledResourceName(subres.HWVersion, "cores")].AddLocalizedUsage(az, subres.VCPUs)
		result[p.pooledResourceName(subres.HWVersion, "ram")].AddLocalizedUsage(az, subres.MemoryMiB.Value)
	}

	return result, nil, nil
}

func (p *novaPlugin) BuildServiceUsageRequest(project core.KeystoneProject, allAZs []limes.AvailabilityZone) (liquid.ServiceUsageRequest, error) {
	return liquid.ServiceUsageRequest{}, core.ErrNotALiquid
}

func (p *novaPlugin) pooledResourceName(hwVersion string, base liquid.ResourceName) liquid.ResourceName {
	// `base` is one of "cores", "instances" or "ram"
	if hwVersion == "" {
		return base
	}

	// if we saw a "quota:hw_version" extra spec on the instance's flavor, use the appropriate resource if it exists
	if p.hasPooledResource[hwVersion][base] {
		return liquid.ResourceName(fmt.Sprintf("hw_version_%s_instances", hwVersion))
	}
	return base
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *novaPlugin) SetQuota(ctx context.Context, project core.KeystoneProject, quotaReq map[liquid.ResourceName]liquid.ResourceQuotaRequest) error {
	// translate Limes resource names for separate instance quotas into Nova quota names
	novaQuotas := make(novaQuotaUpdateOpts, len(quotaReq))
	for resourceName, request := range quotaReq {
		novaQuotas[string(resourceName)] = request.Quota
	}

	return quotasets.Update(ctx, p.NovaV2, project.UUID, novaQuotas).Err
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *novaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	// unused
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *novaPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	return nil // unused
}

type novaQuotaUpdateOpts map[string]uint64

func (opts novaQuotaUpdateOpts) ToComputeQuotaUpdateMap() (map[string]any, error) {
	return map[string]any{"quota_set": opts}, nil
}
