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
	"os"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/utils/openstack/clientconfig"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/osext"
)

// AuthSession contains the gophercloud authentication things.
type AuthSession struct {
	ProviderClient *gophercloud.ProviderClient
	EndpointOpts   gophercloud.EndpointOpts
	TokenValidator gopherpolicy.Validator
}

// Connect creates the gophercloud.ProviderClient instance for these credentials.
func AuthToOpenstack() (*AuthSession, error) {
	//initialize OpenStack connection
	ao, err := clientconfig.AuthOptions(nil)
	if err != nil {
		logg.Fatal("cannot find OpenStack credentials: " + err.Error())
	}
	ao.AllowReauth = true
	provider, err := openstack.AuthenticatedClient(*ao)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize OpenStack client: %w", err)
	}

	eo := gophercloud.EndpointOpts{
		Availability: gophercloud.Availability(os.Getenv("OS_INTERFACE")),
		Region:       os.Getenv("OS_REGION_NAME"),
	}

	identityV3, err := openstack.NewIdentityV3(provider, eo)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize Keystone v3 client: %w", err)
	}
	tv := gopherpolicy.TokenValidator{
		IdentityV3: identityV3,
		Cacher:     gopherpolicy.InMemoryCacher(),
	}
	err = tv.LoadPolicyFile(osext.GetenvOrDefault("LIMES_API_POLICY_PATH", "/etc/limes/policy.yaml"))
	if err != nil {
		return nil, err
	}

	return &AuthSession{
		ProviderClient: provider,
		EndpointOpts:   eo,
		TokenValidator: &tv,
	}, nil
}