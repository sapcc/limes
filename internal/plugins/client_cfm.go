/*******************************************************************************
*
* Copyright 2018 SAP SE
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

package plugins

import (
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/identity/v3/tokens"
	"github.com/sapcc/go-bits/logg"
)

type cfmClient struct {
	*gophercloud.ServiceClient
	projectID string
}

func getProjectIDForToken(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (string, error) {
	//The CFM API is stupid and needs the caller to provide the scope of the
	//token redundantly in the X-Project-Id header.

	//try to use the new AuthResult API to get the token without extra HTTP requests
	if result, ok := provider.GetAuthResult().(tokens.CreateResult); ok {
		project, err := result.ExtractProject()
		if err == nil {
			return project.ID, nil
		}
	}

	//fallback: validate our own token to get its metadata
	logg.Info("using fallback strategy for getProjectIDForToken")
	token := provider.Token()
	identityClient, err := openstack.NewIdentityV3(provider, eo)
	if err != nil {
		return "", err
	}
	project, err := tokens.Get(identityClient, token).ExtractProject()
	if err != nil {
		return "", err
	}
	return project.ID, nil
}

func newCFMClient(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, projectID string) (*cfmClient, error) {
	serviceType := "database"
	eoCFM := eo
	eoCFM.ApplyDefaults(serviceType)

	url, err := provider.EndpointLocator(eoCFM)
	if err != nil {
		return nil, err
	}

	return &cfmClient{
		ServiceClient: &gophercloud.ServiceClient{
			ProviderClient: provider,
			Endpoint:       url,
			Type:           serviceType,
		},
		projectID: projectID,
	}, nil
}

func (c *cfmClient) reqOpts(okCodes ...int) *gophercloud.RequestOpts {
	return &gophercloud.RequestOpts{
		OkCodes: okCodes,
		MoreHeaders: map[string]string{
			"X-Auth-Token":  "",
			"Authorization": "Token " + c.ProviderClient.Token(),
			"X-Project-ID":  c.projectID,
		},
		ErrorContext: cfmNotFoundError{},
	}
}

////////////////////////////////////////////////////////////////////////////////
// new-style quota/usage API

func (c *cfmClient) GetQuotaSet(projectID string) (result gophercloud.Result) {
	url := c.ServiceURL("v1.0", "quota-sets", projectID) + "/"
	_, result.Err = c.Get(url, &result.Body, c.reqOpts(200)) //nolint:bodyclose // already closed by gophercloud
	return
}

func (c *cfmClient) CreateQuotaSet(projectID string, quotaBytes uint64) error {
	body := struct {
		StorageQuota struct {
			ProjectID string `json:"project_id"`
			SizeLimit uint64 `json:"size_limit"`
		} `json:"storage_quota"`
	}{}
	body.StorageQuota.ProjectID = projectID
	body.StorageQuota.SizeLimit = quotaBytes
	url := c.ServiceURL("v1.0", "quota-sets") + "/"
	_, err := c.Post(url, body, nil, c.reqOpts(202)) //nolint:bodyclose // already closed by gophercloud
	return err
}

// UpdateQuotaSet may return cfmNotFoundError.
func (c *cfmClient) UpdateQuotaSet(projectID string, quotaBytes uint64) error {
	body := struct {
		StorageQuota struct {
			SizeLimit uint64 `json:"size_limit"`
		} `json:"storage_quota"`
	}{}
	body.StorageQuota.SizeLimit = quotaBytes
	url := c.ServiceURL("v1.0", "quota-sets", projectID) + "/"
	_, err := c.Put(url, body, nil, c.reqOpts(200)) //nolint:bodyclose // already closed by gophercloud
	return err
}

// An error type that can be used in gophercloud's weird ErrorContext interface.
type cfmNotFoundError struct{}

// Error implements the builtin/error interface.
func (cfmNotFoundError) Error() string {
	return "not found"
}

// Error404 implements the gophercloud.Err404er interface.
func (cfmNotFoundError) Error404(gophercloud.ErrUnexpectedResponseCode) error {
	return cfmNotFoundError{}
}

////////////////////////////////////////////////////////////////////////////////
// capacity API

type cfmPool struct {
	HostName     string `json:"host"`
	Name         string `json:"name"`
	Type         string `json:"pool"`
	Capabilities struct {
		TotalCapacityBytes uint64 `json:"total_capacity"`
	} `json:"capabilities"`
}

func (c *cfmClient) ListPools() ([]cfmPool, error) {
	url := c.ServiceURL("v1.0", "scheduler-stats", "pools", "detail") + "/"
	var result gophercloud.Result
	_, err := c.Get(url, &result.Body, c.reqOpts(200)) //nolint:bodyclose // already closed by gophercloud
	if err != nil {
		return nil, err
	}

	var data struct {
		Pools []cfmPool `json:"pools"`
	}
	err = result.ExtractInto(&data)
	return data.Pools, err
}
