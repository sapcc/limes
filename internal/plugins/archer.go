/*******************************************************************************
*
* Copyright 2023 SAP SE
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
	"math/big"
	"net/http"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"

	"github.com/sapcc/limes/internal/core"
)

type archerPlugin struct {
	//connections
	Archer *gophercloud.ServiceClient `yaml:"-"`
}

var archerResources = []limesresources.ResourceInfo{
	{
		Name: "endpoints",
		Unit: limes.UnitNone,
	},
	{
		Name: "services",
		Unit: limes.UnitNone,
	},
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &archerPlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *archerPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	serviceType := "endpoint-services"
	eo.ApplyDefaults(serviceType)

	url, err := provider.EndpointLocator(eo)
	if err != nil {
		return err
	}
	p.Archer = &gophercloud.ServiceClient{
		ProviderClient: provider,
		Endpoint:       url,
		Type:           serviceType,
	}
	return nil
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *archerPlugin) PluginTypeID() string {
	return "endpoint-services"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *archerPlugin) ServiceInfo(serviceType string) limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        serviceType,
		ProductName: "archer",
		Area:        "network",
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *archerPlugin) Resources() []limesresources.ResourceInfo {
	return archerResources
}

// Rates implements the core.QuotaPlugin interface.
func (p *archerPlugin) Rates() []limesrates.RateInfo {
	return nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *archerPlugin) Scrape(project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[string]core.ResourceData, serializedMetrics []byte, err error) {
	url := p.Archer.ServiceURL("quotas", project.UUID)
	var res gophercloud.Result
	//nolint:bodyclose // already closed by gophercloud
	_, res.Err = p.Archer.Get(url, &res.Body, &gophercloud.RequestOpts{OkCodes: []int{http.StatusOK}})

	var archerQuota struct {
		Endpoint      int64  `json:"endpoint"`
		Service       int64  `json:"service"`
		InUseEndpoint uint64 `json:"in_use_endpoint"`
		InUseService  uint64 `json:"in_use_service"`
	}
	if err = res.ExtractInto(&archerQuota); err != nil {
		return nil, nil, err
	}

	result = map[string]core.ResourceData{
		"endpoints": {
			Quota: archerQuota.Endpoint,
			UsageData: core.InAnyAZ(core.UsageData{
				Usage: archerQuota.InUseEndpoint,
			}),
		},
		"services": {
			Quota: archerQuota.Service,
			UsageData: core.InAnyAZ(core.UsageData{
				Usage: archerQuota.InUseService,
			}),
		},
	}
	return result, nil, nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *archerPlugin) SetQuota(project core.KeystoneProject, quotas map[string]uint64) error {
	url := p.Archer.ServiceURL("quotas", project.UUID)
	expect200 := &gophercloud.RequestOpts{OkCodes: []int{200}}

	body := map[string]any{
		"endpoint": quotas["endpoints"],
		"service":  quotas["services"],
	}
	//nolint:bodyclose // already closed by gophercloud
	_, err := p.Archer.Put(url, body, nil, expect200)
	return err
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *archerPlugin) ScrapeRates(project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *archerPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *archerPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	//not used by this plugin
	return nil
}
