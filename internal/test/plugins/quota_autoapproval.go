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
	"errors"
	"math/big"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"

	"github.com/sapcc/limes/internal/core"
)

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &AutoApprovalQuotaPlugin{} })
}

// AutoApprovalQuotaPlugin is a core.QuotaPlugin implementation for testing the
// auto-approval mechanism in quota scraping.
type AutoApprovalQuotaPlugin struct {
	StaticBackendQuota uint64 `yaml:"static_backend_quota"`
}

// Init implements the core.QuotaPlugin interface.
func (p *AutoApprovalQuotaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubresources map[string]bool) error {
	return nil
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *AutoApprovalQuotaPlugin) PluginTypeID() string {
	return "--test-auto-approval"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *AutoApprovalQuotaPlugin) ServiceInfo(serviceType string) limes.ServiceInfo {
	return limes.ServiceInfo{
		Type: serviceType,
		Area: serviceType,
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *AutoApprovalQuotaPlugin) Resources() []limesresources.ResourceInfo {
	//one resource can auto-approve, one cannot because BackendQuota != AutoApproveInitialQuota
	return []limesresources.ResourceInfo{
		{
			Name:                    "approve",
			AutoApproveInitialQuota: p.StaticBackendQuota,
		},
		{
			Name:                    "noapprove",
			AutoApproveInitialQuota: p.StaticBackendQuota,
		},
	}
}

// Rates implements the core.QuotaPlugin interface.
func (p *AutoApprovalQuotaPlugin) Rates() []limesrates.RateInfo {
	return nil
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *AutoApprovalQuotaPlugin) ScrapeRates(project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *AutoApprovalQuotaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *AutoApprovalQuotaPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	return nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *AutoApprovalQuotaPlugin) Scrape(project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[string]core.ResourceData, serializedMetrics []byte, err error) {
	return map[string]core.ResourceData{
		"approve":   {UsageData: core.InAnyAZ(core.UsageData{Usage: 0}), Quota: int64(p.StaticBackendQuota)},
		"noapprove": {UsageData: core.InAnyAZ(core.UsageData{Usage: 0}), Quota: int64(p.StaticBackendQuota) + 10},
	}, nil, nil
}

// IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *AutoApprovalQuotaPlugin) IsQuotaAcceptableForProject(project core.KeystoneProject, fullQuotas map[string]map[string]uint64, allServiceInfos []limes.ServiceInfo) error {
	return errors.New("unimplemented")
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *AutoApprovalQuotaPlugin) SetQuota(project core.KeystoneProject, quotas map[string]uint64) error {
	return errors.New("unimplemented")
}
