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
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

//use a name that's unique to github.com/gophercloud/gophercloud/openstack/imageservice/v2/images
//to ensure that goimports does not mistakenly replace it by .../compute/v2/images
var _ images.ImageVisibility

type novaHypervisorTypeRule struct {
	ExtraSpecName  string
	ValuePattern   *regexp.Regexp
	HypervisorType string
}

type novaPlugin struct {
	cfg             limes.ServiceConfiguration
	scrapeInstances bool
	//computed state
	hypervisorTypeRules []novaHypervisorTypeRule
	resources           []limes.ResourceInfo
	//caches
	flavors        map[string]*flavors.Flavor
	extraSpecs     map[string]map[string]string
	osTypeForImage map[string]string
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
func (p *novaPlugin) Init(provider *gophercloud.ProviderClient) error {
	client, err := p.Client(provider)
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
		case strings.HasPrefix(rule.Key, "extra-spec:"):
			p.hypervisorTypeRules = append(p.hypervisorTypeRules, novaHypervisorTypeRule{
				ExtraSpecName:  strings.TrimPrefix(rule.Key, "extra-spec:"),
				ValuePattern:   rx,
				HypervisorType: rule.Type,
			})
		default:
			return fmt.Errorf("key %q for hypervisor type rule must start with \"extra-spec:\"", rule.Key)
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

func (p *novaPlugin) Client(provider *gophercloud.ProviderClient) (*gophercloud.ServiceClient, error) {
	return openstack.NewComputeV2(provider,
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
}

func (p *novaPlugin) GlanceClient(provider *gophercloud.ProviderClient) (*gophercloud.ServiceClient, error) {
	return openstack.NewImageServiceV2(provider,
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
}

//Scrape implements the limes.QuotaPlugin interface.
func (p *novaPlugin) Scrape(provider *gophercloud.ProviderClient, clusterID, domainUUID, projectUUID string) (map[string]limes.ResourceData, error) {
	client, err := p.Client(provider)
	if err != nil {
		return nil, err
	}

	var limitsData struct {
		Limits struct {
			Absolute struct {
				MaxTotalCores      int64  `json:"maxTotalCores"`
				MaxTotalInstances  int64  `json:"maxTotalInstances"`
				MaxTotalRAMSize    int64  `json:"maxTotalRAMSize"`
				TotalCoresUsed     uint64 `json:"totalCoresUsed"`
				TotalInstancesUsed uint64 `json:"totalInstancesUsed"`
				TotalRAMUsed       uint64 `json:"totalRAMUsed"`
			} `json:"absolute"`
			AbsolutePerFlavor map[string]struct {
				MaxTotalInstances  int64  `json:"maxTotalInstances"`
				TotalInstancesUsed uint64 `json:"totalInstancesUsed"`
			} `json:"absolutePerFlavor"`
		} `json:"limits"`
	}
	err = limits.Get(client, limits.GetOpts{TenantID: projectUUID}).ExtractInto(&limitsData)
	if err != nil {
		return nil, err
	}

	result := map[string]*limes.ResourceData{
		"cores": {
			Quota: limitsData.Limits.Absolute.MaxTotalCores,
			Usage: limitsData.Limits.Absolute.TotalCoresUsed,
		},
		"instances": {
			Quota: limitsData.Limits.Absolute.MaxTotalInstances,
			Usage: limitsData.Limits.Absolute.TotalInstancesUsed,
		},
		"ram": {
			Quota: limitsData.Limits.Absolute.MaxTotalRAMSize,
			Usage: limitsData.Limits.Absolute.TotalRAMUsed,
		},
	}
	if limitsData.Limits.AbsolutePerFlavor != nil {
		for flavorName, flavorLimits := range limitsData.Limits.AbsolutePerFlavor {
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
				flavor, _, err := p.getFlavor(client, flavorID)
				if err == nil {
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
				} else {
					util.LogError("error while trying to retrieve data for flavor %s: %s", flavorID, err.Error())
				}

				hypervisorType, err := p.getHypervisorType(client, flavorID)
				if err != nil {
					util.LogError("error while trying to find hypervisor type for flavor %s: %s", flavorID, err.Error())
				}
				subResource["hypervisor"] = hypervisorType
				countsByHypervisor[hypervisorType]++

				imageID, ok := instance.Image["id"].(string)
				if ok {
					osType, err := p.getOSType(provider, imageID)
					if err == nil {
						subResource["os_type"] = osType
					} else {
						util.LogError("error while trying to find OS type for image %s: %s", imageID, err.Error())
					}
				} else {
					subResource["os_type"] = "image-missing"
				}

				flavorName := ""
				if flavor != nil {
					flavorName = flavor.Name
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
		for typeStr, count := range countsByHypervisor {
			novaInstanceCountGauge.With(prometheus.Labels{
				"os_cluster": clusterID,
				"domain_id":  domainUUID,
				"project_id": projectUUID,
				"hypervisor": typeStr,
			}).Set(float64(count))
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
func (p *novaPlugin) SetQuota(provider *gophercloud.ProviderClient, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	client, err := p.Client(provider)
	if err != nil {
		return err
	}

	return quotasets.Update(client, projectUUID, novaQuotaUpdateOpts(quotas)).Err
}

//Getting and caching flavor details and extra specs
//Changing a flavor is not supported from OpenStack, so no invalidating of the cache needed
//Access to the map is not thread safe
func (p *novaPlugin) getFlavor(client *gophercloud.ServiceClient, flavorID string) (*flavors.Flavor, map[string]string, error) {
	if p.flavors == nil {
		p.flavors = make(map[string]*flavors.Flavor)
	}
	if p.extraSpecs == nil {
		p.extraSpecs = make(map[string]map[string]string)
	}

	if _, ok := p.flavors[flavorID]; !ok {
		flavor, err := flavors.Get(client, flavorID).Extract()
		if err != nil {
			return nil, nil, err
		}
		p.flavors[flavorID] = flavor
	}

	if _, ok := p.extraSpecs[flavorID]; !ok {
		specs, err := getFlavorExtras(client, flavorID)
		if err != nil {
			return nil, nil, err
		}
		p.extraSpecs[flavorID] = specs
	}

	return p.flavors[flavorID], p.extraSpecs[flavorID], nil
}

func (p *novaPlugin) getHypervisorType(client *gophercloud.ServiceClient, flavorID string) (string, error) {
	flavor, extraSpecs, err := p.getFlavor(client, flavorID)
	if err != nil {
		return "unknown", err
	}

	for _, rule := range p.hypervisorTypeRules {
		if flavor == nil {
			return "flavor-deleted", nil
		}
		if rule.ValuePattern.MatchString(extraSpecs[rule.ExtraSpecName]) {
			return rule.HypervisorType, nil
		}
	}
	return "unknown", nil
}

func (p *novaPlugin) getOSType(provider *gophercloud.ProviderClient, imageID string) (string, error) {
	if p.osTypeForImage == nil {
		p.osTypeForImage = make(map[string]string)
	}

	if osType, ok := p.osTypeForImage[imageID]; ok {
		return osType, nil
	}

	osType, err := p.findOSType(provider, imageID)
	if err == nil {
		p.osTypeForImage[imageID] = osType
	} else {
		fmt.Printf("internal error: %#v\n", err)
	}
	return osType, err
}

func (p *novaPlugin) findOSType(provider *gophercloud.ProviderClient, imageID string) (string, error) {
	client, err := p.GlanceClient(provider)
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
