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
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
)

type neutronPlugin struct {
	cfg           core.ServiceConfiguration
	resources     []limes.ResourceInfo
	resourcesMeta []neutronResourceMetadata
	hasOctavia    bool
}

var neutronResources = []limes.ResourceInfo{
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
	core.RegisterQuotaPlugin(func(c core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		return &neutronPlugin{
			cfg:           c,
			resources:     neutronResources,
			resourcesMeta: neutronResourceMeta,
		}
	})
}

//Init implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	_, err := openstack.NewLoadBalancerV2(provider, eo)
	switch err.(type) {
	case *gophercloud.ErrEndpointNotFound:
		p.hasOctavia = false
	case nil:
		p.hasOctavia = true
	default:
		return err
	}

	return nil
}

//ServiceInfo implements the core.QuotaPlugin interface.
func (p *neutronPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "network",
		ProductName: "neutron",
		Area:        "network",
	}
}

//Resources implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Resources() []limes.ResourceInfo {
	return p.resources
}

type neutronResourceMetadata struct {
	LimesName   string
	NeutronName string
	InOctavia   bool
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
		LimesName:   "loadbalancers",
		NeutronName: "loadbalancer",
		InOctavia:   true,
	},
	{
		LimesName:   "listeners",
		NeutronName: "listener",
		InOctavia:   true,
	},
	{
		LimesName:   "pools",
		NeutronName: "pool",
		InOctavia:   true,
	},
	{
		LimesName:   "healthmonitors",
		NeutronName: "healthmonitor",
		InOctavia:   true,
	},
	{
		LimesName:   "l7policies",
		NeutronName: "l7policy",
		InOctavia:   false, //for some reason, Octavia does not support this quota type
	},
	{
		LimesName:   "pool_members",
		NeutronName: "member",
		InOctavia:   true,
	},
}

type neutronQueryOpts struct {
	Fields      string `q:"fields"`
	ProjectUUID string `q:"tenant_id"`
}

//Scrape implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string) (map[string]core.ResourceData, error) {
	client, err := openstack.NewNetworkV2(provider, eo)
	if err != nil {
		return nil, err
	}

	var result gophercloud.Result
	url := client.ServiceURL("quotas", projectUUID, "details")
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	type neutronQuotaStruct struct {
		Quota int64  `json:"limit"`
		Usage uint64 `json:"used"`
	}
	var quotas struct {
		Values map[string]neutronQuotaStruct `json:"quota"`
	}
	quotas.Values = make(map[string]neutronQuotaStruct)
	err = result.ExtractInto(&quotas)
	if err != nil {
		return nil, err
	}

	//convert data returned by Neutron into Limes' internal format
	data := make(map[string]core.ResourceData)
	for _, res := range p.resourcesMeta {
		values := quotas.Values[res.NeutronName]
		data[res.LimesName] = core.ResourceData{
			Quota: values.Quota,
			Usage: values.Usage,
		}
	}
	return data, nil
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *neutronPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	//map resource names from Limes to Neutron/Octavia
	var neutronRequestData struct {
		Quotas map[string]uint64 `json:"quota"`
	}
	var octaviaRequestData struct {
		Quotas map[string]uint64 `json:"quota"`
	}
	neutronRequestData.Quotas = make(map[string]uint64)
	octaviaRequestData.Quotas = make(map[string]uint64)
	for _, res := range p.resourcesMeta {
		quota, exists := quotas[res.LimesName]
		if exists {
			neutronRequestData.Quotas[res.NeutronName] = quota
		}
		if exists && res.InOctavia {
			octaviaRequestData.Quotas[res.NeutronName] = quota
		}
	}

	neutronClient, err := openstack.NewNetworkV2(provider, eo)
	if err != nil {
		return err
	}
	neutronURL := neutronClient.ServiceURL("quotas", projectUUID)
	_, err = neutronClient.Put(neutronURL, neutronRequestData, nil, &gophercloud.RequestOpts{OkCodes: []int{200}})
	if err != nil {
		return err
	}

	if p.hasOctavia {
		octaviaClient, err := openstack.NewLoadBalancerV2(provider, eo)
		if err != nil {
			return err
		}
		octaviaURL := octaviaClient.ServiceURL("lbaas", "quotas", projectUUID)
		_, err = octaviaClient.Put(octaviaURL, octaviaRequestData, nil, &gophercloud.RequestOpts{OkCodes: []int{202}})
		if err != nil {
			return err
		}
	}

	return nil
}
