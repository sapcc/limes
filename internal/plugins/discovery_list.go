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
