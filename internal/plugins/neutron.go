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
	"context"
	"fmt"
	"math/big"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/common/extensions"
	octavia_quotas "github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/quotas"
	neutron_quotas "github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/quotas"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

type neutronPlugin struct {
	// computed state
	resources    map[liquid.ResourceName]liquid.ResourceInfo `yaml:"-"`
	hasExtension map[string]bool                             `yaml:"-"`
	hasOctavia   bool                                        `yaml:"-"`
	// connections
	NeutronV2 *gophercloud.ServiceClient `yaml:"-"`
	OctaviaV2 *gophercloud.ServiceClient `yaml:"-"`
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &neutronPlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, serviceType db.ServiceType) (err error) {
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
		_, err := extensions.Get(ctx, p.NeutronV2, resource.Extension).Extract()
		switch {
		case err == nil:
			p.hasExtension[resource.Extension] = true
		case gophercloud.ResponseCodeIs(err, http.StatusNotFound):
			p.hasExtension[resource.Extension] = false
		default:
			return fmt.Errorf("cannot check for %q support in Neutron: %w", resource.Extension, err)
		}
		logg.Info("Neutron extension %s is enabled = %t", resource.Extension, p.hasExtension[resource.Extension])
	}

	// Octavia supported?
	p.OctaviaV2, err = openstack.NewLoadBalancerV2(provider, eo)
	switch {
	case err == nil:
		p.hasOctavia = true
	case errext.IsOfType[*gophercloud.ErrEndpointNotFound](err):
		p.hasOctavia = false
	default:
		return err
	}

	// filter resource list to reflect supported extensions and services
	resInfo := liquid.ResourceInfo{
		Unit:     limes.UnitNone,
		HasQuota: true,
	}
	p.resources = make(map[liquid.ResourceName]liquid.ResourceInfo)
	for _, resource := range neutronResourceMeta {
		if resource.Extension == "" || p.hasExtension[resource.Extension] {
			p.resources[resource.LimesName] = resInfo
		}
	}
	if p.hasOctavia {
		for _, resource := range octaviaResourceMeta {
			p.resources[resource.LimesName] = resInfo
		}
	}

	return nil
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *neutronPlugin) PluginTypeID() string {
	return "network"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *neutronPlugin) ServiceInfo() core.ServiceInfo {
	return core.ServiceInfo{
		ProductName: "neutron",
		Area:        "network",
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Resources() map[liquid.ResourceName]liquid.ResourceInfo {
	return p.resources
}

// Rates implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Rates() map[db.RateName]core.RateInfo {
	return nil
}

type neutronResourceMetadata struct {
	LimesName   liquid.ResourceName
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
	LimesName         liquid.ResourceName
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
func (p *neutronPlugin) ScrapeRates(ctx context.Context, project core.KeystoneProject, prevSerializedState string) (result map[db.RateName]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *neutronPlugin) Scrape(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[liquid.ResourceName]core.ResourceData, serializedMetrics []byte, err error) {
	data := make(map[liquid.ResourceName]core.ResourceData)

	err = p.scrapeNeutronInto(ctx, data, project.UUID)
	if err != nil {
		return nil, nil, err
	}

	if p.hasOctavia {
		err = p.scrapeOctaviaInto(ctx, data, project.UUID)
		if err != nil {
			return nil, nil, err
		}
	}

	return data, nil, nil
}

func (p *neutronPlugin) scrapeNeutronInto(ctx context.Context, result map[liquid.ResourceName]core.ResourceData, projectUUID string) error {
	// read Neutron quota/usage
	type neutronQuotaStruct struct {
		Quota int64  `json:"limit"`
		Usage uint64 `json:"used"`
	}
	var quotas struct {
		Values map[string]neutronQuotaStruct `json:"quota"`
	}
	quotas.Values = make(map[string]neutronQuotaStruct)
	err := neutron_quotas.GetDetail(ctx, p.NeutronV2, projectUUID).ExtractInto(&quotas)
	if err != nil {
		return err
	}

	// convert data into Limes' internal format
	for _, res := range neutronResourceMeta {
		if res.Extension != "" && !p.hasExtension[res.Extension] {
			continue
		}
		values := quotas.Values[res.NeutronName]
		result[res.LimesName] = core.ResourceData{
			Quota: values.Quota,
			UsageData: core.InAnyAZ(core.UsageData{
				Usage: values.Usage,
			}),
		}
	}
	return nil
}

func (p *neutronPlugin) scrapeOctaviaInto(ctx context.Context, result map[liquid.ResourceName]core.ResourceData, projectUUID string) error {
	// read Octavia quota
	var quotas struct {
		Values map[string]int64 `json:"quota"`
	}
	err := octavia_quotas.Get(ctx, p.OctaviaV2, projectUUID).ExtractInto(&quotas)
	if err != nil {
		return err
	}

	// read Octavia usage
	usage, err := p.scrapeOctaviaUsage(ctx, projectUUID)
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
			UsageData: core.InAnyAZ(core.UsageData{
				Usage: usage[res.OctaviaName],
			}),
		}
	}
	return nil
}

// scrapeOctaviaUsage returns Octavia quota usage for a project.
func (p *neutronPlugin) scrapeOctaviaUsage(ctx context.Context, projectID string) (map[string]uint64, error) {
	var (
		usage struct {
			Values map[string]uint64 `json:"quota_usage"`
		}
		r gophercloud.Result
	)
	usage.Values = make(map[string]uint64)
	resp, err := p.OctaviaV2.Get(ctx, p.OctaviaV2.ServiceURL("quota_usage", projectID), &r.Body, nil) //nolint:bodyclose // already closed by gophercloud
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
func (q neutronOrOctaviaQuotaSet) ToQuotaUpdateMap() (map[string]any, error) {
	return map[string]any{"quota": map[string]uint64(q)}, nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *neutronPlugin) SetQuota(ctx context.Context, project core.KeystoneProject, quotas map[liquid.ResourceName]uint64) error {
	// collect Neutron quotas
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

	// set Neutron quotas
	_, err := neutron_quotas.Update(ctx, p.NeutronV2, project.UUID, neutronQuotas).Extract()
	if err != nil {
		return err
	}

	if p.hasOctavia {
		// collect Octavia quotas
		octaviaQuotas := make(neutronOrOctaviaQuotaSet)
		for _, res := range octaviaResourceMeta {
			quota, exists := quotas[res.LimesName]
			if exists {
				octaviaQuotas[res.OctaviaName] = quota
			}
		}

		// set Octavia quotas
		_, err = octavia_quotas.Update(ctx, p.OctaviaV2, project.UUID, octaviaQuotas).Extract()
		if err != nil {
			return err
		}
	}

	return nil
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *neutronPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	// not used by this plugin
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *neutronPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	// not used by this plugin
	return nil
}
