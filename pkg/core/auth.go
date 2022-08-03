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

package core

import (
	"fmt"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/sapcc/go-api-declarations/bininfo"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/osext"
	"github.com/sapcc/go-bits/secrets"
)

// AuthParameters contains credentials for authenticating with Keystone (i.e.
// everything that's needed to set up a gophercloud.ProviderClient instance).
type AuthParameters struct {
	AuthURL           string               `yaml:"auth_url"`
	UserName          string               `yaml:"user_name"`
	UserDomainName    string               `yaml:"user_domain_name"`
	ProjectName       string               `yaml:"project_name"`
	ProjectDomainName string               `yaml:"project_domain_name"`
	Password          secrets.AuthPassword `yaml:"password"`
	RegionName        string               `yaml:"region_name"`
	Interface         string               `yaml:"interface"`
	//The following fields are only valid after calling Connect().
	ProviderClient *gophercloud.ProviderClient `yaml:"-"`
	EndpointOpts   gophercloud.EndpointOpts    `yaml:"-"`
	TokenValidator gopherpolicy.Validator      `yaml:"-"`
}

// Connect creates the gophercloud.ProviderClient instance for these credentials.
func (auth *AuthParameters) Connect() error {
	if auth.ProviderClient != nil {
		//already done
		return nil
	}

	var err error
	auth.ProviderClient, err = openstack.NewClient(auth.AuthURL)
	if err != nil {
		return fmt.Errorf("cannot initialize OpenStack client: %w", err)
	}

	userAgent := fmt.Sprintf("%s@%s", bininfo.Component(), bininfo.VersionOr("rolling"))
	auth.ProviderClient.UserAgent.Prepend(userAgent)

	err = openstack.Authenticate(auth.ProviderClient, gophercloud.AuthOptions{
		IdentityEndpoint: auth.AuthURL,
		AllowReauth:      true,
		Username:         auth.UserName,
		DomainName:       auth.UserDomainName,
		Password:         string(auth.Password),
		Scope: &gophercloud.AuthScope{
			ProjectName: auth.ProjectName,
			DomainName:  auth.ProjectDomainName,
		},
	})
	if err != nil {
		return fmt.Errorf("cannot fetch initial Keystone token: %w", err)
	}

	auth.EndpointOpts = gophercloud.EndpointOpts{
		Availability: gophercloud.Availability(auth.Interface),
		Region:       auth.RegionName,
	}

	identityV3, err := openstack.NewIdentityV3(auth.ProviderClient, auth.EndpointOpts)
	if err != nil {
		return fmt.Errorf("cannot initialize Keystone v3 client: %w", err)
	}
	tv := gopherpolicy.TokenValidator{
		IdentityV3: identityV3,
		Cacher:     gopherpolicy.InMemoryCacher(),
	}
	err = tv.LoadPolicyFile(osext.GetenvOrDefault("LIMES_API_POLICY_PATH", "/etc/limes/policy.yaml"))
	if err != nil {
		return err
	}
	auth.TokenValidator = &tv

	return nil
}
