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

package test

import (
	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/limes/pkg/core"
)

//DiscoveryPlugin is a core.DiscoveryPlugin implementation for unit tests that
//reports a static set of domains and projects.
type DiscoveryPlugin struct {
	StaticDomains  []core.KeystoneDomain
	StaticProjects map[string][]core.KeystoneProject
}

//NewDiscoveryPlugin creates a DiscoveryPlugin instance.
func NewDiscoveryPlugin() *DiscoveryPlugin {
	return &DiscoveryPlugin{
		StaticDomains: []core.KeystoneDomain{
			{Name: "germany", UUID: "uuid-for-germany"},
			{Name: "france", UUID: "uuid-for-france"},
		},
		StaticProjects: map[string][]core.KeystoneProject{
			"uuid-for-germany": {
				{Name: "berlin", UUID: "uuid-for-berlin", ParentUUID: "uuid-for-germany"},
				{Name: "dresden", UUID: "uuid-for-dresden", ParentUUID: "uuid-for-berlin"},
			},
			"uuid-for-france": {
				{Name: "paris", UUID: "uuid-for-paris", ParentUUID: "uuid-for-france"},
			},
		},
	}
}

//Method implements the core.DiscoveryPlugin interface.
func (p *DiscoveryPlugin) Method() string {
	return "unittest"
}

//ListDomains implements the core.DiscoveryPlugin interface.
func (p *DiscoveryPlugin) ListDomains(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) ([]core.KeystoneDomain, error) {
	return p.StaticDomains, nil
}

//ListProjects implements the core.DiscoveryPlugin interface.
func (p *DiscoveryPlugin) ListProjects(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, domainUUID string) ([]core.KeystoneProject, error) {
	return p.StaticProjects[domainUUID], nil
}
