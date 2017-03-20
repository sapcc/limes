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
			limes.KeystoneDomain{Name: "Default", UUID: "2131d24fee484da9be8671aa276360e0"},
			limes.KeystoneDomain{Name: "Example", UUID: "a2f0d9a6a8a0410f9881335f1fe0b538"},
		},
		StaticProjects: map[string][]limes.KeystoneProject{
			"2131d24fee484da9be8671aa276360e0": []limes.KeystoneProject{
				limes.KeystoneProject{Name: "foo", UUID: "dd53fc9c38d740c6b7889424e740e194"},
				limes.KeystoneProject{Name: "bar", UUID: "003645ff7b534b8ab612885ff7653526"},
			},
			"a2f0d9a6a8a0410f9881335f1fe0b538": []limes.KeystoneProject{
				limes.KeystoneProject{Name: "qux", UUID: "ed5867497beb40c69f829837639d873d"},
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

//CheckUserPermission implements the limes.Driver interface.
func (d *Driver) CheckUserPermission(token, rule string, enforcer *policy.Enforcer, requestParams map[string]string) (bool, error) {
	return true, nil
}
