// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package plugins

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/domains"
	"github.com/gophercloud/gophercloud/v2/openstack/identity/v3/projects"

	"github.com/sapcc/limes/internal/core"
)

type listDiscoveryPlugin struct {
	// connections
	KeystoneV3 *gophercloud.ServiceClient `yaml:"-"`
}

func init() {
	core.DiscoveryPluginRegistry.Add(func() core.DiscoveryPlugin { return &listDiscoveryPlugin{} })
}

// PluginTypeID implements the core.DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) PluginTypeID() string {
	return "list"
}

// Init implements the core.DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	p.KeystoneV3, err = openstack.NewIdentityV3(provider, eo)
	return err
}

// ListDomains implements the core.DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) ListDomains(ctx context.Context) ([]core.KeystoneDomain, error) {
	allPages, err := domains.List(p.KeystoneV3, nil).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	allDomains, err := domains.ExtractDomains(allPages)
	if err != nil {
		return nil, err
	}

	var result []core.KeystoneDomain
	for _, p := range allDomains {
		result = append(result, core.KeystoneDomain{
			UUID: p.ID,
			Name: p.Name,
		})
	}
	return result, nil
}

// ListProjects implements the core.DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) ListProjects(ctx context.Context, domain core.KeystoneDomain) ([]core.KeystoneProject, error) {
	allPages, err := projects.List(p.KeystoneV3, projects.ListOpts{DomainID: domain.UUID}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	allProjects, err := projects.ExtractProjects(allPages)
	if err != nil {
		return nil, err
	}

	var result []core.KeystoneProject
	for _, p := range allProjects {
		result = append(result, core.KeystoneProject{
			UUID:       p.ID,
			Name:       p.Name,
			ParentUUID: p.ParentID,
			Domain:     domain,
		})
	}
	return result, nil
}
