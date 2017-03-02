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
	"sync"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/tokens"
)

//Cluster manages the connection to one of the OpenStack clusters defined in the Limes configuration.
type Cluster struct {
	ID                string
	Client            *gophercloud.ProviderClient
	config            *ConfigurationEntryCluster
	tokenRenewalMutex *sync.Mutex
}

//NewCluster initializes a Cluster struct.
func NewCluster(cfg Configuration, clusterID string) (*Cluster, error) {
	var (
		c   Cluster
		ok  bool
		err error
	)

	c.ID = clusterID
	c.tokenRenewalMutex = &sync.Mutex{}

	//find the configuration for this cluster
	c.config, ok = cfg.Clusters[clusterID]
	if !ok {
		return nil, fmt.Errorf("no such cluster configured: %s", clusterID)
	}

	//initialize the OpenStack client
	c.Client, err = openstack.NewClient(c.config.AuthURL)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize OpenStack client: %v", err)
	}
	err = c.RefreshToken()
	if err != nil {
		return nil, fmt.Errorf("cannot fetch initial Keystone token: %v", err)
	}
	return &c, nil
}

//RefreshToken fetches a new Keystone token for this cluster. It is also used
//to fetch the initial token on startup.
func (c *Cluster) RefreshToken() error {
	//NOTE: This function is very similar to v3auth() in
	//gophercloud/openstack/client.go, but with a few differences:
	//
	//1. thread-safe token renewal
	//2. proper support for cross-domain scoping

	c.tokenRenewalMutex.Lock()
	defer c.tokenRenewalMutex.Unlock()

	c.Client.TokenID = ""

	//TODO: crashes with RegionName != ""
	eo := gophercloud.EndpointOpts{Region: c.config.RegionName}
	keystone, err := openstack.NewIdentityV3(c.Client, eo)
	if err != nil {
		return fmt.Errorf("cannot initialize Keystone client: %v", err)
	}
	keystone.Endpoint = c.config.AuthURL

	result := tokens.Create(keystone, c.config)
	token, err := result.ExtractToken()
	if err != nil {
		return fmt.Errorf("cannot read token: %v", err)
	}
	catalog, err := result.ExtractServiceCatalog()
	if err != nil {
		return fmt.Errorf("cannot read service catalog: %v", err)
	}

	c.Client.TokenID = token.ID
	c.Client.ReauthFunc = c.RefreshToken //TODO: exponential backoff necessary or already provided by gophercloud?
	c.Client.EndpointLocator = func(opts gophercloud.EndpointOpts) (string, error) {
		return openstack.V3EndpointURL(catalog, opts)
	}

	return nil
}

//CanReauth implements the
//gophercloud/openstack/identity/v3/tokens.AuthOptionsBuilder interface.
func (cfg *ConfigurationEntryCluster) CanReauth() bool {
	return true
}

//ToTokenV3CreateMap implements the
//gophercloud/openstack/identity/v3/tokens.AuthOptionsBuilder interface.
func (cfg *ConfigurationEntryCluster) ToTokenV3CreateMap(scope map[string]interface{}) (map[string]interface{}, error) {
	gophercloudAuthOpts := gophercloud.AuthOptions{
		Username:    cfg.UserName,
		Password:    cfg.Password,
		DomainName:  cfg.UserDomainName,
		AllowReauth: true,
	}
	return gophercloudAuthOpts.ToTokenV3CreateMap(scope)
}

//ToTokenV3ScopeMap implements the
//gophercloud/openstack/identity/v3/tokens.AuthOptionsBuilder interface.
func (cfg *ConfigurationEntryCluster) ToTokenV3ScopeMap() (map[string]interface{}, error) {
	return map[string]interface{}{
		"project": map[string]interface{}{
			"name":   cfg.ProjectName,
			"domain": map[string]interface{}{"name": cfg.ProjectDomainName},
		},
	}, nil
}
