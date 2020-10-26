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

import (
	"net/http"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
)

type keppelPlugin struct {
	cfg core.ServiceConfiguration
}

var keppelResources = []limes.ResourceInfo{
	{
		Name: "images",
		Unit: limes.UnitNone,
	},
}

func init() {
	core.RegisterQuotaPlugin(func(c core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		return &keppelPlugin{c}
	})
}

//Init implements the core.QuotaPlugin interface.
func (p *keppelPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

//ServiceInfo implements the core.QuotaPlugin interface.
func (p *keppelPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "keppel",
		ProductName: "keppel",
		Area:        "storage",
	}
}

//Resources implements the core.QuotaPlugin interface.
func (p *keppelPlugin) Resources() []limes.ResourceInfo {
	return keppelResources
}

//Rates implements the core.QuotaPlugin interface.
func (p *keppelPlugin) Rates() []limes.RateInfo {
	return nil
}

//Scrape implements the core.QuotaPlugin interface.
func (p *keppelPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string) (map[string]core.ResourceData, error) {
	client, err := newKeppelClient(provider, eo)
	if err != nil {
		return nil, err
	}
	quotas, err := client.GetQuota(projectUUID)
	if err != nil {
		return nil, err
	}
	return map[string]core.ResourceData{
		"images": {
			Quota: quotas.Manifests.Quota,
			Usage: quotas.Manifests.Usage,
		},
	}, nil
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *keppelPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	client, err := newKeppelClient(provider, eo)
	if err != nil {
		return err
	}

	var qs keppelQuotaSet
	qs.Manifests.Quota = int64(quotas["images"])
	return client.SetQuota(projectUUID, qs)
}

////////////////////////////////////////////////////////////////////////////////
// Gophercloud client for Keppel

type keppelClient struct {
	*gophercloud.ServiceClient
}

func newKeppelClient(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (*keppelClient, error) {
	serviceType := "keppel"
	eo.ApplyDefaults(serviceType)

	url, err := provider.EndpointLocator(eo)
	if err != nil {
		return nil, err
	}
	return &keppelClient{
		ServiceClient: &gophercloud.ServiceClient{
			ProviderClient: provider,
			Endpoint:       url,
			Type:           serviceType,
		},
	}, nil
}

type keppelQuotaSet struct {
	Manifests struct {
		Quota int64  `json:"quota"`
		Usage uint64 `json:"usage,omitempty"`
	} `json:"manifests"`
}

func (c keppelClient) GetQuota(projectUUID string) (keppelQuotaSet, error) {
	url := c.ServiceURL("keppel", "v1", "quotas", projectUUID)

	var result gophercloud.Result
	_, result.Err = c.Get(url, &result.Body, &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK},
	})

	var qs keppelQuotaSet
	err := result.ExtractInto(&qs)
	return qs, err
}

func (c keppelClient) SetQuota(projectUUID string, qs keppelQuotaSet) error {
	url := c.ServiceURL("keppel", "v1", "quotas", projectUUID)
	_, err := c.Put(url, &qs, nil, &gophercloud.RequestOpts{
		OkCodes: []int{http.StatusOK},
	})
	return err
}
