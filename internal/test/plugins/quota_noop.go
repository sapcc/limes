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

package plugins

import (
	"math/big"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"

	"github.com/sapcc/limes/internal/core"
)

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &NoopQuotaPlugin{} })
}

// NoopQuotaPlugin is a core.QuotaPlugin implementation for tests, with no
// resources or rates at all.
//
// Alternatively, `with_empty_resource: true` can be set to report a resource
// with no UsageData at all (not even zero, the UsageData map just does not
// have any entries at all).
type NoopQuotaPlugin struct {
	WithEmptyResource bool `yaml:"with_empty_resource"`
}

// Init implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubresources map[string]bool) error {
	return nil
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) PluginTypeID() string {
	return "--test-noop"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) ServiceInfo(serviceType string) limes.ServiceInfo {
	return limes.ServiceInfo{
		Type: serviceType,
		Area: serviceType,
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) Resources() []limesresources.ResourceInfo {
	if !p.WithEmptyResource {
		return nil
	}
	return []limesresources.ResourceInfo{{
		Name: "things",
		Unit: limes.UnitNone,
	}}
}

// Rates implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) Rates() []limesrates.RateInfo {
	return nil
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) ScrapeRates(project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	return nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) Scrape(project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[string]core.ResourceData, serializedMetrics []byte, err error) {
	if p.WithEmptyResource {
		result = map[string]core.ResourceData{
			"things": {}, //no usage at all (this is used to test that the scraper adds a zero entry for AZ "any")
		}
	}
	return result, nil, nil
}

// IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) IsQuotaAcceptableForProject(project core.KeystoneProject, fullQuotas map[string]map[string]uint64, allServiceInfos []limes.ServiceInfo) error {
	return nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) SetQuota(project core.KeystoneProject, quotas map[string]uint64) error {
	return nil
}
