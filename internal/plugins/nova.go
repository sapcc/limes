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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/availabilityzones"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/limits"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/quotasets"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/servergroups"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/volumeattach"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/openstack/imageservice/v2/images"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/regexpext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/util"
)

// use a name that's unique to github.com/gophercloud/gophercloud/openstack/imageservice/v2/images
// to ensure that goimports does not mistakenly replace it by .../compute/v2/images
var _ images.ImageVisibility

type novaHypervisorTypeRule struct {
	MatchFlavorName bool
	MatchExtraSpec  string
	ValuePattern    *regexp.Regexp
	HypervisorType  string
}

type novaPlugin struct {
	//configuration
	BigVMMinMemoryMiB   uint64 `yaml:"bigvm_min_memory"`
	HypervisorTypeRules []struct {
		Key     string `yaml:"match"`
		Pattern string `yaml:"pattern"`
		Type    string `yaml:"type"`
	} `yaml:"hypervisor_type_rules"`
	SeparateInstanceQuotas struct {
		FlavorNameRx  regexpext.PlainRegexp `yaml:"flavor_name_pattern"`
		FlavorAliases map[string][]string   `yaml:"flavor_aliases"`
	} `yaml:"separate_instance_quotas"`
	scrapeInstances bool `yaml:"-"`
	//computed state
	hypervisorTypeRules []novaHypervisorTypeRule      `yaml:"-"`
	resources           []limesresources.ResourceInfo `yaml:"-"`
	ftt                 novaFlavorTranslationTable    `yaml:"-"`
	//caches
	osTypeForImage    map[string]string `yaml:"-"`
	osTypeForInstance map[string]string `yaml:"-"`
	serverGroups      struct {
		lastScrapeTime *time.Time
		members        map[string]uint64 // per project
	} `yaml:"-"`
	//connections
	NovaV2   *gophercloud.ServiceClient `yaml:"-"`
	CinderV3 *gophercloud.ServiceClient `yaml:"-"`
	GlanceV2 *gophercloud.ServiceClient `yaml:"-"`
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
	p.osTypeForImage = make(map[string]string)
	p.osTypeForInstance = make(map[string]string)
	p.scrapeInstances = scrapeSubresources["instances"]
	p.ftt = newNovaFlavorTranslationTable(p.SeparateInstanceQuotas.FlavorAliases)

	p.NovaV2, err = openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}
	p.NovaV2.Microversion = "2.60" //to list server groups across projects and get all required server attributes
	p.CinderV3, err = openstack.NewBlockStorageV3(provider, eo)
	if err != nil {
		return err
	}
	p.GlanceV2, err = openstack.NewImageServiceV2(provider, eo)
	if err != nil {
		return err
	}

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

	//compile hypervisor type rules
	p.hypervisorTypeRules = nil
	for _, rule := range p.HypervisorTypeRules {
		rx, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return err
		}
		//the format of rule.Key is built for future extensibility, e.g. if it
		//later becomes required to match against image capabilities
		switch {
		case rule.Key == "flavor-name":
			p.hypervisorTypeRules = append(p.hypervisorTypeRules, novaHypervisorTypeRule{
				MatchFlavorName: true,
				ValuePattern:    rx,
				HypervisorType:  rule.Type,
			})
		case strings.HasPrefix(rule.Key, "extra-spec:"):
			p.hypervisorTypeRules = append(p.hypervisorTypeRules, novaHypervisorTypeRule{
				MatchExtraSpec: strings.TrimPrefix(rule.Key, "extra-spec:"),
				ValuePattern:   rx,
				HypervisorType: rule.Type,
			})
		default:
			return fmt.Errorf(
				"key %q for hypervisor type rule must be \"flavor-name\" or start with \"extra-spec:\"",
				rule.Key,
			)
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

					if len(p.hypervisorTypeRules) > 0 {
						hypervisorType := p.getHypervisorType(flavor)
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
					//instance built from bootable volume -> look at bootable volume to find OS type
					osType, err := p.getOSTypeFromBootVolume(instance.ID)
					if err == nil {
						subResource["os_type"] = osType
					} else {
						logg.Error("error while trying to find OS type for instance %s from boot volume: %s", instance.ID, util.UnpackError(err).Error())
						subResource["os_type"] = "rootdisk-inspect-error"
					}
				} else {
					//instance built from image -> look at image to find OS type
					imageID, ok := instance.Image["id"].(string)
					if ok {
						osType, err := p.getOSTypeForImage(imageID)
						if err == nil {
							subResource["os_type"] = osType
						} else {
							logg.Error("error while trying to find OS type for image %s: %s", imageID, util.UnpackError(err).Error())
							subResource["os_type"] = "image-inspect-error"
						}
					} else {
						subResource["os_type"] = "image-missing"
					}
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

func (p *novaPlugin) getHypervisorType(flavor novaFlavorInfo) string {
	for _, rule := range p.hypervisorTypeRules {
		switch {
		case rule.MatchFlavorName:
			if rule.ValuePattern.MatchString(flavor.OriginalName) {
				return rule.HypervisorType
			}
		case rule.MatchExtraSpec != "":
			if rule.ValuePattern.MatchString(flavor.ExtraSpecs[rule.MatchExtraSpec]) {
				return rule.HypervisorType
			}
		}
	}
	return "unknown"
}

func (p *novaPlugin) getOSTypeFromBootVolume(instanceID string) (string, error) {
	cachedResult, ok := p.osTypeForInstance[instanceID]
	if ok {
		return cachedResult, nil
	}

	//list volume attachments
	page, err := volumeattach.List(p.NovaV2, instanceID).AllPages()
	if err != nil {
		return "", err
	}
	attachments, err := volumeattach.ExtractVolumeAttachments(page)
	if err != nil {
		return "", err
	}

	//find root volume among attachments
	var rootVolumeID string
	for _, attachment := range attachments {
		if attachment.Device == "/dev/sda" || attachment.Device == "/dev/vda" {
			rootVolumeID = attachment.VolumeID
			break
		}
	}
	if rootVolumeID == "" {
		return "rootdisk-missing", nil
	}

	//check volume metadata
	var volume struct {
		ImageMetadata struct {
			VMwareOSType string `json:"vmware_ostype"`
		} `json:"volume_image_metadata"`
	}
	err = volumes.Get(p.CinderV3, rootVolumeID).ExtractInto(&volume)
	if err != nil {
		return "", err
	}

	if isValidVMwareOSType[volume.ImageMetadata.VMwareOSType] {
		p.osTypeForInstance[instanceID] = volume.ImageMetadata.VMwareOSType
		return volume.ImageMetadata.VMwareOSType, nil
	}
	return "unknown", nil
}

func (p *novaPlugin) getOSTypeForImage(imageID string) (string, error) {
	if osType, ok := p.osTypeForImage[imageID]; ok {
		return osType, nil
	}

	osType, err := p.findOSTypeForImage(imageID)
	if err == nil {
		p.osTypeForImage[imageID] = osType
	} else {
		logg.Error("internal: %#v\n", err)
	}
	return osType, err
}

func (p *novaPlugin) findOSTypeForImage(imageID string) (string, error) {
	var result struct {
		Tags         []string `json:"tags"`
		VMwareOSType string   `json:"vmware_ostype"`
	}
	err := images.Get(p.GlanceV2, imageID).ExtractInto(&result)
	if err != nil {
		//report a dummy value if image has been deleted...
		if errext.IsOfType[gophercloud.ErrDefault404](err) {
			return "image-deleted", nil
		}
		//otherwise, try to GET image again during next scrape
		return "", err
	}

	//prefer vmware_ostype attribute since this is validated by Nova upon booting the VM
	if isValidVMwareOSType[result.VMwareOSType] {
		return result.VMwareOSType, nil
	}
	//look for a tag like "ostype:rhel7" or "ostype:windows8Server64"
	osType := ""
	for _, tag := range result.Tags {
		if strings.HasPrefix(tag, "ostype:") {
			if osType == "" {
				osType = strings.TrimPrefix(tag, "ostype:")
			} else {
				//multiple such tags -> wtf
				osType = ""
				break
			}
		}
	}

	//report "unknown" as a last resort
	if osType == "" {
		osType = "unknown"
	}
	return osType, nil
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
