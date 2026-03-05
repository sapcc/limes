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

// Logic implements the liquidapi.Logic interface for Neutron.
type Logic struct {
	// connections
	NeutronV2 *gophercloud.ServiceClient `json:"-"`
	// state
	OwnProjectID string `json:"-"`
}

var mappedNamesForResource = map[liquid.ResourceName]struct {
	NeutronName  string
	DisplayName  string
	CategoryName Option[liquid.CategoryName]
}{
	// core feature set
	"floating_ips":         {NeutronName: "floatingip", DisplayName: "Floating IPs"},
	"networks":             {NeutronName: "network", DisplayName: "Networks"},
	"ports":                {NeutronName: "port", DisplayName: "Ports"},
	"rbac_policies":        {NeutronName: "rbac_policy", DisplayName: "RBAC Policies"},
	"routers":              {NeutronName: "router", DisplayName: "Routers"},
	"security_group_rules": {NeutronName: "security_group_rule", DisplayName: "Security Group Rules"},
	"security_groups":      {NeutronName: "security_group", DisplayName: "Security Groups"},
	"subnet_pools":         {NeutronName: "subnetpool", DisplayName: "Subnet Pools"},
	"subnets":              {NeutronName: "subnet", DisplayName: "Subnets"},
	// extensions
	"bgpvpns": {NeutronName: "bgpvpn", DisplayName: "BGP VPNs"},
	"trunks":  {NeutronName: "trunk", DisplayName: "Trunks"},
	// VPNaaS
	"endpoint_groups":        {NeutronName: "endpoint_group", DisplayName: "Endpoint Groups", CategoryName: Some(liquid.CategoryName("vpnaas"))},
	"ikepolicies":            {NeutronName: "ikepolicy", DisplayName: "IKE Policies", CategoryName: Some(liquid.CategoryName("vpnaas"))},
	"ipsec_site_connections": {NeutronName: "ipsec_site_connection", DisplayName: "IPsec Site Connections", CategoryName: Some(liquid.CategoryName("vpnaas"))},
	"ipsecpolicies":          {NeutronName: "ipsecpolicy", DisplayName: "IPsec Policies", CategoryName: Some(liquid.CategoryName("vpnaas"))},
	"vpnservices":            {NeutronName: "vpnservice", DisplayName: "VPN Services", CategoryName: Some(liquid.CategoryName("vpnaas"))},
	// FWaaS
	"firewall_groups":   {NeutronName: "firewall_group", DisplayName: "Firewall Groups", CategoryName: Some(liquid.CategoryName("fwaas"))},
	"firewall_policies": {NeutronName: "firewall_policy", DisplayName: "Firewall Policies", CategoryName: Some(liquid.CategoryName("fwaas"))},
	"firewall_rules":    {NeutronName: "firewall_rule", DisplayName: "Firewall Rules", CategoryName: Some(liquid.CategoryName("fwaas"))},
}

func getNeutronNameForResource(resourceName liquid.ResourceName) string {
	val, exists := mappedNamesForResource[resourceName]
	if exists {
		return val.NeutronName
	} else {
		return string(resourceName)
	}
}

var resourceDisplayNamePrefixByResourcePrefix = map[string]string{
	"routers_flavor_": "Routers of flavor:",
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

func getMappedNamesIfQuotaRelevant(neutronName string) Option[struct {
	resourceName liquid.ResourceName
	displayName  string
	categoryName Option[liquid.CategoryName]
}] {
	// check for static quota name
	for resName, v := range mappedNamesForResource {
		if v.NeutronName == neutronName {
			return Some(struct {
				resourceName liquid.ResourceName
				displayName  string
				categoryName Option[liquid.CategoryName]
			}{
				resName,
				v.DisplayName,
				v.CategoryName,
			})
		}
	}

	// check for dynamic quota names
	for prefix, resourceDisplayNamePrefix := range resourceDisplayNamePrefixByResourcePrefix {
		if sanitizedName, ok := strings.CutPrefix(neutronName, prefix); ok {
			return Some(struct {
				resourceName liquid.ResourceName
				displayName  string
				categoryName Option[liquid.CategoryName]
			}{
				liquid.ResourceName(neutronName),
				fmt.Sprintf("%s %s", resourceDisplayNamePrefix, sanitizedName),
				None[liquid.CategoryName](),
			})
		}
	}
	return None[struct {
		resourceName liquid.ResourceName
		displayName  string
		categoryName Option[liquid.CategoryName]
	}]()
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
	resources := make(map[liquid.ResourceName]liquid.ResourceInfo, len(mappedNamesForResource))
	usesCategory := make(map[liquid.CategoryName]bool)
	for neutronName := range data.Quota {
		mappedNames, isRelevantQuota := getMappedNamesIfQuotaRelevant(neutronName).Unpack()
		if isRelevantQuota {
			resources[mappedNames.resourceName] = liquid.ResourceInfo{
				DisplayName: mappedNames.displayName,
				Category:    mappedNames.categoryName,
				Unit:        liquid.UnitNone,
				Topology:    liquid.FlatTopology,
				HasCapacity: false,
				HasQuota:    true,
			}
			if categoryName, ok := mappedNames.categoryName.Unpack(); ok {
				usesCategory[categoryName] = true
			}
		}
	}

	// declare exactly those categories that we need
	categories := make(map[liquid.CategoryName]liquid.CategoryInfo, len(usesCategory))
	if usesCategory["fwaas"] {
		categories["fwaas"] = liquid.CategoryInfo{DisplayName: "Firewall as a Service"}
	}
	if usesCategory["vpnaas"] {
		categories["vpnaas"] = liquid.CategoryInfo{DisplayName: "VPN as a Service"}
	}

	return liquid.ServiceInfo{
		Version:     time.Now().Unix(),
		DisplayName: "Network",
		Categories:  categories,
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
