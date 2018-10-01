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
	//The following fields are only valid after calling Connect().
	ProviderClient *gophercloud.ProviderClient `yaml:"-"`
	EndpointOpts   gophercloud.EndpointOpts    `yaml:"-"`
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
	if err != nil {
		return fmt.Errorf("cannot fetch initial Keystone token: %v", err)
	}

	auth.EndpointOpts = gophercloud.EndpointOpts{
		Availability: gophercloud.AvailabilityPublic,
		Region:       auth.RegionName,
	}
	return nil
}
