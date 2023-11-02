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
	"math/big"
	"sort"
	"strconv"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/availabilityzones"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/limits"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/quotasets"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/servergroups"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/openstack/imageservice/v2/images"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/regexpext"

	"github.com/sapcc/limes/internal/core"
)

// use a name that's unique to github.com/gophercloud/gophercloud/openstack/imageservice/v2/images
// to ensure that goimports does not mistakenly replace it by .../compute/v2/images
var _ images.ImageVisibility

type novaPlugin struct {
	//configuration
	BigVMMinMemoryMiB      uint64                  `yaml:"bigvm_min_memory"`
	HypervisorTypeRules    novaHypervisorTypeRules `yaml:"hypervisor_type_rules"`
	SeparateInstanceQuotas struct {
		FlavorNameRx  regexpext.PlainRegexp `yaml:"flavor_name_pattern"`
		FlavorAliases map[string][]string   `yaml:"flavor_aliases"`
	} `yaml:"separate_instance_quotas"`
	scrapeInstances bool `yaml:"-"`
	//computed state
	resources []limesresources.ResourceInfo `yaml:"-"`
	ftt       novaFlavorTranslationTable    `yaml:"-"`
	//caches
	serverGroups struct {
		lastScrapeTime *time.Time
		members        map[string]uint64 // per project
	} `yaml:"-"`
	//connections
	NovaV2       *gophercloud.ServiceClient `yaml:"-"`
	OSTypeProber *novaOSTypeProber          `yaml:"-"`
}

type novaSerializedMetrics struct {
	InstanceCountsByHypervisor map[string]uint64 `json:"instances_by_hypervisor,omitempty"`
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
func (p *novaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubresources map[string]bool) (err error) {
	p.resources = novaDefaultResources
	p.scrapeInstances = scrapeSubresources["instances"]
	p.ftt = newNovaFlavorTranslationTable(p.SeparateInstanceQuotas.FlavorAliases)

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

	//find per-flavor instance resources
	flavorNames, err := p.ftt.ListFlavorsWithSeparateInstanceQuota(p.NovaV2)
	if err != nil {
		return err
	}
	for _, flavorName := range flavorNames {
		//NOTE: If `flavor_name_pattern` is empty, then FlavorNameRx will match any input.
		if p.SeparateInstanceQuotas.FlavorNameRx.MatchString(flavorName) {
			p.resources = append(p.resources, limesresources.ResourceInfo{
				Name:     p.ftt.LimesResourceNameForFlavor(flavorName),
				Category: "per_flavor",
				Unit:     limes.UnitNone,
			})
		}
	}

	//add price class resources if requested
	if p.scrapeInstances && p.BigVMMinMemoryMiB != 0 {
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

	//validate hypervisor type rules
	for _, rule := range p.HypervisorTypeRules {
		err = rule.Validate()
		if err != nil {
			return err
		}
	}

	return nil
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
func (p *novaPlugin) Scrape(project core.KeystoneProject) (result map[string]core.ResourceData, serializedMetrics []byte, err error) {
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

	var totalServerGroupMembersUsed uint64
	if limitsData.Limits.Absolute.TotalServerGroupsUsed > 0 {
		err := p.getServerGroups()
		if err != nil {
			return nil, nil, err
		}

		if v, ok := p.serverGroups.members[project.UUID]; ok {
			totalServerGroupMembersUsed = v
		}
	}

	resultPtr := map[string]*core.ResourceData{
		"cores": {
			Quota: limitsData.Limits.Absolute.MaxTotalCores,
			UsageData: core.InAnyAZ(core.UsageData{
				Usage: limitsData.Limits.Absolute.TotalCoresUsed,
			}),
		},
		"instances": {
			Quota: limitsData.Limits.Absolute.MaxTotalInstances,
			UsageData: core.InAnyAZ(core.UsageData{
				Usage: limitsData.Limits.Absolute.TotalInstancesUsed,
			}),
		},
		"ram": {
			Quota: limitsData.Limits.Absolute.MaxTotalRAMSize,
			UsageData: core.InAnyAZ(core.UsageData{
				Usage: limitsData.Limits.Absolute.TotalRAMUsed,
			}),
		},
		"server_groups": {
			Quota: limitsData.Limits.Absolute.MaxServerGroups,
			UsageData: core.InAnyAZ(core.UsageData{
				Usage: limitsData.Limits.Absolute.TotalServerGroupsUsed,
			}),
		},
		"server_group_members": {
			Quota: limitsData.Limits.Absolute.MaxServerGroupMembers,
			UsageData: core.InAnyAZ(core.UsageData{
				Usage: totalServerGroupMembersUsed,
			}),
		},
	}

	if limitsData.Limits.AbsolutePerFlavor != nil {
		for flavorName, flavorLimits := range limitsData.Limits.AbsolutePerFlavor {
			//NOTE: If `flavor_name_pattern` is empty, then FlavorNameRx will match any input.
			if p.SeparateInstanceQuotas.FlavorNameRx.MatchString(flavorName) {
				resultPtr[p.ftt.LimesResourceNameForFlavor(flavorName)] = &core.ResourceData{
					Quota: flavorLimits.MaxTotalInstances,
					UsageData: core.InAnyAZ(core.UsageData{
						Usage: flavorLimits.TotalInstancesUsed,
					}),
				}
			}
		}
	}

	//the Queens branch of sapcc/nova sometimes does not report zero quotas,
	//so make sure that all known resources are reflected
	//
	//(this also ensures that we have the {cores|ram}_{regular|bigvm} quotas for below)
	for _, res := range p.resources {
		if _, exists := resultPtr[res.Name]; !exists {
			resultPtr[res.Name] = &core.ResourceData{
				Quota:     0,
				UsageData: core.InAnyAZ(core.UsageData{}),
			}
		}
	}

	var metrics novaSerializedMetrics

	if p.scrapeInstances {
		listOpts := novaServerListOpts{
			AllTenants: true,
			TenantID:   project.UUID,
		}

		metrics.InstanceCountsByHypervisor = map[string]uint64{
			"vmware":  0,
			"none":    0,
			"unknown": 0,
		}

		err := servers.List(p.NovaV2, listOpts).EachPage(func(page pagination.Page) (bool, error) {
			var instances []struct {
				servers.Server
				availabilityzones.ServerAvailabilityZoneExt
			}
			err := servers.ExtractServersInto(page, &instances)
			if err != nil {
				return false, err
			}

			for _, instance := range instances {
				subResource := map[string]any{
					"id":                instance.ID,
					"name":              instance.Name,
					"status":            instance.Status,
					"availability_zone": instance.AvailabilityZone,
					"metadata":          instance.Metadata,
					"tags":              derefSlicePtrOrEmpty(instance.Tags),
				}

				var flavorName string
				flavor, err := unpackFlavorData(instance.Flavor)
				if err != nil {
					logg.Error("error while trying to parse flavor data for instance %s: %s", instance.ID, err.Error())
				} else {
					flavorName = flavor.OriginalName
					subResource["flavor"] = flavor.OriginalName
					subResource["vcpu"] = flavor.VCPUs
					subResource["ram"] = limes.ValueWithUnit{
						Value: flavor.MemoryMiB,
						Unit:  limes.UnitMebibytes,
					}
					subResource["disk"] = limes.ValueWithUnit{
						Value: flavor.DiskGiB,
						Unit:  limes.UnitGibibytes,
					}

					if videoRAMStr, exists := flavor.ExtraSpecs["hw_video:ram_max_mb"]; exists {
						videoRAMVal, err := strconv.ParseUint(videoRAMStr, 10, 64)
						if err == nil {
							subResource["video_ram"] = limes.ValueWithUnit{
								Value: videoRAMVal,
								Unit:  limes.UnitMebibytes,
							}
						}
					}

					if len(p.HypervisorTypeRules) > 0 {
						hypervisorType := p.HypervisorTypeRules.Evaluate(flavor)
						subResource["hypervisor"] = hypervisorType
						metrics.InstanceCountsByHypervisor[hypervisorType]++
					}

					if p.BigVMMinMemoryMiB > 0 {
						class := "regular"
						if flavor.MemoryMiB >= p.BigVMMinMemoryMiB {
							class = "bigvm"
						}
						subResource["class"] = class

						//do not count baremetal instances into `{cores,instances,ram}_{bigvm,regular}`
						if _, exists := resultPtr[p.ftt.LimesResourceNameForFlavor(flavorName)]; !exists {
							resultPtr["cores_"+class].UsageData[limes.AvailabilityZoneAny].Usage += flavor.VCPUs
							resultPtr["instances_"+class].UsageData[limes.AvailabilityZoneAny].Usage++
							resultPtr["ram_"+class].UsageData[limes.AvailabilityZoneAny].Usage += flavor.MemoryMiB
						}
					}
				}

				if instance.Image == nil {
					subResource["os_type"] = p.OSTypeProber.GetFromBootVolume(instance.ID)
				} else {
					subResource["os_type"] = p.OSTypeProber.GetFromImage(instance.Image["id"])
				}

				resource, exists := resultPtr[p.ftt.LimesResourceNameForFlavor(flavorName)]
				if !exists {
					resource = resultPtr["instances"]
				}
				resource.UsageData[limes.AvailabilityZoneAny].Subresources = append(resource.UsageData[limes.AvailabilityZoneAny].Subresources, subResource)
			}
			return true, nil
		})
		if err != nil {
			return nil, nil, err
		}
	}

	//remove references (which we needed to apply the subresources correctly)
	result2 := make(map[string]core.ResourceData, len(resultPtr))
	for name, data := range resultPtr {
		result2[name] = *data
	}
	serializedMetrics, err = json.Marshal(metrics)
	return result2, serializedMetrics, err
}

func derefSlicePtrOrEmpty(val *[]string) []string {
	if val == nil {
		return nil
	}
	return *val
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
		novaQuotaName := p.ftt.NovaQuotaNameForLimesResourceName(resourceName)
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

// Information about a flavor, as it appears in GET /servers/:id in the "flavor"
// key with newer Nova microversions.
type novaFlavorInfo struct {
	DiskGiB      uint64            `json:"disk"`
	EphemeralGiB uint64            `json:"ephemeral"`
	ExtraSpecs   map[string]string `json:"extra_specs"`
	OriginalName string            `json:"original_name"`
	MemoryMiB    uint64            `json:"ram"`
	SwapMiB      uint64            `json:"swap"`
	VCPUs        uint64            `json:"vcpus"`
}

func unpackFlavorData(input map[string]any) (novaFlavorInfo, error) {
	buf, err := json.Marshal(input)
	if err != nil {
		return novaFlavorInfo{}, err
	}
	var result novaFlavorInfo
	err = json.Unmarshal(buf, &result)
	return result, err
}

type novaServerListOpts struct {
	AllTenants bool   `q:"all_tenants"`
	TenantID   string `q:"tenant_id"`
}

func (opts novaServerListOpts) ToServerListQuery() (string, error) {
	q, err := gophercloud.BuildQueryString(opts)
	return q.String(), err
}

type novaQuotaUpdateOpts map[string]uint64

func (opts novaQuotaUpdateOpts) ToComputeQuotaUpdateMap() (map[string]any, error) {
	return map[string]any{"quota_set": opts}, nil
}

func (p *novaPlugin) getServerGroups() error {
	if p.serverGroups.lastScrapeTime != nil {
		if time.Since(*p.serverGroups.lastScrapeTime) < 10*time.Minute {
			return nil // no need to refresh cache
		}
	}

	//When paginating through the list of server groups, perform steps slightly
	//smaller than the actual page size, in order to correctly detect insertions
	//and deletions that may cause list entries to shift around while we iterate
	//over them.
	const pageSize int = 500
	stepSize := pageSize * 9 / 10
	var currentOffset int
	serverGroupSeen := make(map[string]bool)
	membersPerProject := make(map[string]uint64)
	for {
		groups, err := p.getServerGroupsPage(pageSize, currentOffset)
		if err != nil {
			return err
		}
		for _, sg := range groups {
			if !serverGroupSeen[sg.ID] {
				membersPerProject[sg.ProjectID] += uint64(len(sg.Members))
				serverGroupSeen[sg.ID] = true
			}
		}

		//abort after the last page
		if len(groups) < pageSize {
			break
		}
		currentOffset += stepSize
	}

	p.serverGroups.members = membersPerProject
	t := time.Now()
	p.serverGroups.lastScrapeTime = &t

	return nil
}

func (p *novaPlugin) getServerGroupsPage(limit, offset int) ([]servergroups.ServerGroup, error) {
	allPages, err := servergroups.List(p.NovaV2, servergroups.ListOpts{AllProjects: true, Limit: limit, Offset: offset}).AllPages()
	if err != nil {
		return nil, err
	}
	allServerGroups, err := servergroups.ExtractServerGroups(allPages)
	if err != nil {
		return nil, err
	}
	return allServerGroups, nil
}
