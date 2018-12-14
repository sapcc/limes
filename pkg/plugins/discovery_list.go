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
	"github.com/sapcc/limes/pkg/core"
)

type listDiscoveryPlugin struct {
	cfg core.DiscoveryConfiguration
}

func init() {
	core.RegisterDiscoveryPlugin(func(c core.DiscoveryConfiguration) core.DiscoveryPlugin {
		return &listDiscoveryPlugin{c}
	})
}

//Method implements the core.DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) Method() string {
	return "list"
}

//ListDomains implements the core.DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) ListDomains(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) ([]core.KeystoneDomain, error) {
	client, err := openstack.NewIdentityV3(provider, eo)
	if err != nil {
		return nil, err
	}

	//gophercloud does not support domain listing yet - do it manually
	url := client.ServiceURL("domains")
	var result gophercloud.Result
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	var data struct {
		Domains []core.KeystoneDomain `json:"domains"`
	}
	err = result.ExtractInto(&data)
	return data.Domains, err
}

//ListProjects implements the core.DiscoveryPlugin interface.
func (p *listDiscoveryPlugin) ListProjects(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, domainUUID string) ([]core.KeystoneProject, error) {
	client, err := openstack.NewIdentityV3(provider, eo)
	if err != nil {
		return nil, err
	}

	//gophercloud does not support project listing yet - do it manually
	url := client.ServiceURL("projects")
	var opts struct {
		DomainUUID string `q:"domain_id"`
	}
	opts.DomainUUID = domainUUID
	query, err := gophercloud.BuildQueryString(opts)
	if err != nil {
		return nil, err
	}
	url += query.String()

	var result gophercloud.Result
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	var data struct {
		Projects []core.KeystoneProject `json:"projects"`
	}
	err = result.ExtractInto(&data)
	return data.Projects, err
}
