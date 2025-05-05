/*******************************************************************************
*
* Copyright 2024 SAP SE
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
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/gophercloud/gophercloud/v2"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

type LiquidQuotaPlugin struct {
	// configuration
	Area              string         `yaml:"area"`
	LiquidServiceType string         `yaml:"liquid_service_type"`
	ServiceType       db.ServiceType `yaml:"-"`

	// state
	LiquidServiceInfo liquid.ServiceInfo `yaml:"-"`
	LiquidClient      core.LiquidClient  `yaml:"-"`
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &LiquidQuotaPlugin{} })
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *LiquidQuotaPlugin) PluginTypeID() string {
	return "liquid"
}

// Init implements the core.QuotaPlugin interface.
func (p *LiquidQuotaPlugin) Init(ctx context.Context, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, serviceType db.ServiceType) (err error) {
	p.ServiceType = serviceType
	if p.LiquidServiceType == "" {
		p.LiquidServiceType = "liquid-" + string(p.ServiceType)
	}

	p.LiquidClient, err = core.NewLiquidClient(client, eo, liquidapi.ClientOpts{ServiceType: p.LiquidServiceType})
	if err != nil {
		return err
	}

	p.LiquidServiceInfo, err = p.LiquidClient.GetInfo(ctx)
	if err != nil {
		return err
	}
	err = liquid.ValidateServiceInfo(p.LiquidServiceInfo)
	return err
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *LiquidQuotaPlugin) ServiceInfo() liquid.ServiceInfo {
	return p.LiquidServiceInfo
}

// Resources implements the core.QuotaPlugin interface.
func (p *LiquidQuotaPlugin) Resources() map[liquid.ResourceName]liquid.ResourceInfo {
	return p.LiquidServiceInfo.Resources
}

// Scrape implements the core.QuotaPlugin interface.
func (p *LiquidQuotaPlugin) Scrape(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result liquid.ServiceUsageReport, err error) {
	// shortcut for liquids that only have rates and no resources
	if len(p.LiquidServiceInfo.Resources) == 0 && len(p.LiquidServiceInfo.UsageMetricFamilies) == 0 {
		return liquid.ServiceUsageReport{}, nil
	}

	req, err := p.BuildServiceUsageRequest(project, allAZs)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	result, err = p.LiquidClient.GetUsageReport(ctx, project.UUID, req)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}
	if result.InfoVersion != p.LiquidServiceInfo.Version {
		logg.Fatal("ServiceInfo version for %s changed from %d to %d; restarting now to reload ServiceInfo...",
			p.LiquidServiceType, p.LiquidServiceInfo.Version, result.InfoVersion)
	}
	err = liquid.ValidateServiceInfo(p.LiquidServiceInfo)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}
	err = liquid.ValidateUsageReport(result, req, p.LiquidServiceInfo)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	return result, nil
}

func (p *LiquidQuotaPlugin) BuildServiceUsageRequest(project core.KeystoneProject, allAZs []limes.AvailabilityZone) (liquid.ServiceUsageRequest, error) {
	req := liquid.ServiceUsageRequest{AllAZs: allAZs}
	if p.LiquidServiceInfo.UsageReportNeedsProjectMetadata {
		req.ProjectMetadata = Some(project.ForLiquid())
	}
	return req, nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *LiquidQuotaPlugin) SetQuota(ctx context.Context, project core.KeystoneProject, quotaReq map[liquid.ResourceName]liquid.ResourceQuotaRequest) error {
	req := liquid.ServiceQuotaRequest{Resources: quotaReq}
	if p.LiquidServiceInfo.QuotaUpdateNeedsProjectMetadata {
		req.ProjectMetadata = Some(project.ForLiquid())
	}

	return p.LiquidClient.PutQuota(ctx, project.UUID, req)
}

// Rates implements the core.QuotaPlugin interface.
func (p *LiquidQuotaPlugin) Rates() map[liquid.RateName]liquid.RateInfo {
	return p.LiquidServiceInfo.Rates
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *LiquidQuotaPlugin) ScrapeRates(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone, prevSerializedState string) (result map[liquid.RateName]*big.Int, serializedState string, err error) {
	// shortcut for liquids that do not have rates
	if len(p.LiquidServiceInfo.Rates) == 0 {
		return nil, "", nil
	}

	req := liquid.ServiceUsageRequest{
		AllAZs:          allAZs,
		SerializedState: json.RawMessage(prevSerializedState),
	}
	if p.LiquidServiceInfo.UsageReportNeedsProjectMetadata {
		req.ProjectMetadata = Some(project.ForLiquid())
	}

	resp, err := p.LiquidClient.GetUsageReport(ctx, project.UUID, req)
	if err != nil {
		return nil, "", err
	}
	if resp.InfoVersion != p.LiquidServiceInfo.Version {
		logg.Fatal("ServiceInfo version for %s changed from %d to %d; restarting now to reload ServiceInfo...",
			p.LiquidServiceType, p.LiquidServiceInfo.Version, resp.InfoVersion)
	}

	result = make(map[liquid.RateName]*big.Int)
	for rateName := range p.LiquidServiceInfo.Rates {
		rateReport := resp.Rates[rateName]
		if rateReport == nil {
			return nil, "", fmt.Errorf("missing report for rate %q", rateName)
		}

		// TODO: add AZ-awareness for rate usage in Limes
		// (until this is done, we take the sum over all AZs here)
		result[rateName] = &big.Int{}
		for _, azReport := range rateReport.PerAZ {
			if usage, ok := azReport.Usage.Unpack(); ok {
				var x big.Int
				result[rateName] = x.Add(result[rateName], usage)
			}
		}
	}

	return result, string(resp.SerializedState), nil
}
