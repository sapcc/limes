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
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
)

type neutronPlugin struct {
	cfg               core.ServiceConfiguration
	resources         []limes.ResourceInfo
	hasLBaaSExtension bool //TODO remove after migrating to newer Neutron
	hasOctavia        bool
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
			cfg:       c,
			resources: neutronResources,
		}
	})
}

//Init implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	client, err := openstack.NewNetworkV2(provider, eo)
	if err != nil {
		return err
	}

	// LBaaSv2 supported?
	r := extensions.Get(client, "lbaasv2")
	switch r.Result.Err.(type) {
	case gophercloud.ErrDefault404:
		p.hasLBaaSExtension = false
	case nil:
		p.hasLBaaSExtension = true
	default:
		return fmt.Errorf("cannot check for lbaasv2 support in Neutron: %s", r.Result.Err.Error())
	}

	// Octavia supported?
	_, err = openstack.NewLoadBalancerV2(provider, eo)
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

//Rates implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Rates() []limes.RateInfo {
	return nil
}

type neutronResourceMetadata struct {
	LimesName   string
	NeutronName string
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
}

type octaviaResourceMetadata struct {
	LimesName         string
	OctaviaName       string
	LegacyOctaviaName string
	DoNotSetQuota     bool
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
		LimesName:     "l7policies",
		OctaviaName:   "l7policy",
		DoNotSetQuota: true, //this quota is supported from Victoria onwards, but we have an older Octavia at the moment
	},
	{
		LimesName:   "pool_members",
		OctaviaName: "member",
	},
}

type neutronQueryOpts struct {
	Fields      string `q:"fields"`
	ProjectUUID string `q:"tenant_id"`
}

//ScrapeRates implements the core.QuotaPlugin interface.
func (p *neutronPlugin) ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, domainUUID, projectUUID string, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

//Scrape implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, domainUUID, projectUUID string) (map[string]core.ResourceData, string, error) {
	data := make(map[string]core.ResourceData)

	err := p.scrapeNeutronInto(data, provider, eo, projectUUID)
	if err != nil {
		return nil, "", err
	}

	if p.hasOctavia {
		err = p.scrapeOctaviaInto(data, provider, eo, projectUUID)
		if err != nil {
			return nil, "", err
		}
	}

	return data, "", nil
}

func (p *neutronPlugin) scrapeNeutronInto(result map[string]core.ResourceData, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, projectUUID string) error {
	networkV2, err := openstack.NewNetworkV2(provider, eo)
	if err != nil {
		return err
	}

	//read Neutron quota/usage
	type neutronQuotaStruct struct {
		Quota int64  `json:"limit"`
		Usage uint64 `json:"used"`
	}
	var quotas struct {
		Values map[string]neutronQuotaStruct `json:"quota"`
	}
	quotas.Values = make(map[string]neutronQuotaStruct)
	err = neutron_quotas.GetDetail(networkV2, projectUUID).ExtractInto(&quotas)
	if err != nil {
		return err
	}

	//convert data into Limes' internal format
	for _, res := range neutronResourceMeta {
		values := quotas.Values[res.NeutronName]
		result[res.LimesName] = core.ResourceData{
			Quota: values.Quota,
			Usage: values.Usage,
		}
	}
	return nil
}

func (p *neutronPlugin) scrapeOctaviaInto(result map[string]core.ResourceData, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, projectUUID string) error {
	octaviaV2, err := openstack.NewLoadBalancerV2(provider, eo)
	if err != nil {
		return err
	}

	//read Octavia quota
	var quotas struct {
		Values map[string]int64 `json:"quota"`
	}
	err = octavia_quotas.Get(octaviaV2, projectUUID).ExtractInto(&quotas)
	if err != nil {
		return err
	}

	//read Octavia quota
	usage, err := p.scrapeOctaviaUsage(octaviaV2, projectUUID)
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
func (p *neutronPlugin) scrapeOctaviaUsage(client *gophercloud.ServiceClient, projectID string) (map[string]uint64, error) {
	var (
		usage struct {
			Values map[string]uint64 `json:"quota_usage"`
		}
		r gophercloud.Result
	)
	usage.Values = make(map[string]uint64)
	resp, err := client.Get(client.ServiceURL("quota_usage", projectID), &r.Body, nil)
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

//ToQuotaUpdateMap implements the neutron_quotas.UpdateOpts and octavia_quotas.UpdateOpts interfaces.
func (q neutronOrOctaviaQuotaSet) ToQuotaUpdateMap() (map[string]interface{}, error) {
	return map[string]interface{}{"quota": map[string]uint64(q)}, nil
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *neutronPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, domainUUID, projectUUID string, quotas map[string]uint64) error {
	//collect Neutron quotas
	neutronQuotas := make(neutronOrOctaviaQuotaSet)
	for _, res := range neutronResourceMeta {
		quota, exists := quotas[res.LimesName]
		if exists {
			neutronQuotas[res.NeutronName] = quota
		}
	}
	//even if Octavia is there, when Neutron has the LBaaS extension enabled
	//(with the proxy driver for Octavia), it needs to know about LBaaS quotas as
	//well since it also enforces them
	if p.hasLBaaSExtension {
		for _, res := range octaviaResourceMeta {
			quota, exists := quotas[res.LimesName]
			//NOTE: do not check DoNotSetQuota here, since Neutron knows how to deal with the "l7policy" quota
			if exists {
				neutronQuotas[res.OctaviaName] = quota
			}
		}
	}

	//set Neutron quotas
	networkV2, err := openstack.NewNetworkV2(provider, eo)
	if err != nil {
		return err
	}
	_, err = neutron_quotas.Update(networkV2, projectUUID, neutronQuotas).Extract()
	if err != nil {
		return err
	}

	if p.hasOctavia {
		//collect Octavia quotas
		octaviaQuotas := make(neutronOrOctaviaQuotaSet)
		for _, res := range octaviaResourceMeta {
			quota, exists := quotas[res.LimesName]
			if exists && !res.DoNotSetQuota {
				octaviaQuotas[res.OctaviaName] = quota
			}
		}

		//set Octavia quotas
		octaviaV2, err := openstack.NewLoadBalancerV2(provider, eo)
		if err != nil {
			return err
		}
		_, err = octavia_quotas.Update(octaviaV2, projectUUID, octaviaQuotas).Extract()
		if err != nil {
			return err
		}
	}

	return nil
}

//DescribeMetrics implements the core.QuotaPlugin interface.
func (p *neutronPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

//CollectMetrics implements the core.QuotaPlugin interface.
func (p *neutronPlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID, domainUUID, projectUUID, serializedMetrics string) error {
	//not used by this plugin
	return nil
}
