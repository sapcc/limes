/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package neutron

import (
	"context"
	"fmt"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/quotas"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gophercloudext"
)

type Logic struct {
	// connections
	NeutronV2 *gophercloud.ServiceClient `yaml:"-"`
	// state
	OwnProjectID string `yaml:"-"`
}

var neutronNameForResource = map[liquid.ResourceName]string{
	// core feature set
	"floating_ips":         "floatingip",
	"networks":             "network",
	"ports":                "port",
	"rbac_policies":        "rbac_policy",
	"routers":              "router",
	"security_group_rules": "security_group_rule",
	"security_groups":      "security_group",
	"subnet_pools":         "subnetpool",
	"subnets":              "subnet",
	// extensions
	"bgpvpns": "bgpvpn",
	"trunks":  "trunk",
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.NeutronV2, err = openstack.NewNetworkV2(provider, eo)
	if err != nil {
		return fmt.Errorf("cannot initialize Neutron v2 client: %w", err)
	}
	l.OwnProjectID, err = gophercloudext.GetProjectIDFromTokenScope(provider)
	if err != nil {
		return fmt.Errorf("cannot find project scope of own token: %w", err)
	}
	return nil
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	// probe default quotas to see which resources are supported by Neutron
	url := l.NeutronV2.ServiceURL("quotas", l.OwnProjectID, "default")
	var r gophercloud.Result
	_, r.Header, r.Err = gophercloud.ParseResponse(l.NeutronV2.Get(ctx, url, &r.Body, nil))
	var data struct {
		Quota map[string]int `json:"quota"`
	}
	err := r.ExtractInto(&data)
	if err != nil {
		return liquid.ServiceInfo{}, err
	}

	// we support all resources that Neutron supports and that we also know about
	resources := make(map[liquid.ResourceName]liquid.ResourceInfo, len(neutronNameForResource))
	for resName, neutronName := range neutronNameForResource {
		_, exists := data.Quota[neutronName]
		if exists {
			resources[resName] = liquid.ResourceInfo{
				Unit:        liquid.UnitNone,
				Topology:    liquid.FlatResourceTopology,
				HasCapacity: false,
				HasQuota:    true,
			}
		}
	}

	return liquid.ServiceInfo{
		Version:   time.Now().Unix(),
		Resources: resources,
	}, nil
}

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	// no resources report capacity
	return liquid.ServiceCapacityReport{InfoVersion: serviceInfo.Version}, nil
}

// ScanUsage implements the liquidapi.Logic interface.
func (l *Logic) ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error) {
	var data struct {
		Resources map[string]struct {
			Quota int64  `json:"limit"`
			Usage uint64 `json:"used"`
		} `json:"quota"`
	}
	err := quotas.GetDetail(ctx, l.NeutronV2, projectUUID).ExtractInto(&data)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	resourceReports := make(map[liquid.ResourceName]*liquid.ResourceUsageReport, len(serviceInfo.Resources))
	for resName := range serviceInfo.Resources {
		resData := data.Resources[neutronNameForResource[resName]]
		resourceReports[resName] = &liquid.ResourceUsageReport{
			Quota: &resData.Quota,
			PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: resData.Usage}),
		}
	}

	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources:   resourceReports,
	}, nil
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	neutronQuotas := make(quotaSet, len(serviceInfo.Resources))
	for resName := range serviceInfo.Resources {
		neutronQuotas[neutronNameForResource[resName]] = req.Resources[resName].Quota
	}
	_, err := quotas.Update(ctx, l.NeutronV2, projectUUID, neutronQuotas).Extract()
	return err
}

type quotaSet map[string]uint64

// ToQuotaUpdateMap implements the neutron_quotas.UpdateOpts and octavia_quotas.UpdateOpts interfaces.
func (q quotaSet) ToQuotaUpdateMap() (map[string]any, error) {
	return map[string]any{"quota": map[string]uint64(q)}, nil
}
