/******************************************************************************
*
*  Copyright 2018 Stefan Majewsky <majewsky@gmx.net>
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

/*

Package gopherschwift contains a Gophercloud backend for Schwift.

If your application uses Gophercloud (https://github.com/gophercloud/gophercloud),
you can use the Wrap() function in this package as an entrypoint to Schwift.
A schwift.Account created this way will re-use Gophercloud's authentication code,
so you only need to obtain a client token once using Gophercloud. For example:

	authOptions, err := openstack.AuthOptionsFromEnv() // or build a gophercloud.AuthOptions instance yourself
	provider, err := openstack.AuthenticatedClient(authOptions)
	client, err := openstack.NewObjectStorageV1(provider, gophercloud.EndpointOpts{})

  account, err := gopherschwift.Wrap(client)

Using this schwift.Account instance, you have access to all of schwift's API.

*/
package gopherschwift

import (
	"io"
	"io/ioutil"
	"net/http"

	"github.com/gophercloud/gophercloud"
	"github.com/majewsky/schwift"
)

//Wrap creates a schwift.Account that uses the given service client as its
//backend. The service client must refer to a Swift endpoint, i.e. it should
//have been created by openstack.NewObjectStorageV1().
func Wrap(client *gophercloud.ServiceClient) (*schwift.Account, error) {
	return schwift.InitializeAccount(&backend{client})
}

type backend struct {
	c *gophercloud.ServiceClient
}

func (g *backend) EndpointURL() string {
	return g.c.Endpoint
}

func (g *backend) Clone(newEndpointURL string) schwift.Backend {
	clonedClient := *g.c
	clonedClient.Endpoint = newEndpointURL
	return &backend{&clonedClient}
}

func (g *backend) Do(req *http.Request) (*http.Response, error) {
	return g.do(req, false)
}

func (g *backend) do(req *http.Request, afterReauth bool) (*http.Response, error) {
	provider := g.c.ProviderClient

	req.Header.Set("User-Agent", provider.UserAgent.Join())
	for key, value := range provider.AuthenticatedHeaders() {
		req.Header.Set(key, value)
	}

	resp, err := provider.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}

	//detect expired token
	if resp.StatusCode == http.StatusUnauthorized && !afterReauth {
		_, err := io.Copy(ioutil.Discard, resp.Body)
		if err != nil {
			return nil, err
		}
		err = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		err = provider.Reauthenticate(resp.Request.Header.Get("X-Auth-Token"))
		if err != nil {
			return nil, err
		}
		//restart request with new token
		return g.do(req, true)
	}

	return resp, nil
}
