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
	policy "github.com/databus23/goslo.policy"
	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/limes/pkg/limes"
)

//Driver is a limes.Driver implementation for unit tests that does not talk to
//an actual OpenStack. It returns a static set of domains and projects, and a
//static set of quota/usage values for any project.
type Driver struct {
	ClusterConfig  *limes.ClusterConfiguration
	StaticDomains  []limes.KeystoneDomain
	StaticProjects map[string][]limes.KeystoneProject
}

//NewDriver creates a Driver instance. The ClusterConfiguration does not need
//to have the Keystone auth params fields filled. Only the cluster ID and
//service list are required.
func NewDriver(cluster *limes.ClusterConfiguration) *Driver {
	return &Driver{
		ClusterConfig: cluster,
		StaticDomains: []limes.KeystoneDomain{
			{Name: "germany", UUID: "uuid-for-germany"},
			{Name: "france", UUID: "uuid-for-france"},
		},
		StaticProjects: map[string][]limes.KeystoneProject{
			"uuid-for-germany": {
				{Name: "berlin", UUID: "uuid-for-berlin"},
				{Name: "dresden", UUID: "uuid-for-dresden"},
			},
			"uuid-for-france": {
				{Name: "paris", UUID: "uuid-for-paris"},
			},
		},
	}
}

//Cluster implements the limes.Driver interface.
func (d *Driver) Cluster() *limes.ClusterConfiguration {
	return d.ClusterConfig
}

//Client implements the limes.Driver interface.
func (d *Driver) Client() *gophercloud.ProviderClient {
	return nil
}

//ListDomains implements the limes.Driver interface.
func (d *Driver) ListDomains() ([]limes.KeystoneDomain, error) {
	return d.StaticDomains, nil
}

//ListProjects implements the limes.Driver interface.
func (d *Driver) ListProjects(domainUUID string) ([]limes.KeystoneProject, error) {
	return d.StaticProjects[domainUUID], nil
}

//ValidateToken implements the limes.Driver interface.
func (d *Driver) ValidateToken(token string) (policy.Context, error) {
	return policy.Context{}, nil
}
