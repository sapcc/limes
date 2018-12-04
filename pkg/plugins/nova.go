/*******************************************************************************
*
* Copyright 2017 SAP SE
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
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/limits"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/quotasets"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/flavors"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/servers"
	"github.com/gophercloud/gophercloud/openstack/imageservice/v2/images"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/limes"
)

//use a name that's unique to github.com/gophercloud/gophercloud/openstack/imageservice/v2/images
//to ensure that goimports does not mistakenly replace it by .../compute/v2/images
var _ images.ImageVisibility

type novaHypervisorTypeRule struct {
	MatchFlavorName bool
	MatchExtraSpec  string
	ValuePattern    *regexp.Regexp
	HypervisorType  string
}

type novaPlugin struct {
	cfg             limes.ServiceConfiguration
	scrapeInstances bool
	//computed state
	hypervisorTypeRules []novaHypervisorTypeRule
	resources           []limes.ResourceInfo
	//caches
	flavorInfo     map[string]novaFlavorInfo
	osTypeForImage map[string]string
}

type novaFlavorInfo struct {
	Flavor     *flavors.Flavor
	ExtraSpecs map[string]string
}

var novaDefaultResources = []limes.ResourceInfo{
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
		Name:              "server_groups",
		Unit:              limes.UnitNone,
		ExternallyManaged: true,
	},
	{
		Name:              "server_group_members",
		Unit:              limes.UnitNone,
		ExternallyManaged: true,
	},
}

var novaInstanceCountGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_instance_counts",
		Help: "Number of Nova instances per project and hypervisor type.",
	},
	[]string{"os_cluster", "domain_id", "project_id", "hypervisor"},
)

func init() {
	limes.RegisterQuotaPlugin(func(c limes.ServiceConfiguration, scrapeSubresources map[string]bool) limes.QuotaPlugin {
		return &novaPlugin{
			cfg:             c,
			scrapeInstances: scrapeSubresources["instances"],
			resources:       novaDefaultResources,
		}
	})
	prometheus.MustRegister(novaInstanceCountGauge)
}

//Init implements the limes.QuotaPlugin interface.
func (p *novaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	client, err := openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}

	//find per-flavor instance resources
	resources, err := listPerFlavorInstanceResources(client)
	if err != nil {
		return err
	}
	for _, resourceName := range resources {
		p.resources = append(p.resources, limes.ResourceInfo{
			Name:     resourceName,
			Category: "per_flavor",
			Unit:     limes.UnitNone,
		})
	}
	sort.Slice(p.resources, func(i, j int) bool {
		return p.resources[i].Name < p.resources[j].Name
	})

	//compile hypervisor type rules
	p.hypervisorTypeRules = nil
	for _, rule := range p.cfg.Compute.HypervisorTypeRules {
		rx, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return err
		}
		//the format of rule.Key is built for future extensibility, e.g. if it
		//later becomes required to match against image capabilties or flavor name
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

//ServiceInfo implements the limes.QuotaPlugin interface.
func (p *novaPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "compute",
		ProductName: "nova",
		Area:        "compute",
	}
}

//Resources implements the limes.QuotaPlugin interface.
func (p *novaPlugin) Resources() []limes.ResourceInfo {
	return p.resources
}

//Scrape implements the limes.QuotaPlugin interface.
func (p *novaPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string) (map[string]limes.ResourceData, error) {
	client, err := openstack.NewComputeV2(provider, eo)
	if err != nil {
		return nil, err
	}

	type quotaDetail struct {
		InUse uint64 `json:"in_use"`
		Limit int64  `json:"limit"`
	}

	var projectResourceData struct {
		QuotaSet struct {
			Cores              quotaDetail `json:"cores"`
			Instances          quotaDetail `json:"instances"`
			RAM                quotaDetail `json:"ram"`
			ServerGroupMembers quotaDetail `json:"server_group_members"`
			ServerGroups       quotaDetail `json:"server_groups"`
		} `json:"quota_set"`
		Limits struct {
			AbsolutePerFlavor map[string]struct {
				MaxTotalInstances  int64  `json:"maxTotalInstances"`
				TotalInstancesUsed uint64 `json:"totalInstancesUsed"`
			} `json:"absolutePerFlavor"`
		} `json:"limits"`
	}

	err = quotasets.GetDetail(client, projectUUID).ExtractInto(&projectResourceData)
	if err != nil {
		return nil, err
	}

	err = limits.Get(client, limits.GetOpts{TenantID: projectUUID}).ExtractInto(&projectResourceData)
	if err != nil {
		return nil, err
	}

	result := map[string]*limes.ResourceData{
		"cores": {
			Quota: projectResourceData.QuotaSet.Cores.Limit,
			Usage: projectResourceData.QuotaSet.Cores.InUse,
		},
		"instances": {
			Quota: projectResourceData.QuotaSet.Instances.Limit,
			Usage: projectResourceData.QuotaSet.Instances.InUse,
		},
		"ram": {
			Quota: projectResourceData.QuotaSet.RAM.Limit,
			Usage: projectResourceData.QuotaSet.RAM.InUse,
		},
		"server_groups": {
			Quota: projectResourceData.QuotaSet.ServerGroups.Limit,
			Usage: projectResourceData.QuotaSet.ServerGroups.InUse,
		},
		"server_group_members": {
			Quota: projectResourceData.QuotaSet.ServerGroupMembers.Limit,
			Usage: projectResourceData.QuotaSet.ServerGroupMembers.InUse,
		},
	}
	if projectResourceData.Limits.AbsolutePerFlavor != nil {
		for flavorName, flavorLimits := range projectResourceData.Limits.AbsolutePerFlavor {
			result["instances_"+flavorName] = &limes.ResourceData{
				Quota: flavorLimits.MaxTotalInstances,
				Usage: flavorLimits.TotalInstancesUsed,
			}
		}
	}

	if p.scrapeInstances {
		listOpts := novaServerListOpts{
			AllTenants: true,
			TenantID:   projectUUID,
		}

		countsByHypervisor := map[string]uint64{
			"vmware":  0,
			"none":    0,
			"unknown": 0,
		}

		err := servers.List(client, listOpts).EachPage(func(page pagination.Page) (bool, error) {
			instances, err := servers.ExtractServers(page)
			if err != nil {
				return false, err
			}

			for _, instance := range instances {
				subResource := map[string]interface{}{
					"id":     instance.ID,
					"name":   instance.Name,
					"status": instance.Status,
				}
				flavorID := instance.Flavor["id"].(string)
				flavorInfo := p.getFlavorInfo(client, flavorID)
				if flavor := flavorInfo.Flavor; flavor != nil {
					subResource["flavor"] = flavor.Name
					subResource["vcpu"] = flavor.VCPUs
					subResource["ram"] = limes.ValueWithUnit{
						Value: uint64(flavor.RAM),
						Unit:  limes.UnitMebibytes,
					}
					subResource["disk"] = limes.ValueWithUnit{
						Value: uint64(flavor.Disk),
						Unit:  limes.UnitGibibytes,
					}
				}

				if len(p.hypervisorTypeRules) > 0 {
					hypervisorType, err := p.getHypervisorType(client, flavorID)
					if err != nil {
						logg.Error("error while trying to find hypervisor type for flavor %s: %s", flavorID, err.Error())
					}
					subResource["hypervisor"] = hypervisorType
					countsByHypervisor[hypervisorType]++
				}

				if instance.Image == nil {
					subResource["os_type"] = "image-missing"
				} else {
					imageID, ok := instance.Image["id"].(string)
					if ok {
						osType, err := p.getOSType(provider, eo, imageID)
						if err == nil {
							subResource["os_type"] = osType
						} else {
							logg.Error("error while trying to find OS type for image %s: %s", imageID, err.Error())
						}
					} else {
						subResource["os_type"] = "image-missing"
					}
				}

				flavorName := ""
				if flavorInfo.Flavor != nil {
					flavorName = flavorInfo.Flavor.Name
				}
				resource, exists := result["instances_"+flavorName]
				if !exists {
					resource = result["instances"]
				}
				resource.Subresources = append(resource.Subresources, subResource)
			}
			return true, nil
		})
		if err != nil {
			return nil, err
		}

		//report Prometheus metrics
		if len(p.hypervisorTypeRules) > 0 {
			for typeStr, count := range countsByHypervisor {
				novaInstanceCountGauge.With(prometheus.Labels{
					"os_cluster": clusterID,
					"domain_id":  domainUUID,
					"project_id": projectUUID,
					"hypervisor": typeStr,
				}).Set(float64(count))
			}
		}
	}

	//remove references (which we needed to apply the subresources correctly)
	result2 := make(map[string]limes.ResourceData, len(result))
	for name, data := range result {
		result2[name] = *data
	}
	return result2, nil
}

//SetQuota implements the limes.QuotaPlugin interface.
func (p *novaPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	client, err := openstack.NewComputeV2(provider, eo)
	if err != nil {
		return err
	}

	return quotasets.Update(client, projectUUID, novaQuotaUpdateOpts(quotas)).Err
}

//Getting and caching flavor details and extra specs
//Changing a flavor is not supported from OpenStack, so no invalidating of the cache needed
//Access to the map is not thread safe
//
//Note that the fields of the result value can be nil, if the flavor has been deleted.
func (p *novaPlugin) getFlavorInfo(client *gophercloud.ServiceClient, flavorID string) novaFlavorInfo {
	if p.flavorInfo == nil {
		p.flavorInfo = make(map[string]novaFlavorInfo)
	}
	if info, ok := p.flavorInfo[flavorID]; ok {
		return info
	}

	var (
		result novaFlavorInfo
		err    error
	)
	result.Flavor, err = flavors.Get(client, flavorID).Extract()
	if err != nil {
		logg.Error("retrieve flavor %s: %s", flavorID, err.Error())
	}
	result.ExtraSpecs, err = getFlavorExtras(client, flavorID)
	if err != nil {
		logg.Error("retrieve flavor %s extra-specs: %s", flavorID, err.Error())
	}

	p.flavorInfo[flavorID] = result
	return result
}

func (p *novaPlugin) getHypervisorType(client *gophercloud.ServiceClient, flavorID string) (string, error) {
	flavorInfo := p.getFlavorInfo(client, flavorID)

	for _, rule := range p.hypervisorTypeRules {
		switch {
		case rule.MatchFlavorName:
			if flavorInfo.Flavor == nil { //cannot evaluate this rule
				return "flavor-deleted", nil
			}
			if rule.ValuePattern.MatchString(flavorInfo.Flavor.Name) {
				return rule.HypervisorType, nil
			}
		case rule.MatchExtraSpec != "":
			if flavorInfo.Flavor == nil { //cannot evaluate this rule
				return "flavor-deleted", nil
			}
			if rule.ValuePattern.MatchString(flavorInfo.ExtraSpecs[rule.MatchExtraSpec]) {
				return rule.HypervisorType, nil
			}
		}
	}
	return "unknown", nil
}

func (p *novaPlugin) getOSType(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, imageID string) (string, error) {
	if p.osTypeForImage == nil {
		p.osTypeForImage = make(map[string]string)
	}

	if osType, ok := p.osTypeForImage[imageID]; ok {
		return osType, nil
	}

	osType, err := p.findOSType(provider, eo, imageID)
	if err == nil {
		p.osTypeForImage[imageID] = osType
	} else {
		fmt.Printf("internal error: %#v\n", err)
	}
	return osType, err
}

func (p *novaPlugin) findOSType(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, imageID string) (string, error) {
	client, err := openstack.NewImageServiceV2(provider, eo)
	if err != nil {
		return "", err
	}
	var result struct {
		Tags         []string `json:"tags"`
		VMwareOSType string   `json:"vmware_ostype"`
	}
	err = images.Get(client, imageID).ExtractInto(&result)
	if err != nil {
		//report a dummy value if image has been deleted...
		if _, ok := err.(gophercloud.ErrDefault404); ok {
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

func makeIntPointer(value int) *int {
	return &value
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

func (opts novaQuotaUpdateOpts) ToComputeQuotaUpdateMap() (map[string]interface{}, error) {
	result := make(map[string]interface{}, len(opts))
	for key, val := range opts {
		result[key] = val
	}
	return map[string]interface{}{"quota_set": result}, nil
}
