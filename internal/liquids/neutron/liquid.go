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
	NeutronName string
	DisplayName string
}{
	// core feature set
	"floatingip":          {"floating_ips", "Floating IPs"},
	"network":             {"networks", "Networks"},
	"port":                {"ports", "Ports"},
	"rbac_policy":         {"rbac_policies", "RBAC Policies"},
	"router":              {"routers", "Routers"},
	"security_group_rule": {"security_group_rules", "Security Group Rules"},
	"security_group":      {"security_groups", "Security Groups"},
	"subnetpool":          {"subnet_pools", "Subnet Pools"},
	"subnet":              {"subnets", "Subnets"},
	// extensions
	"bgpvpn": {"bgpvpns", "BGP VPNs"},
	"trunk":  {"trunks", "Trunks"},
	// VPNaaS
	"endpoint_group":        {"endpoint_groups", "Endpoint Groups"},
	"ikepolicy":             {"ikepolicies", "IKE Policies"},
	"ipsec_site_connection": {"ipsec_site_connections", "IPsec Site Connections"},
	"ipsecpolicy":           {"ipsecpolicies", "IPsec Policies"},
	"vpnservice":            {"vpnservices", "VPN Services"},
	// FWaaS
	"firewall_group":  {"firewall_group", "Firewall Groups"},
	"firewall_policy": {"firewall_policy", "Firewall Policies"},
	"firewall_rule":   {"firewall_rule", "Firewall Rules"},
}

func getNeutronNameForResource(resourceName liquid.ResourceName) string {
	val, exists := mappedNamesForResource[resourceName]
	if exists {
		return val.NeutronName
	} else {
		return string(resourceName)
	}
}

var mappedDisplayNamePrefixesByResourcePrefix = map[string]string{
	"routers_flavor_": "Routers Flavor ",
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
}] {
	// check for static quota name
	if mappedNames, ok := mappedNamesForResource[liquid.ResourceName(neutronName)]; ok {
		return Some(struct {
			resourceName liquid.ResourceName
			displayName  string
		}{liquid.ResourceName(neutronName), mappedNames.DisplayName})
	}

	// check for dynamic quota names
	for prefix, displayNamePrefix := range mappedDisplayNamePrefixesByResourcePrefix {
		if sanitizedName, ok := strings.CutPrefix(neutronName, prefix); ok {
			return Some(struct {
				resourceName liquid.ResourceName
				displayName  string
			}{liquid.ResourceName(neutronName), fmt.Sprintf(`%s "%s"`, displayNamePrefix, sanitizedName)})
		}
	}
	return None[struct {
		resourceName liquid.ResourceName
		displayName  string
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
	for neutronName := range data.Quota {
		mappedNames, isRelevantQuota := getMappedNamesIfQuotaRelevant(neutronName).Unpack()

		if isRelevantQuota {
			resources[mappedNames.resourceName] = liquid.ResourceInfo{
				DisplayName: mappedNames.displayName,
				Unit:        liquid.UnitNone,
				Topology:    liquid.FlatTopology,
				HasCapacity: false,
				HasQuota:    true,
			}
		}
	}

	return liquid.ServiceInfo{
		Version:     time.Now().Unix(),
		DisplayName: "Network",
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
