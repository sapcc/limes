/*******************************************************************************
*
* Copyright 2020 SAP SE
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

package cronus

import (
	"context"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
)

// Client is a gophercloud.ServiceClient for the Cronus v1 API.
type Client struct {
	*gophercloud.ServiceClient
}

func NewClient(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (*Client, error) {
	serviceType := "email-aws"
	eo.ApplyDefaults(serviceType)

	url, err := provider.EndpointLocator(eo)
	if err != nil {
		return nil, err
	}
	return &Client{
		ServiceClient: &gophercloud.ServiceClient{
			ProviderClient: provider,
			Endpoint:       url,
			Type:           serviceType,
		},
	}, nil
}

// Usage contains Cronus usage data for a single project.
type Usage struct {
	AttachmentsSize uint64 `json:"attachments_size"`
	DataTransferIn  uint64 `json:"data_transfer_in"`
	DataTransferOut uint64 `json:"data_transfer_out"`
	Recipients      uint64 `json:"recipients"`
	StartDate       string `json:"start"`
	EndDate         string `json:"end"`
}

// GetUsage returns usage data for a single project.
func (c Client) GetUsage(ctx context.Context, projectUUID string, previous bool) (Usage, error) {
	url := c.ServiceURL("v1", "usage", projectUUID)
	if previous {
		url += "?prev=true"
	}

	var result gophercloud.Result
	_, result.Err = c.Get(ctx, url, &result.Body, &gophercloud.RequestOpts{ //nolint:bodyclose // already closed by gophercloud
		OkCodes: []int{http.StatusOK},
	})

	var data struct {
		Usage Usage `json:"usage"`
	}
	err := result.ExtractInto(&data)
	return data.Usage, err
}
