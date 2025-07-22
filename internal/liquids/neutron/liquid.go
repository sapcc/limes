// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package neutron

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/networking/v2/extensions/quotas"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gophercloudext"
	"github.com/sapcc/go-bits/respondwith"
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
	// VPNaaS
	"endpoint_groups":        "endpoint_group",
	"ikepolicies":            "ikepolicy",
	"ipsec_site_connections": "ipsec_site_connection",
	"ipsecpolicies":          "ipsecpolicy",
	"vpnservices":            "vpnservice",
	// FWaaS
	"firewall_groups":   "firewall_group",
	"firewall_policies": "firewall_policy",
	"firewall_rules":    "firewall_rule",
}

func getNeutronNameForResource(resourceName liquid.ResourceName) string {
	val, exists := neutronNameForResource[resourceName]
	if exists {
		return val
	} else {
		return string(resourceName)
	}
}

var dynamicQuotaPrefixes = []string{
	"routers_flavor_",
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

func getResourceNameIfQuotaRelevant(neutronName string) Option[liquid.ResourceName] {
	// check for static quota name
	for resName, v := range neutronNameForResource {
		if v == neutronName {
			return Some(resName)
		}
	}

	// check for dynamic quota names
	for _, prefix := range dynamicQuotaPrefixes {
		if strings.HasPrefix(neutronName, prefix) {
			return Some(liquid.ResourceName(neutronName))
		}
	}
	return None[liquid.ResourceName]()
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	// probe default quotas to see which resources are supported by Neutron
	url := l.NeutronV2.ServiceURL("quotas", l.OwnProjectID, "default")
	var r gophercloud.Result
	_, r.Header, r.Err = gophercloud.ParseResponse(l.NeutronV2.Get(ctx, url, &r.Body, nil)) //nolint:bodyclose
	var data struct {
		Quota map[string]int `json:"quota"`
	}
	err := r.ExtractInto(&data)
	if err != nil {
		return liquid.ServiceInfo{}, err
	}

	// we support all resources that Neutron supports and that we also know about
	resources := make(map[liquid.ResourceName]liquid.ResourceInfo, len(neutronNameForResource))
	for neutronName := range data.Quota {
		resName, isRelevantQuota := getResourceNameIfQuotaRelevant(neutronName).Unpack()

		if isRelevantQuota {
			resources[resName] = liquid.ResourceInfo{
				Unit:        liquid.UnitNone,
				Topology:    liquid.FlatTopology,
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
		resData := data.Resources[getNeutronNameForResource(resName)]
		resourceReports[resName] = &liquid.ResourceUsageReport{
			Quota: Some(resData.Quota),
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
		neutronQuotas[getNeutronNameForResource(resName)] = req.Resources[resName].Quota
	}
	_, err := quotas.Update(ctx, l.NeutronV2, projectUUID, neutronQuotas).Extract()
	return err
}

type quotaSet map[string]uint64

// ToQuotaUpdateMap implements the neutron_quotas.UpdateOpts and octavia_quotas.UpdateOpts interfaces.
func (q quotaSet) ToQuotaUpdateMap() (map[string]any, error) {
	return map[string]any{"quota": map[string]uint64(q)}, nil
}

// ReviewCommitmentChange implements the liquidapi.Logic interface.
func (l *Logic) ReviewCommitmentChange(ctx context.Context, req liquid.CommitmentChangeRequest, serviceInfo liquid.ServiceInfo) (liquid.CommitmentChangeResponse, error) {
	err := errors.New("this liquid does not manage commitments")
	return liquid.CommitmentChangeResponse{}, respondwith.CustomStatus(http.StatusBadRequest, err)
}
