/******************************************************************************
*
*  Copyright 2019 SAP SE
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

package plugins

import "github.com/gophercloud/gophercloud"

type placementClient struct {
	*gophercloud.ServiceClient
}

func newPlacementClient(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (*placementClient, error) {
	serviceType := "placement"
	eo.ApplyDefaults(serviceType)

	url, err := provider.EndpointLocator(eo)
	if err != nil {
		return nil, err
	}
	return &placementClient{
		ServiceClient: &gophercloud.ServiceClient{
			ProviderClient: provider,
			Endpoint:       url,
			Type:           serviceType,
		},
	}, nil
}

func (c *placementClient) reqOpts(okCodes ...int) *gophercloud.RequestOpts {
	return &gophercloud.RequestOpts{
		OkCodes:      okCodes,
		ErrorContext: cfmNotFoundError{},
	}
}

////////////////////////////////////////////////////////////////////////////////

type placementResourceProvider struct {
	ID   string `json:"uuid"`
	Name string `json:"name"`
}

func (c *placementClient) ListResourceProviders() ([]placementResourceProvider, error) {
	var result gophercloud.Result
	url := c.ServiceURL("resource_providers")
	_, result.Err = c.Get(url, &result.Body, c.reqOpts(200))

	var data struct {
		Providers []placementResourceProvider `json:"resource_providers"`
	}
	err := result.ExtractInto(&data)
	return data.Providers, err
}

type placementInventoryRecord struct {
	AllocationRatio float64 `json:"allocation_ratio"`
	MaxUnit         uint64  `json:"max_unit"`
	MinUnit         uint64  `json:"min_unit"`
	Reserved        uint64  `json:"reserved"`
	StepSize        uint64  `json:"step_size"`
	Total           uint64  `json:"total"`
}

func (c *placementClient) GetInventory(resourceProviderID string) (map[string]placementInventoryRecord, error) {
	var result gophercloud.Result
	url := c.ServiceURL("resource_providers", resourceProviderID, "inventories")
	_, result.Err = c.Get(url, &result.Body, c.reqOpts(200))

	var data struct {
		Inventories map[string]placementInventoryRecord `json:"inventories"`
	}
	err := result.ExtractInto(&data)
	return data.Inventories, err
}

func (c *placementClient) GetUsages(resourceProviderID string) (map[string]uint64, error) {
	var result gophercloud.Result
	url := c.ServiceURL("resource_providers", resourceProviderID, "usages")
	_, result.Err = c.Get(url, &result.Body, c.reqOpts(200))

	var data struct {
		Inventories map[string]uint64 `json:"usages"`
	}
	err := result.ExtractInto(&data)
	return data.Inventories, err
}
