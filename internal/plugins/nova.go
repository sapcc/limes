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
	"encoding/json"
	"fmt"
	"math/big"
	"sort"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/limits"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/quotasets"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/regexpext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/plugins/nova"
)

type novaPlugin struct {
	//configuration
	BigVMMinMemoryMiB      uint64                   `yaml:"bigvm_min_memory"`
	HypervisorTypeRules    nova.HypervisorTypeRules `yaml:"hypervisor_type_rules"`
	SeparateInstanceQuotas struct {
		FlavorNameRx  regexpext.PlainRegexp       `yaml:"flavor_name_pattern"`
		FlavorAliases nova.FlavorTranslationTable `yaml:"flavor_aliases"`
	} `yaml:"separate_instance_quotas"`
	WithSubresources bool `yaml:"with_subresources"`
	//computed state
	resources []limesresources.ResourceInfo `yaml:"-"`
	//connections
	NovaV2            *gophercloud.ServiceClient `yaml:"-"`
	OSTypeProber      *novaOSTypeProber          `yaml:"-"`
	ServerGroupProber *novaServerGroupProber     `yaml:"-"`
}

type novaSerializedMetrics struct {
	//TODO: flip the generated metrics to use the new structure, then remove the old one
	InstanceCountsByHypervisor      map[string]uint64                            `json:"instances_by_hypervisor,omitempty"`
	InstanceCountsByHypervisorAndAZ map[string]map[limes.AvailabilityZone]uint64 `json:"ic_hv_az,omitempty"`
}

var novaDefaultResources = []limesresources.ResourceInfo{
	{
		Name: "cores",
		Unit: limes.UnitNone,
	},
	{
		Name: "instances",
		Unit: limes.UnitNone,
	},
	{
		Name: "ram",
		Unit: limes.UnitMebibytes,
	},
	{
		Name: "server_groups",
		Unit: limes.UnitNone,
	},
	{
		Name: "server_group_members",
		Unit: limes.UnitNone,
	},
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &novaPlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *novaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	p.resources = novaDefaultResources

	p.NovaV2, err = openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}
	p.NovaV2.Microversion = "2.60" //to list server groups across projects and get all required server attributes
	cinderV3, err := openstack.NewBlockStorageV3(provider, eo)
	if err != nil {
		return err
	}
	glanceV2, err := openstack.NewImageServiceV2(provider, eo)
	if err != nil {
		return err
	}
	p.OSTypeProber = newNovaOSTypeProber(p.NovaV2, cinderV3, glanceV2)
	p.ServerGroupProber = newNovaServerGroupProber(p.NovaV2)

	//find per-flavor instance resources
	flavorNames, err := p.SeparateInstanceQuotas.FlavorAliases.ListFlavorsWithSeparateInstanceQuota(p.NovaV2)
	if err != nil {
		return err
	}
	for _, flavorName := range flavorNames {
		//NOTE: If `flavor_name_pattern` is empty, then FlavorNameRx will match any input.
		if p.SeparateInstanceQuotas.FlavorNameRx.MatchString(flavorName) {
			p.resources = append(p.resources, limesresources.ResourceInfo{
				Name:     p.SeparateInstanceQuotas.FlavorAliases.LimesResourceNameForFlavor(flavorName),
				Category: "per_flavor",
				Unit:     limes.UnitNone,
			})
		}
	}

	//add price class resources if requested
	if p.BigVMMinMemoryMiB != 0 {
		p.resources = append(p.resources,
			limesresources.ResourceInfo{
				Name:        "cores_regular",
				Unit:        limes.UnitNone,
				NoQuota:     true,
				ContainedIn: "cores",
			},
			limesresources.ResourceInfo{
				Name:        "cores_bigvm",
				Unit:        limes.UnitNone,
				NoQuota:     true,
				ContainedIn: "cores",
			},
			limesresources.ResourceInfo{
				Name:        "instances_regular",
				Unit:        limes.UnitNone,
				NoQuota:     true,
				ContainedIn: "instances",
			},
			limesresources.ResourceInfo{
				Name:        "instances_bigvm",
				Unit:        limes.UnitNone,
				NoQuota:     true,
				ContainedIn: "instances",
			},
			limesresources.ResourceInfo{
				Name:        "ram_regular",
				Unit:        limes.UnitMebibytes,
				NoQuota:     true,
				ContainedIn: "ram",
			},
			limesresources.ResourceInfo{
				Name:        "ram_bigvm",
				Unit:        limes.UnitMebibytes,
				NoQuota:     true,
				ContainedIn: "ram",
			},
		)
	}

	sort.Slice(p.resources, func(i, j int) bool {
		return p.resources[i].Name < p.resources[j].Name
	})

	return p.HypervisorTypeRules.Validate()
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *novaPlugin) PluginTypeID() string {
	return "compute"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *novaPlugin) ServiceInfo(serviceType string) limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        serviceType,
		ProductName: "nova",
		Area:        "compute",
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *novaPlugin) Resources() []limesresources.ResourceInfo {
	return p.resources
}

// Rates implements the core.QuotaPlugin interface.
func (p *novaPlugin) Rates() []limesrates.RateInfo {
	return nil
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *novaPlugin) ScrapeRates(project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *novaPlugin) Scrape(project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[string]core.ResourceData, serializedMetrics []byte, err error) {
	//collect quota and usage from Nova
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
		} `json:"limits"`
	}
	err = limits.Get(p.NovaV2, limits.GetOpts{TenantID: project.UUID}).ExtractInto(&limitsData)
	if err != nil {
		return nil, nil, err
	}
	absoluteLimits := limitsData.Limits.Absolute
	var totalServerGroupMembersUsed uint64
	if absoluteLimits.TotalServerGroupsUsed > 0 {
		totalServerGroupMembersUsed, err = p.ServerGroupProber.GetMemberUsageForProject(project.UUID)
		if err != nil {
			return nil, nil, err
		}
	}

	//initialize `result`
	result = map[string]core.ResourceData{
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
		if p.SeparateInstanceQuotas.FlavorNameRx.MatchString(flavorName) {
			result[p.SeparateInstanceQuotas.FlavorAliases.LimesResourceNameForFlavor(flavorName)] = core.ResourceData{
				Quota:     flavorLimits.MaxTotalInstances,
				UsageData: core.InUnknownAZUnlessEmpty(core.UsageData{Usage: flavorLimits.TotalInstancesUsed}).AndZeroInTheseAZs(allAZs),
			}
		}
	}
	if p.BigVMMinMemoryMiB != 0 {
		for _, classSuffix := range []string{"_regular", "_bigvm"} {
			for _, baseResName := range []string{"cores", "instances", "ram"} {
				result[baseResName+classSuffix] = core.ResourceData{
					UsageData: core.PerAZ[core.UsageData]{}.AndZeroInTheseAZs(allAZs),
				}
			}
		}
	}

	//Nova does not have a native API for AZ-aware usage reporting,
	//so we will obtain AZ-aware usage stats by counting up all subresources,
	//even if we don't end up showing them in the API
	allSubresources, err := p.buildInstanceSubresources(project)
	if err != nil {
		return nil, nil, fmt.Errorf("while collecting instance data: %w", err)
	}

	for _, subres := range allSubresources {
		az := subres.AZ

		//use separate instance resource if we have a matching "instances_$FLAVOR" resource
		instanceResourceName := p.SeparateInstanceQuotas.FlavorAliases.LimesResourceNameForFlavor(subres.FlavorName)
		if _, exists := result[instanceResourceName]; !exists {
			instanceResourceName = "instances"
		}

		//count subresource towards "instances" (or separate instance resource)
		result[instanceResourceName].AddLocalizedUsage(az, 1)
		if p.WithSubresources {
			azData := result[instanceResourceName].UsageInAZ(az)
			azData.Subresources = append(azData.Subresources, subres)
		}

		//if counted towards separate instance resource, do not count towards "cores" and "ram"
		if instanceResourceName != "instances" {
			continue
		}

		//count towards "cores" and "ram"
		result["cores"].AddLocalizedUsage(az, subres.VCPUs)
		result["ram"].AddLocalizedUsage(az, subres.MemoryMiB.Value)

		//count towards "bigvm" or "regular" class if requested
		if p.BigVMMinMemoryMiB != 0 {
			class := subres.Class
			result["cores_"+class].UsageInAZ(az).Usage += subres.VCPUs
			result["instances_"+class].UsageInAZ(az).Usage++
			result["ram_"+class].UsageInAZ(az).Usage += subres.MemoryMiB.Value
		}
	}

	//calculate metrics
	var metrics novaSerializedMetrics
	if len(p.HypervisorTypeRules) > 0 {
		metrics.InstanceCountsByHypervisor = map[string]uint64{"unknown": 0}
		metrics.InstanceCountsByHypervisorAndAZ = map[string]map[limes.AvailabilityZone]uint64{
			"unknown": {limes.AvailabilityZoneUnknown: 0},
		}

		for _, subres := range allSubresources {
			hvType := subres.HypervisorType
			if hvType == "" {
				continue
			}
			metrics.InstanceCountsByHypervisor[hvType]++
			if metrics.InstanceCountsByHypervisorAndAZ[hvType] == nil {
				metrics.InstanceCountsByHypervisorAndAZ[hvType] = make(map[limes.AvailabilityZone]uint64)
			}
			metrics.InstanceCountsByHypervisorAndAZ[hvType][subres.AZ]++
		}
	}

	serializedMetrics, err = json.Marshal(metrics)
	return result, serializedMetrics, err
}

// IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *novaPlugin) IsQuotaAcceptableForProject(project core.KeystoneProject, fullQuotas map[string]map[string]uint64, allServiceInfos []limes.ServiceInfo) error {
	//not required for this plugin
	return nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *novaPlugin) SetQuota(project core.KeystoneProject, quotas map[string]uint64) error {
	//translate Limes resource names for separate instance quotas into Nova quota names
	novaQuotas := make(novaQuotaUpdateOpts, len(quotas))
	for resourceName, quota := range quotas {
		novaQuotaName := p.SeparateInstanceQuotas.FlavorAliases.NovaQuotaNameForLimesResourceName(resourceName)
		if novaQuotaName == "" {
			//not a separate instance quota - leave as-is
			novaQuotas[resourceName] = quota
		} else {
			novaQuotas[novaQuotaName] = quota
		}
	}

	return quotasets.Update(p.NovaV2, project.UUID, novaQuotas).Err
}

var novaInstanceCountGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_instance_counts",
		Help: "Number of Nova instances per project and hypervisor type.",
	},
	[]string{"domain_id", "project_id", "hypervisor"},
)

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *novaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	novaInstanceCountGauge.Describe(ch)
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *novaPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	if len(serializedMetrics) == 0 {
		return nil
	}
	var metrics novaSerializedMetrics
	err := json.Unmarshal(serializedMetrics, &metrics)
	if err != nil {
		return err
	}

	descCh := make(chan *prometheus.Desc, 1)
	novaInstanceCountGauge.Describe(descCh)
	novaInstanceCountDesc := <-descCh

	for hypervisorName, instanceCount := range metrics.InstanceCountsByHypervisor {
		ch <- prometheus.MustNewConstMetric(
			novaInstanceCountDesc,
			prometheus.GaugeValue, float64(instanceCount),
			project.Domain.UUID, project.UUID, hypervisorName,
		)
	}
	return nil
}

type novaQuotaUpdateOpts map[string]uint64

func (opts novaQuotaUpdateOpts) ToComputeQuotaUpdateMap() (map[string]any, error) {
	return map[string]any{"quota_set": opts}, nil
}
