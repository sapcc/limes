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

package api

import (
	"net/http"

	policy "github.com/databus23/goslo.policy"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gorilla/mux"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/logg"
)

//CheckToken checks the validity of the request's X-Auth-Token in Keystone, and
//returns a Token instance for checking authorization. Any errors that occur
//during this function are deferred until Require() is called.
func (p *v1Provider) CheckToken(r *http.Request) *gopherpolicy.Token {
	//special case for unit tests
	auth := p.Cluster.Config.Auth
	if auth.AuthURL == "" {
		return &gopherpolicy.Token{
			Enforcer: p.Config.API.PolicyEnforcer,
			Context:  policy.Context{},
		}
	}

	client, err := openstack.NewIdentityV3(auth.ProviderClient, auth.EndpointOpts)
	if err != nil {
		return &gopherpolicy.Token{Err: err}
	}

	validator := gopherpolicy.TokenValidator{
		IdentityV3: client,
		Enforcer:   p.Config.API.PolicyEnforcer,
	}
	t := validator.CheckToken(r)
	t.Context.Logger = logg.Debug
	t.Context.Request = mux.Vars(r)

	//provide the cluster ID to the policy (this can be used e.g. to restrict
	//cloud-admin operations to a certain cluster)
	if t.Context.Auth != nil {
		t.Context.Auth["cluster_id"] = p.Cluster.ID
	}

	return t
}
