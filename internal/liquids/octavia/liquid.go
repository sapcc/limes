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

package octavia

import (
	"context"
	"fmt"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/quotas"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gophercloudext"
)

type Logic struct {
	// connections
	OctaviaV2 *gophercloud.ServiceClient `yaml:"-"`
	// state
	OwnProjectID string `yaml:"-"`
}

// On reading quota, we will accept any of the given names.
// On reading usage and writing quota, we will use the first name in the list.
//
// It appears that the names with underscore are deprecated since Rocky, but
// their removal has been procrastinated since then. Worse yet, GET requests on
// Yoga still return the supposedly deprecated fields only, not the intended names.
var octaviaNamesForResource = map[liquid.ResourceName][]string{
	"healthmonitors": {"healthmonitor", "health_monitor"},
	"l7policies":     {"l7policy"},
	"listeners":      {"listener"},
	"loadbalancers":  {"loadbalancer", "load_balancer"},
	"pool_members":   {"member"},
	"pools":          {"pool"},
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.OctaviaV2, err = openstack.NewLoadBalancerV2(provider, eo)
	if err != nil {
		return fmt.Errorf("cannot initialize Octavia v2 client: %w", err)
	}
	l.OwnProjectID, err = gophercloudext.GetProjectIDFromTokenScope(provider)
	if err != nil {
		return fmt.Errorf("cannot find project scope of own token: %w", err)
	}
	return nil
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	// probe default quotas to see which resources are supported by Octavia
	defaultQuota, err := getQuota(ctx, l.OctaviaV2, "defaults")
	if err != nil {
		return liquid.ServiceInfo{}, err
	}

	// we support all resources that Octavia supports and that we also know about
	resources := make(map[liquid.ResourceName]liquid.ResourceInfo, len(octaviaNamesForResource))
	for resName, octaviaNames := range octaviaNamesForResource {
		for _, octaviaName := range octaviaNames {
			_, exists := defaultQuota[octaviaName]
			if exists {
				resources[resName] = liquid.ResourceInfo{
					Unit:        liquid.UnitNone,
					Topology:    liquid.FlatTopology,
					HasCapacity: false,
					HasQuota:    true,
				}
				break // from inner loop
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
	octaviaQuotas, err := getQuota(ctx, l.OctaviaV2, projectUUID)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}
	octaviaUsage, err := getUsage(ctx, l.OctaviaV2, projectUUID)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	resourceReports := make(map[liquid.ResourceName]*liquid.ResourceUsageReport, len(serviceInfo.Resources))
	for resName := range serviceInfo.Resources {
		octaviaNames := octaviaNamesForResource[resName]
		report := liquid.ResourceUsageReport{
			PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: octaviaUsage[octaviaNames[0]]}),
		}
		for _, octaviaName := range octaviaNames {
			quota, exists := octaviaQuotas[octaviaName]
			if exists {
				report.Quota = Some(quota)
				break
			}
		}
		resourceReports[resName] = &report
	}

	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources:   resourceReports,
	}, nil
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	octaviaQuotas := make(quotaSet, len(serviceInfo.Resources))
	for resName := range serviceInfo.Resources {
		octaviaQuotas[octaviaNamesForResource[resName][0]] = req.Resources[resName].Quota
	}
	_, err := quotas.Update(ctx, l.OctaviaV2, projectUUID, octaviaQuotas).Extract()
	return err
}
