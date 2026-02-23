// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package octavia

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/quotas"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gophercloudext"
	"github.com/sapcc/go-bits/respondwith"
)

// Logic implements the liquidapi.Logic interface for Octavia.
type Logic struct {
	// connections
	OctaviaV2 *gophercloud.ServiceClient `json:"-"`
	// state
	OwnProjectID string `json:"-"`
}

// On reading quota, we will accept any of the given names.
// On reading usage and writing quota, we will use the ResourceName.
//
// It appears that the names with underscore are deprecated since Rocky, but
// their removal has been procrastinated since then. Worse yet, GET requests on
// Yoga still return the supposedly deprecated fields only, not the intended names.
var mappedNamesForResource = map[liquid.ResourceName]struct {
	OctaviaNames []string
	DisplayName  string
}{
	"healthmonitors": {[]string{"healthmonitor", "health_monitor"}, "Health Monitors"},
	"l7policies":     {[]string{"l7policy"}, "L7 Policies"},
	"listeners":      {[]string{"listener"}, "Listeners"},
	"loadbalancers":  {[]string{"loadbalancer", "load_balancer"}, "Load Balancers"},
	"pool_members":   {[]string{"member"}, "Pool Members"},
	"pools":          {[]string{"pool"}, "Pools"},
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
	resources := make(map[liquid.ResourceName]liquid.ResourceInfo, len(mappedNamesForResource))
	for resName, mappedNames := range mappedNamesForResource {
		for _, octaviaName := range mappedNames.OctaviaNames {
			_, exists := defaultQuota[octaviaName]
			if exists {
				resources[resName] = liquid.ResourceInfo{
					DisplayName: mappedNames.DisplayName,
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
		Version:     time.Now().Unix(),
		DisplayName: "Loadbalancing",
		Resources:   resources,
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
		octaviaNames := mappedNamesForResource[resName].OctaviaNames
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
		octaviaQuotas[mappedNamesForResource[resName].OctaviaNames[0]] = req.Resources[resName].Quota
	}
	_, err := quotas.Update(ctx, l.OctaviaV2, projectUUID, octaviaQuotas).Extract()
	return err
}

// ReviewCommitmentChange implements the liquidapi.Logic interface.
func (l *Logic) ReviewCommitmentChange(ctx context.Context, req liquid.CommitmentChangeRequest, serviceInfo liquid.ServiceInfo) (liquid.CommitmentChangeResponse, error) {
	err := errors.New("this liquid does not manage commitments")
	return liquid.CommitmentChangeResponse{}, respondwith.CustomStatus(http.StatusBadRequest, err)
}
