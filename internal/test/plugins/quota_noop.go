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
	"context"
	"math/big"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
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
	ServiceType            db.ServiceType `yaml:"-"`
	WithEmptyResource      bool           `yaml:"with_empty_resource"`
	WithConvertCommitments bool           `yaml:"with_convert_commitments"`
}

// Init implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, serviceType db.ServiceType) error {
	p.ServiceType = serviceType
	return nil
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) PluginTypeID() string {
	return "--test-noop"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) ServiceInfo() core.ServiceInfo {
	return core.ServiceInfo{
		Area:        string(p.ServiceType),
		ProductName: "noop-" + string(p.ServiceType),
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) Resources() map[liquid.ResourceName]liquid.ResourceInfo {
	if !p.WithEmptyResource {
		return nil
	}
	if p.WithConvertCommitments {
		return map[liquid.ResourceName]liquid.ResourceInfo{
			"capacity_c32":   {Unit: limes.UnitBytes, HasQuota: true},
			"capacity_c48":   {Unit: limes.UnitBytes, HasQuota: true},
			"capacity_c96":   {Unit: limes.UnitBytes, HasQuota: true},
			"capacity_c120":  {Unit: limes.UnitNone, HasQuota: true},
			"capacity2_c144": {Unit: limes.UnitNone, HasQuota: true},
		}
	}
	return map[liquid.ResourceName]liquid.ResourceInfo{
		"things": {Unit: limes.UnitNone, HasQuota: true},
	}
}

// Rates implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) Rates() map[liquid.RateName]liquid.RateInfo {
	return nil
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) ScrapeRates(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone, prevSerializedState string) (result map[liquid.RateName]*big.Int, serializedState string, err error) {
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
func (p *NoopQuotaPlugin) Scrape(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[liquid.ResourceName]core.ResourceData, serializedMetrics []byte, err error) {
	if p.WithEmptyResource {
		result = map[liquid.ResourceName]core.ResourceData{
			"things": {}, // no usage at all (this is used to test that the scraper adds a zero entry for AZ "any")
		}
	}
	return result, nil, nil
}

func (p *NoopQuotaPlugin) BuildServiceUsageRequest(project core.KeystoneProject, allAZs []limes.AvailabilityZone) (liquid.ServiceUsageRequest, error) {
	return liquid.ServiceUsageRequest{}, core.ErrNotALiquid
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *NoopQuotaPlugin) SetQuota(ctx context.Context, project core.KeystoneProject, quotas map[liquid.ResourceName]liquid.ResourceQuotaRequest) error {
	return nil
}
