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

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/tokens"
	"github.com/sapcc/limes/pkg/util"
)

//AuthParameters contains credentials for authenticating with Keystone (i.e.
//everything that's needed to set up a gophercloud.ProviderClient instance).
type AuthParameters struct {
	AuthURL           string                      `yaml:"auth_url"`
	UserName          string                      `yaml:"user_name"`
	UserDomainName    string                      `yaml:"user_domain_name"`
	ProjectName       string                      `yaml:"project_name"`
	ProjectDomainName string                      `yaml:"project_domain_name"`
	Password          string                      `yaml:"password"`
	RegionName        string                      `yaml:"region_name"`
	tokenRenewalMutex *sync.Mutex                 //initialized on first use
	providerClient    *gophercloud.ProviderClient //initialized on first use
}

//CanReauth implements the
//gophercloud/openstack/identity/v3/tokens.AuthOptionsBuilder interface.
func (auth AuthParameters) CanReauth() bool {
	return true
}

//ToTokenV3CreateMap implements the
//gophercloud/openstack/identity/v3/tokens.AuthOptionsBuilder interface.
func (auth AuthParameters) ToTokenV3CreateMap(scope map[string]interface{}) (map[string]interface{}, error) {
	gophercloudAuthOpts := gophercloud.AuthOptions{
		Username:    auth.UserName,
		Password:    auth.Password,
		DomainName:  auth.UserDomainName,
		AllowReauth: true,
	}
	return gophercloudAuthOpts.ToTokenV3CreateMap(scope)
}

//ToTokenV3ScopeMap implements the
//gophercloud/openstack/identity/v3/tokens.AuthOptionsBuilder interface.
func (auth AuthParameters) ToTokenV3ScopeMap() (map[string]interface{}, error) {
	return map[string]interface{}{
		"project": map[string]interface{}{
			"name":   auth.ProjectName,
			"domain": map[string]interface{}{"name": auth.ProjectDomainName},
		},
	}, nil
}

//ProviderClient returns an instance of gophercloud.ProviderClient using these
//credentials, or creates one if called for the first time.
func (auth *AuthParameters) ProviderClient() (*gophercloud.ProviderClient, error) {
	if auth.tokenRenewalMutex == nil {
		auth.tokenRenewalMutex = &sync.Mutex{}
	}

	if auth.providerClient != nil {
		var err error
		auth.providerClient, err = openstack.NewClient(auth.AuthURL)
		if err != nil {
			return nil, fmt.Errorf("cannot initialize OpenStack client: %v", err)
		}
		//use http.DefaultClient, esp. to pick up LIMES_INSECURE flag
		auth.providerClient.HTTPClient = *http.DefaultClient

		err = auth.refreshToken()
		if err != nil {
			return nil, fmt.Errorf("cannot fetch initial Keystone token: %v", err)
		}

		//TODO: auth.providerClient.UserAgent.Prepend("limes/%s", versionString)
	}

	return auth.providerClient, nil
}

//refreshToken fetches a new Keystone token for this cluster. It is also used
//to fetch the initial token on startup.
func (auth *AuthParameters) refreshToken() error {
	//NOTE: This function is very similar to v3auth() in
	//gophercloud/openstack/client.go, but with a few differences:
	//
	//1. thread-safe token renewal
	//2. proper support for cross-domain scoping

	auth.tokenRenewalMutex.Lock()
	defer auth.tokenRenewalMutex.Unlock()
	util.LogDebug("renewing Keystone token...")

	auth.providerClient.TokenID = ""

	//TODO: crashes with RegionName != ""
	eo := gophercloud.EndpointOpts{Region: auth.RegionName}
	keystone, err := openstack.NewIdentityV3(auth.providerClient, eo)
	if err != nil {
		return fmt.Errorf("cannot initialize Keystone client: %v", err)
	}
	keystone.Endpoint = auth.AuthURL

	result := tokens.Create(keystone, auth)
	token, err := result.ExtractToken()
	if err != nil {
		return fmt.Errorf("cannot read token: %v", err)
	}
	catalog, err := result.ExtractServiceCatalog()
	if err != nil {
		return fmt.Errorf("cannot read service catalog: %v", err)
	}

	auth.providerClient.TokenID = token.ID
	auth.providerClient.ReauthFunc = auth.refreshToken //TODO: exponential backoff necessary or already provided by gophercloud?
	auth.providerClient.EndpointLocator = func(opts gophercloud.EndpointOpts) (string, error) {
		return openstack.V3EndpointURL(catalog, opts)
	}

	return nil
}
