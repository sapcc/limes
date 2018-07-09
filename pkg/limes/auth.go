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

package limes

import (
	"fmt"
	"net/http"
	"sync"

	policy "github.com/databus23/goslo.policy"
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/tokens"
	"github.com/sapcc/limes/pkg/util"
)

//AuthParameters contains credentials for authenticating with Keystone (i.e.
//everything that's needed to set up a gophercloud.ProviderClient instance).
type AuthParameters struct {
	AuthURL           string      `yaml:"auth_url"`
	UserName          string      `yaml:"user_name"`
	UserDomainName    string      `yaml:"user_domain_name"`
	ProjectName       string      `yaml:"project_name"`
	ProjectDomainName string      `yaml:"project_domain_name"`
	Password          string      `yaml:"password"`
	RegionName        string      `yaml:"region_name"`
	tokenRenewalMutex *sync.Mutex `yaml:"-"`
	//ProviderClient is only valid after calling Connect().
	ProviderClient *gophercloud.ProviderClient `yaml:"-"`
}

//Connect creates the gophercloud.ProviderClient instance for these credentials.
func (auth *AuthParameters) Connect() error {
	if auth.tokenRenewalMutex == nil {
		auth.tokenRenewalMutex = &sync.Mutex{}
	}

	if auth.ProviderClient != nil {
		//already done
		return nil
	}

	var err error
	auth.ProviderClient, err = openstack.NewClient(auth.AuthURL)
	if err != nil {
		return fmt.Errorf("cannot initialize OpenStack client: %v", err)
	}

	//use http.DefaultClient, esp. to pick up LIMES_INSECURE flag
	auth.ProviderClient.HTTPClient = *http.DefaultClient

	err = openstack.Authenticate(auth.ProviderClient, gophercloud.AuthOptions{
		IdentityEndpoint: auth.AuthURL,
		AllowReauth:      true,
		Username:         auth.UserName,
		DomainName:       auth.UserDomainName,
		Password:         auth.Password,
		Scope: &gophercloud.AuthScope{
			ProjectName: auth.ProjectName,
			DomainName:  auth.ProjectDomainName,
		},
	})
	//FIXME: honor auth.RegionName
	if err != nil {
		return fmt.Errorf("cannot fetch initial Keystone token: %v", err)
	}

	return nil
}

//ValidateToken validates the given Keystone token and returns a policy context for
//checking authorization.
func (auth *AuthParameters) ValidateToken(token string) (policy.Context, error) {
	//special case for unit tests
	if auth.AuthURL == "" {
		return policy.Context{}, nil
	}

	client, err := openstack.NewIdentityV3(auth.ProviderClient,
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
	if err != nil {
		return policy.Context{}, err
	}

	response := tokens.Get(client, token)
	if response.Err != nil {
		//this includes 4xx responses, so after this point, we can be sure that the token is valid
		return policy.Context{}, response.Err
	}

	//use a custom token struct instead of tokens.Token which is way incomplete
	var tokenData keystoneToken
	err = response.ExtractInto(&tokenData)
	if err != nil {
		return policy.Context{}, err
	}
	return tokenData.ToContext(), nil
}

type keystoneToken struct {
	DomainScope  keystoneTokenThing         `json:"domain"`
	ProjectScope keystoneTokenThingInDomain `json:"project"`
	Roles        []keystoneTokenThing       `json:"roles"`
	User         keystoneTokenThingInDomain `json:"user"`
}

type keystoneTokenThing struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type keystoneTokenThingInDomain struct {
	keystoneTokenThing
	Domain keystoneTokenThing `json:"domain"`
}

func (t *keystoneToken) ToContext() policy.Context {
	c := policy.Context{
		Roles: make([]string, 0, len(t.Roles)),
		Auth: map[string]string{
			"user_id":             t.User.ID,
			"user_name":           t.User.Name,
			"user_domain_id":      t.User.Domain.ID,
			"user_domain_name":    t.User.Domain.Name,
			"domain_id":           t.DomainScope.ID,
			"domain_name":         t.DomainScope.Name,
			"project_id":          t.ProjectScope.ID,
			"project_name":        t.ProjectScope.Name,
			"project_domain_id":   t.ProjectScope.Domain.ID,
			"project_domain_name": t.ProjectScope.Domain.Name,
			"tenant_id":           t.ProjectScope.ID,
			"tenant_name":         t.ProjectScope.Name,
			"tenant_domain_id":    t.ProjectScope.Domain.ID,
			"tenant_domain_name":  t.ProjectScope.Domain.Name,
		},
		Request: nil,
		Logger:  util.LogDebug,
	}
	for key, value := range c.Auth {
		if value == "" {
			delete(c.Auth, key)
		}
	}
	for _, role := range t.Roles {
		c.Roles = append(c.Roles, role.Name)
	}
	if c.Request == nil {
		c.Request = map[string]string{}
	}

	return c
}
