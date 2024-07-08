/*******************************************************************************
*
* Copyright 2017-2023 SAP SE
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
	"github.com/gophercloud/gophercloud/v2"

	"github.com/sapcc/limes/internal/core"
)

func init() {
	core.DiscoveryPluginRegistry.Add(func() core.DiscoveryPlugin { return &StaticDiscoveryPlugin{} })
}

// StaticDiscoveryPlugin is a core.DiscoveryPlugin implementation for unit tests.
// It reports a static set of domains and projects.
type StaticDiscoveryPlugin struct {
	Domains  []core.KeystoneDomain             `yaml:"domains"`
	Projects map[string][]core.KeystoneProject `yaml:"projects"`
}

// PluginTypeID implements the core.DiscoveryPlugin interface.
func (p *StaticDiscoveryPlugin) PluginTypeID() string {
	return "--test-static"
}

// Init implements the core.DiscoveryPlugin interface.
func (p *StaticDiscoveryPlugin) Init(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	// apply default set of domains and projects
	if len(p.Domains) == 0 && len(p.Projects) == 0 {
		p.Domains = []core.KeystoneDomain{
			{Name: "germany", UUID: "uuid-for-germany"},
			{Name: "france", UUID: "uuid-for-france"},
		}
		p.Projects = map[string][]core.KeystoneProject{
			"uuid-for-germany": {
				{Name: "berlin", UUID: "uuid-for-berlin", ParentUUID: "uuid-for-germany"},
				{Name: "dresden", UUID: "uuid-for-dresden", ParentUUID: "uuid-for-berlin"},
			},
			"uuid-for-france": {
				{Name: "paris", UUID: "uuid-for-paris", ParentUUID: "uuid-for-france"},
			},
		}
	}
	return nil
}

// ListDomains implements the core.DiscoveryPlugin interface.
func (p *StaticDiscoveryPlugin) ListDomains() ([]core.KeystoneDomain, error) {
	return p.Domains, nil
}

// ListProjects implements the core.DiscoveryPlugin interface.
func (p *StaticDiscoveryPlugin) ListProjects(domain core.KeystoneDomain) ([]core.KeystoneProject, error) {
	// the domain is not duplicated in each Projects entry, so it must be added now
	result := make([]core.KeystoneProject, len(p.Projects[domain.UUID]))
	for idx, project := range p.Projects[domain.UUID] {
		result[idx] = project
		result[idx].Domain = domain
	}
	return result, nil
}
