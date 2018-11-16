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

func newCFMClient(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (*cfmClient, error) {
	serviceType := "database"
	eoCFM := eo
	eoCFM.ApplyDefaults(serviceType)

	url, err := provider.EndpointLocator(eoCFM)
	if err != nil {
		return nil, err
	}

	//the CFM API is stupid and needs the caller to provide the scope of the
	//token redundantly in the X-Project-Id header
	identityClient, err := openstack.NewIdentityV3(provider, eo)
	if err != nil {
		return nil, err
	}
	project, err := tokens.Get(identityClient, provider.Token()).ExtractProject()
	if err != nil {
		return nil, err
	}

	return &cfmClient{
		ServiceClient: &gophercloud.ServiceClient{
			ProviderClient: provider,
			Endpoint:       url,
			Type:           serviceType,
		},
		projectID: project.ID,
	}, nil
}

func (c *cfmClient) reqOpts(okCodes ...int) *gophercloud.RequestOpts {
	return &gophercloud.RequestOpts{
		OkCodes: []int{200},
		MoreHeaders: map[string]string{
			"X-Auth-Token":  "",
			"Authorization": "Token " + c.ProviderClient.Token(),
			"X-Project-ID":  c.projectID,
		},
	}
}

////////////////////////////////////////////////////////////////////////////////
// old-style usage API

type cfmShareserver struct {
	Type        string
	ProjectUUID string
	DetailsURL  string
	//fields that are only filled by cfmGetShareserver, not by cfmListShareservers
	BytesUsed uint64
}

func (c *cfmClient) ListShareservers() ([]cfmShareserver, error) {
	url := c.ServiceURL("v1.0", "shareservers") + "/"
	var result gophercloud.Result
	_, err := c.Get(url, &result.Body, c.reqOpts(200))
	if err != nil {
		return nil, err
	}

	var data struct {
		Shareservers []struct {
			ID          string `json:"id"`
			Type        string `json:"type"`
			ProjectUUID string `json:"customer_id"`
		} `json:"shareservers"`
	}
	err = result.ExtractInto(&data)
	if err != nil {
		return nil, err
	}

	list := make([]cfmShareserver, len(data.Shareservers))
	for idx, srv := range data.Shareservers {
		list[idx] = cfmShareserver{
			Type:        srv.Type,
			ProjectUUID: srv.ProjectUUID,
			DetailsURL:  url + srv.ID + "/",
		}
	}
	return list, nil
}

func (c *cfmClient) GetShareserver(url string) (*cfmShareserver, error) {
	var result gophercloud.Result
	_, err := c.Get(url, &result.Body, c.reqOpts(200))
	if err != nil {
		return nil, err
	}

	var data struct {
		Shareserver struct {
			ID         string `json:"id"`
			Properties struct {
				SVMs []struct {
					Volumes []struct {
						Space struct {
							BytesUsed uint64 `json:"size"`
						} `json:"space"`
					} `json:"volumes"`
				} `json:"svms"`
			} `json:"properties"`
			Type        string `json:"type"`
			ProjectUUID string `json:"customer_id"`
		} `json:"shareserver"`
	}
	err = result.ExtractInto(&data)
	if err != nil {
		return nil, err
	}

	srv := data.Shareserver

	var totalBytesUsed uint64
	for _, svm := range srv.Properties.SVMs {
		for _, volume := range svm.Volumes {
			totalBytesUsed += volume.Space.BytesUsed
		}
	}

	logg.Info("CFM shareserver %s (type %s) in project %s has size = %d bytes",
		srv.ID, srv.Type, srv.ProjectUUID,
		totalBytesUsed,
	)
	return &cfmShareserver{
		Type:        srv.Type,
		ProjectUUID: srv.ProjectUUID,
		DetailsURL:  url + srv.ID + "/",
		BytesUsed:   totalBytesUsed,
	}, nil
}

////////////////////////////////////////////////////////////////////////////////
// capacity API

type cfmPool struct {
	Capabilities struct {
		TotalCapacityBytes uint64 `json:"total_capacity"`
	} `json:"capabilities"`
}

func (c *cfmClient) ListPools() ([]cfmPool, error) {
	url := c.ServiceURL("v1.0", "scheduler-stats", "pools", "detail") + "/"
	var result gophercloud.Result
	_, err := c.Get(url, &result.Body, c.reqOpts(200))
	if err != nil {
		return nil, err
	}

	var data struct {
		Pools []cfmPool `json:"pools"`
	}
	err = result.ExtractInto(&data)
	return data.Pools, err
}
