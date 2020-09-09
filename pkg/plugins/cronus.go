/******************************************************************************
*
*  Copyright 2020 SAP SE
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

import (
	"fmt"
	"net/http"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
)

type cronusPlugin struct {
	cfg core.ServiceConfiguration
}

var cronusResources = []limes.ResourceInfo{
	{
		Name: "attachments_size",
		Unit: limes.UnitBytes,
	},
	{
		Name: "data_transfer_out",
		Unit: limes.UnitBytes,
	},
	{
		Name: "data_transfer_in",
		Unit: limes.UnitBytes,
	},
	{
		Name: "recipients",
		Unit: limes.UnitNone,
	},
}

func init() {
	core.RegisterQuotaPlugin(func(c core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		return &cronusPlugin{c}
	})
}

//Init implements the core.QuotaPlugin interface.
func (p *cronusPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

//ServiceInfo implements the core.QuotaPlugin interface.
func (p *cronusPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "email-aws",
		ProductName: "cronus",
		Area:        "email",
	}
}

//Resources implements the core.QuotaPlugin interface.
func (p *cronusPlugin) Resources() []limes.ResourceInfo {
	return cronusResources
}

//Scrape implements the core.QuotaPlugin interface.
func (p *cronusPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string) (map[string]core.ResourceData, error) {
	client, err := newCronusClient(provider, eo)
	if err != nil {
		return nil, err
	}
	quotas, err := client.GetQuota(projectUUID)
	if err != nil {
		return nil, err
	}
	return map[string]core.ResourceData{
		"recipients": {
			Usage: quotas.Usage.Recipients,
		},
		"attachments_size": {
			Usage: quotas.Usage.AttachmentsSize,
		},
		"data_transfer_in": {
			Usage: quotas.Usage.DataTransferIn,
		},
		"data_transfer_out": {
			Usage: quotas.Usage.DataTransferOut,
		},
	}, nil
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *cronusPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	return fmt.Errorf("setting email quota is not supported")
}

////////////////////////////////////////////////////////////////////////////////
// Gophercloud client for Cronus

type cronusClient struct {
	*gophercloud.ServiceClient
}

func newCronusClient(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (*cronusClient, error) {
	serviceType := "email-aws"
	eo.ApplyDefaults(serviceType)

	url, err := provider.EndpointLocator(eo)
	if err != nil {
		return nil, err
	}
	return &cronusClient{
		ServiceClient: &gophercloud.ServiceClient{
			ProviderClient: provider,
			Endpoint:       url,
			Type:           serviceType,
		},
	}, nil
}

type cronusUsage struct {
	Usage struct {
		AttachmentsSize uint64 `json:"attachments_size"`
		DataTransferIn  uint64 `json:"data_transfer_in"`
		DataTransferOut uint64 `json:"data_transfer_out"`
		Recipients      uint64 `json:"recipients"`
	} `json:"usage"`
}

func (c cronusClient) GetQuota(projectUUID string) (cronusUsage, error) {
	url := c.ServiceURL("v1", "usage", projectUUID)

	var result gophercloud.Result
	_, result.Err = c.Get(url, &result.Body, &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK},
	})

	var qs cronusUsage
	err := result.ExtractInto(&qs)
	return qs, err
}

func (c cronusClient) SetQuota(projectUUID string, qs cronusUsage) error {
	url := c.ServiceURL("v1", "usage", projectUUID)
	_, err := c.Put(url, &qs, nil, &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK},
	})
	return err
}
