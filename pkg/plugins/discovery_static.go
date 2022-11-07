/*******************************************************************************
*
* Copyright 2021 SAP SE
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
	"errors"
	"fmt"

	"github.com/gophercloud/gophercloud"

	"github.com/sapcc/limes/pkg/core"
)

type staticDiscoveryPlugin struct {
	cfg core.DiscoveryConfiguration
}

func init() {
	core.DiscoveryPluginRegistry.Add(func() core.DiscoveryPlugin { return &staticDiscoveryPlugin{} })
}

// PluginTypeID implements the core.DiscoveryPlugin interface.
func (p *staticDiscoveryPlugin) PluginTypeID() string {
	return "static"
}

// Init implements the core.DiscoveryPlugin interface.
func (p *staticDiscoveryPlugin) Init(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, cfg core.DiscoveryConfiguration) error {
	p.cfg = cfg
	return nil
}

// ListDomains implements the core.DiscoveryPlugin interface.
func (p *staticDiscoveryPlugin) ListDomains(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) ([]core.KeystoneDomain, error) {
	var result []core.KeystoneDomain
	if len(p.cfg.Static.Domains) == 0 {
		return nil, errors.New("no domains configured")
	}
	for _, domain := range p.cfg.Static.Domains {
		if domain.UUID == "" {
			return nil, fmt.Errorf("missing ID for preconfigured domain %q", domain.Name)
		}
		if domain.Name == "" {
			return nil, fmt.Errorf("missing name for preconfigured domain %q", domain.UUID)
		}
		result = append(result, core.KeystoneDomain{
			UUID: domain.UUID,
			Name: domain.Name,
		})
	}
	return result, nil
}

// ListProjects implements the core.DiscoveryPlugin interface.
func (p *staticDiscoveryPlugin) ListProjects(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, queryDomain core.KeystoneDomain) ([]core.KeystoneProject, error) {
	var result []core.KeystoneProject
	if len(p.cfg.Static.Domains) == 0 {
		return nil, errors.New("no domains configured")
	}
	for _, domain := range p.cfg.Static.Domains {
		if domain.UUID == queryDomain.UUID {
			if len(domain.Projects) == 0 {
				return nil, fmt.Errorf("no projects configured for domain %s", queryDomain.UUID)
			}
			for _, project := range domain.Projects {
				if project.UUID == "" {
					return nil, fmt.Errorf("missing ID for preconfigured project %q", project.Name)
				}
				if project.Name == "" {
					return nil, fmt.Errorf("missing name for preconfigured project %q", project.UUID)
				}
				if project.ParentUUID == "" {
					return nil, fmt.Errorf("missing parent_id for preconfigured project %q", project.UUID)
				}
				result = append(result, core.KeystoneProject{
					UUID:       project.UUID,
					Name:       project.Name,
					ParentUUID: project.ParentUUID,
					Domain:     queryDomain,
				})
			}
		}
	}
	return result, nil
}
