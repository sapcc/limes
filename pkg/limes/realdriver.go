/*******************************************************************************
*
* Copyright 2017 Stefan Majewsky <majewsky@gmx.net>
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
	"sync"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/tokens"
	"github.com/sapcc/limes/pkg/util"
)

//This is the type that implements the Driver interface by actually
//calling out to OpenStack. It also manages the Keystone token that's required
//for all access to OpenStack APIs. The interface implementations are in the
//other source files in this module.
type realDriver struct {
	ProviderClient    *gophercloud.ProviderClient
	cluster           *Cluster //need to use lowercase "cluster" because "Cluster" is already a method
	TokenRenewalMutex *sync.Mutex
}

//NewDriver instantiates a Driver for the given Cluster.
func NewDriver(cfg *Cluster) (Driver, error) {
	var err error
	d := &realDriver{
		cluster:           cfg,
		TokenRenewalMutex: &sync.Mutex{},
	}

	//initialize the OpenStack client
	d.ProviderClient, err = openstack.NewClient(d.cluster.Config.AuthURL)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize OpenStack client: %v", err)
	}
	err = d.RefreshToken()
	if err != nil {
		return nil, fmt.Errorf("cannot fetch initial Keystone token: %v", err)
	}
	return d, nil
}

//Cluster implements the Driver interface.
func (d *realDriver) Cluster() *Cluster {
	return d.cluster
}

//Client implements the Driver interface.
func (d *realDriver) Client() *gophercloud.ProviderClient {
	return d.ProviderClient
}

//RefreshToken fetches a new Keystone token for this cluster. It is also used
//to fetch the initial token on startup.
func (d *realDriver) RefreshToken() error {
	//NOTE: This function is very similar to v3auth() in
	//gophercloud/openstack/client.go, but with a few differences:
	//
	//1. thread-safe token renewal
	//2. proper support for cross-domain scoping

	d.TokenRenewalMutex.Lock()
	defer d.TokenRenewalMutex.Unlock()
	util.LogDebug("renewing Keystone token...")

	d.ProviderClient.TokenID = ""

	//TODO: crashes with RegionName != ""
	eo := gophercloud.EndpointOpts{Region: d.cluster.Config.RegionName}
	keystone, err := openstack.NewIdentityV3(d.ProviderClient, eo)
	if err != nil {
		return fmt.Errorf("cannot initialize Keystone client: %v", err)
	}
	keystone.Endpoint = d.cluster.Config.AuthURL

	result := tokens.Create(keystone, &d.cluster.Config)
	token, err := result.ExtractToken()
	if err != nil {
		return fmt.Errorf("cannot read token: %v", err)
	}
	catalog, err := result.ExtractServiceCatalog()
	if err != nil {
		return fmt.Errorf("cannot read service catalog: %v", err)
	}

	d.ProviderClient.TokenID = token.ID
	d.ProviderClient.ReauthFunc = d.RefreshToken //TODO: exponential backoff necessary or already provided by gophercloud?
	d.ProviderClient.EndpointLocator = func(opts gophercloud.EndpointOpts) (string, error) {
		return openstack.V3EndpointURL(catalog, opts)
	}

	return nil
}
