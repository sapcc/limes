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
	"math/big"
	"net/http"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"

	"github.com/sapcc/limes/pkg/core"
)

type keppelPlugin struct{}

var keppelResources = []limesresources.ResourceInfo{
	{
		Name: "images",
		Unit: limes.UnitNone,
	},
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &keppelPlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *keppelPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubresources map[string]bool) error {
	return nil
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *keppelPlugin) PluginTypeID() string {
	return "keppel"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *keppelPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "keppel",
		ProductName: "keppel",
		Area:        "storage",
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *keppelPlugin) Resources() []limesresources.ResourceInfo {
	return keppelResources
}

// Rates implements the core.QuotaPlugin interface.
func (p *keppelPlugin) Rates() []limesrates.RateInfo {
	return nil
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *keppelPlugin) ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *keppelPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject) (result map[string]core.ResourceData, serializedMetrics string, err error) {
	client, err := newKeppelClient(provider, eo)
	if err != nil {
		return nil, "", err
	}
	quotas, err := client.GetQuota(project.UUID)
	if err != nil {
		return nil, "", err
	}
	return map[string]core.ResourceData{
		"images": {
			Quota: quotas.Manifests.Quota,
			Usage: quotas.Manifests.Usage,
		},
	}, "", nil
}

// IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *keppelPlugin) IsQuotaAcceptableForProject(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	//not required for this plugin
	return nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *keppelPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	client, err := newKeppelClient(provider, eo)
	if err != nil {
		return err
	}

	var qs keppelQuotaSet
	qs.Manifests.Quota = int64(quotas["images"])
	return client.SetQuota(project.UUID, qs)
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *keppelPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *keppelPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics string) error {
	//not used by this plugin
	return nil
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
	_, result.Err = c.Get(url, &result.Body, &gophercloud.RequestOpts{ //nolint:bodyclose // already closed by gophercloud
		OkCodes: []int{http.StatusOK},
	})

	var qs keppelQuotaSet
	err := result.ExtractInto(&qs)
	return qs, err
}

func (c keppelClient) SetQuota(projectUUID string, qs keppelQuotaSet) error {
	url := c.ServiceURL("keppel", "v1", "quotas", projectUUID)
	_, err := c.Put(url, &qs, nil, &gophercloud.RequestOpts{ //nolint:bodyclose // already closed by gophercloud
		OkCodes: []int{http.StatusOK},
	})
	return err
}
