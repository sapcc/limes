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
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/domains"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/projects"

	"github.com/sapcc/limes/internal/core"
)

type listDiscoveryPlugin struct{}

func init() {
	core.DiscoveryPluginRegistry.Add(func() core.DiscoveryPlugin { return &listDiscoveryPlugin{} })
}

// PluginTypeID implements the core.DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) PluginTypeID() string {
	return "list"
}

// Init implements the core.DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) Init(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil //not used
}

// ListDomains implements the core.DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) ListDomains(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) ([]core.KeystoneDomain, error) {
	client, err := openstack.NewIdentityV3(provider, eo)
	if err != nil {
		return nil, err
	}

	allPages, err := domains.List(client, nil).AllPages()
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
func (p *listDiscoveryPlugin) ListProjects(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, domain core.KeystoneDomain) ([]core.KeystoneProject, error) {
	client, err := openstack.NewIdentityV3(provider, eo)
	if err != nil {
		return nil, err
	}

	allPages, err := projects.List(client, projects.ListOpts{DomainID: domain.UUID}).AllPages()
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
