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
	"math/big"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/common/extensions"
	octavia_quotas "github.com/gophercloud/gophercloud/openstack/loadbalancer/v2/quotas"
	neutron_quotas "github.com/gophercloud/gophercloud/openstack/networking/v2/extensions/quotas"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
)

type neutronPlugin struct {
	//computed state
	resources    []limesresources.ResourceInfo `yaml:"-"`
	hasExtension map[string]bool               `yaml:"-"`
	hasOctavia   bool                          `yaml:"-"`
	//connections
	NeutronV2 *gophercloud.ServiceClient `yaml:"-"`
	OctaviaV2 *gophercloud.ServiceClient `yaml:"-"`
}

var neutronResources = []limesresources.ResourceInfo{
	////////// SDN resources
	{
		Name:     "floating_ips",
		Unit:     limes.UnitNone,
		Category: "networking",
	},
	{
		Name:     "networks",
		Unit:     limes.UnitNone,
		Category: "networking",
	},
	{
		Name:     "ports",
		Unit:     limes.UnitNone,
		Category: "networking",
	},
	{
		Name:     "rbac_policies",
		Unit:     limes.UnitNone,
		Category: "networking",
	},
	{
		Name:     "routers",
		Unit:     limes.UnitNone,
		Category: "networking",
	},
	{
		Name:     "security_group_rules",
		Unit:     limes.UnitNone,
		Category: "networking",
		//for "default" security group
		AutoApproveInitialQuota: 4,
	},
	{
		Name:     "security_groups",
		Unit:     limes.UnitNone,
		Category: "networking",
		//for "default" security group
		AutoApproveInitialQuota: 1,
	},
	{
		Name:     "subnet_pools",
		Unit:     limes.UnitNone,
		Category: "networking",
	},
	{
		Name:     "subnets",
		Unit:     limes.UnitNone,
		Category: "networking",
	},
	////////// network resources belonging to optional extensions
	{
		Name:     "bgpvpns",
		Unit:     limes.UnitNone,
		Category: "networking",
	},
	{
		Name:     "trunks",
		Unit:     limes.UnitNone,
		Category: "networking",
	},
	////////// LBaaS resources
	{
		Name:     "healthmonitors",
		Unit:     limes.UnitNone,
		Category: "loadbalancing",
	},
	{
		Name:     "l7policies",
		Unit:     limes.UnitNone,
		Category: "loadbalancing",
	},
	{
		Name:     "listeners",
		Unit:     limes.UnitNone,
		Category: "loadbalancing",
	},
	{
		Name:     "loadbalancers",
		Unit:     limes.UnitNone,
		Category: "loadbalancing",
	},
	{
		Name:     "pools",
		Unit:     limes.UnitNone,
		Category: "loadbalancing",
	},
	{
		Name:     "pool_members",
		Unit:     limes.UnitNone,
		Category: "loadbalancing",
	},
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &neutronPlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubresources map[string]bool) (err error) {
	p.NeutronV2, err = openstack.NewNetworkV2(provider, eo)
	if err != nil {
		return err
	}

	// Check required Neutron extensions
	p.hasExtension = map[string]bool{}
	for _, resource := range neutronResourceMeta {
		if resource.Extension == "" {
			continue
		}
		_, err := extensions.Get(p.NeutronV2, resource.Extension).Extract()
		//nolint:errorlint // a type cast is clearer than errors.As()
		switch err.(type) {
		case gophercloud.ErrDefault404:
			p.hasExtension[resource.Extension] = false
		case nil:
			p.hasExtension[resource.Extension] = true
		default:
			return fmt.Errorf("cannot check for %q support in Neutron: %w", resource.Extension, err)
		}
		logg.Info("Neutron extension %s is enabled = %t", resource.Extension, p.hasExtension[resource.Extension])
	}

	// Octavia supported?
	p.OctaviaV2, err = openstack.NewLoadBalancerV2(provider, eo)
	//nolint:errorlint // a type cast is clearer than errors.As()
	switch err.(type) {
	case *gophercloud.ErrEndpointNotFound:
		p.hasOctavia = false
	case nil:
		p.hasOctavia = true
	default:
		return err
	}

	//filter resource list to reflect supported extensions and services
	hasNeutronResource := make(map[string]bool)
	for _, resource := range neutronResourceMeta {
		hasNeutronResource[resource.LimesName] = resource.Extension == "" || p.hasExtension[resource.Extension]
	}
	p.resources = nil
	for _, res := range neutronResources {
		var hasResource bool
		if res.Category == "loadbalancing" {
			hasResource = p.hasOctavia
		} else {
			hasResource = hasNeutronResource[res.Name]
		}
		if hasResource {
			p.resources = append(p.resources, res)
		}
	}

	return nil
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *neutronPlugin) PluginTypeID() string {
	return "network"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *neutronPlugin) ServiceInfo(serviceType string) limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        serviceType,
		ProductName: "neutron",
		Area:        "network",
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Resources() []limesresources.ResourceInfo {
	return p.resources
}

// Rates implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Rates() []limesrates.RateInfo {
	return nil
}

type neutronResourceMetadata struct {
	LimesName   string
	NeutronName string
	Extension   string
}

var neutronResourceMeta = []neutronResourceMetadata{
	{
		LimesName:   "networks",
		NeutronName: "network",
	},
	{
		LimesName:   "subnets",
		NeutronName: "subnet",
	},
	{
		LimesName:   "subnet_pools",
		NeutronName: "subnetpool",
	},
	{
		LimesName:   "floating_ips",
		NeutronName: "floatingip",
	},
	{
		LimesName:   "routers",
		NeutronName: "router",
	},
	{
		LimesName:   "ports",
		NeutronName: "port",
	},
	{
		LimesName:   "security_groups",
		NeutronName: "security_group",
	},
	{
		LimesName:   "security_group_rules",
		NeutronName: "security_group_rule",
	},
	{
		LimesName:   "rbac_policies",
		NeutronName: "rbac_policy",
	},
	{
		LimesName:   "bgpvpns",
		NeutronName: "bgpvpn",
		Extension:   "bgpvpn",
	},
	{
		LimesName:   "trunks",
		NeutronName: "trunk",
		Extension:   "trunk",
	},
}

type octaviaResourceMetadata struct {
	LimesName         string
	OctaviaName       string
	LegacyOctaviaName string
}

var octaviaResourceMeta = []octaviaResourceMetadata{
	{
		LimesName:         "loadbalancers",
		OctaviaName:       "loadbalancer",
		LegacyOctaviaName: "load_balancer",
	},
	{
		LimesName:   "listeners",
		OctaviaName: "listener",
	},
	{
		LimesName:   "pools",
		OctaviaName: "pool",
	},
	{
		LimesName:         "healthmonitors",
		OctaviaName:       "healthmonitor",
		LegacyOctaviaName: "health_monitor",
	},
	{
		LimesName:   "l7policies",
		OctaviaName: "l7policy",
	},
	{
		LimesName:   "pool_members",
		OctaviaName: "member",
	},
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *neutronPlugin) ScrapeRates(project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Scrape(project core.KeystoneProject) (result map[string]core.ResourceData, serializedMetrics string, err error) {
	data := make(map[string]core.ResourceData)

	err = p.scrapeNeutronInto(data, project.UUID)
	if err != nil {
		return nil, "", err
	}

	if p.hasOctavia {
		err = p.scrapeOctaviaInto(data, project.UUID)
		if err != nil {
			return nil, "", err
		}
	}

	return data, "", nil
}

func (p *neutronPlugin) scrapeNeutronInto(result map[string]core.ResourceData, projectUUID string) error {
	//read Neutron quota/usage
	type neutronQuotaStruct struct {
		Quota int64  `json:"limit"`
		Usage uint64 `json:"used"`
	}
	var quotas struct {
		Values map[string]neutronQuotaStruct `json:"quota"`
	}
	quotas.Values = make(map[string]neutronQuotaStruct)
	err := neutron_quotas.GetDetail(p.NeutronV2, projectUUID).ExtractInto(&quotas)
	if err != nil {
		return err
	}

	//convert data into Limes' internal format
	for _, res := range neutronResourceMeta {
		if res.Extension != "" && !p.hasExtension[res.Extension] {
			continue
		}
		values := quotas.Values[res.NeutronName]
		result[res.LimesName] = core.ResourceData{
			Quota: values.Quota,
			Usage: values.Usage,
		}
	}
	return nil
}

func (p *neutronPlugin) scrapeOctaviaInto(result map[string]core.ResourceData, projectUUID string) error {
	//read Octavia quota
	var quotas struct {
		Values map[string]int64 `json:"quota"`
	}
	err := octavia_quotas.Get(p.OctaviaV2, projectUUID).ExtractInto(&quotas)
	if err != nil {
		return err
	}

	//read Octavia usage
	usage, err := p.scrapeOctaviaUsage(projectUUID)
	if err != nil {
		return err
	}

	for _, res := range octaviaResourceMeta {
		quota, exists := quotas.Values[res.OctaviaName]
		if !exists {
			quota = quotas.Values[res.LegacyOctaviaName]
		}
		result[res.LimesName] = core.ResourceData{
			Quota: quota,
			Usage: usage[res.OctaviaName],
		}
	}
	return nil
}

// scrapeOctaviaUsage returns Octavia quota usage for a project.
func (p *neutronPlugin) scrapeOctaviaUsage(projectID string) (map[string]uint64, error) {
	var (
		usage struct {
			Values map[string]uint64 `json:"quota_usage"`
		}
		r gophercloud.Result
	)
	usage.Values = make(map[string]uint64)
	resp, err := p.OctaviaV2.Get(p.OctaviaV2.ServiceURL("quota_usage", projectID), &r.Body, nil) //nolint:bodyclose // already closed by gophercloud
	if err != nil {
		return usage.Values, err
	}

	// parse response
	_, r.Header, r.Err = gophercloud.ParseResponse(resp, err)

	// read Octavia quota
	if err := r.ExtractInto(&usage); err != nil {
		return usage.Values, err
	}

	return usage.Values, err
}

type neutronOrOctaviaQuotaSet map[string]uint64

// ToQuotaUpdateMap implements the neutron_quotas.UpdateOpts and octavia_quotas.UpdateOpts interfaces.
func (q neutronOrOctaviaQuotaSet) ToQuotaUpdateMap() (map[string]interface{}, error) {
	return map[string]interface{}{"quota": map[string]uint64(q)}, nil
}

// IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *neutronPlugin) IsQuotaAcceptableForProject(project core.KeystoneProject, fullQuotas map[string]map[string]uint64, allServiceInfos []limes.ServiceInfo) error {
	//not required for this plugin
	return nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *neutronPlugin) SetQuota(project core.KeystoneProject, quotas map[string]uint64) error {
	//collect Neutron quotas
	neutronQuotas := make(neutronOrOctaviaQuotaSet)
	for _, res := range neutronResourceMeta {
		if res.Extension != "" && !p.hasExtension[res.Extension] {
			continue
		}

		quota, exists := quotas[res.LimesName]
		if exists {
			neutronQuotas[res.NeutronName] = quota
		}
	}

	//set Neutron quotas
	_, err := neutron_quotas.Update(p.NeutronV2, project.UUID, neutronQuotas).Extract()
	if err != nil {
		return err
	}

	if p.hasOctavia {
		//collect Octavia quotas
		octaviaQuotas := make(neutronOrOctaviaQuotaSet)
		for _, res := range octaviaResourceMeta {
			quota, exists := quotas[res.LimesName]
			if exists {
				octaviaQuotas[res.OctaviaName] = quota
			}
		}

		//set Octavia quotas
		_, err = octavia_quotas.Update(p.OctaviaV2, project.UUID, octaviaQuotas).Extract()
		if err != nil {
			return err
		}
	}

	return nil
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *neutronPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *neutronPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics string) error {
	//not used by this plugin
	return nil
}
