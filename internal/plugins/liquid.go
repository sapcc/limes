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
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

type liquidQuotaPlugin struct {
	// configuration
	Area              string         `yaml:"area"`
	LiquidServiceType string         `yaml:"liquid_service_type"`
	ServiceType       db.ServiceType `yaml:"-"`

	// TODO: remove EndpointOverride once obsolete
	//
	// My ultimate goal is to move the test harness for quota/capacity plugins into limesctl, once the
	// plugins are rewritten into liquids. Until then, this override can be set to something like
	// "http://localhost:8000" to target a development copy of a liquid.
	EndpointOverride string `yaml:"endpoint_override"`

	// state
	LiquidServiceInfo liquid.ServiceInfo `yaml:"-"`
	LiquidClient      *liquidapi.Client  `yaml:"-"`
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &liquidQuotaPlugin{} })
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *liquidQuotaPlugin) PluginTypeID() string {
	return "liquid"
}

// Init implements the core.QuotaPlugin interface.
func (p *liquidQuotaPlugin) Init(ctx context.Context, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, serviceType db.ServiceType) (err error) {
	if p.Area == "" {
		return errors.New("missing required value: params.area")
	}
	p.ServiceType = serviceType
	if p.LiquidServiceType == "" {
		p.LiquidServiceType = "liquid-" + string(p.ServiceType)
	}

	p.LiquidClient, err = liquidapi.NewClient(client, eo, liquidapi.ClientOpts{
		ServiceType:      p.LiquidServiceType,
		EndpointOverride: p.EndpointOverride,
	})
	if err != nil {
		return fmt.Errorf("cannot initialize ServiceClient for liquid-%s: %w", serviceType, err)
	}
	p.LiquidServiceInfo, err = p.LiquidClient.GetInfo(ctx)
	if err != nil {
		return err
	}
	err = CheckResourceTopologies(p.LiquidServiceInfo)
	return err
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *liquidQuotaPlugin) ServiceInfo() core.ServiceInfo {
	return core.ServiceInfo{
		ProductName: strings.TrimPrefix(p.LiquidServiceType, "liquid-"),
		Area:        p.Area,
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *liquidQuotaPlugin) Resources() map[liquid.ResourceName]liquid.ResourceInfo {
	return p.LiquidServiceInfo.Resources
}

// Scrape implements the core.QuotaPlugin interface.
func (p *liquidQuotaPlugin) Scrape(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[liquid.ResourceName]core.ResourceData, serializedMetrics []byte, err error) {
	// shortcut for liquids that only have rates and no resources
	if len(p.LiquidServiceInfo.Resources) == 0 && len(p.LiquidServiceInfo.UsageMetricFamilies) == 0 {
		return nil, nil, nil
	}

	req, err := p.BuildServiceUsageRequest(project, allAZs)
	if err != nil {
		return nil, nil, err
	}

	resp, err := p.LiquidClient.GetUsageReport(ctx, project.UUID, req)
	if err != nil {
		return nil, nil, err
	}
	if resp.InfoVersion != p.LiquidServiceInfo.Version {
		logg.Fatal("ServiceInfo version for %s changed from %d to %d; restarting now to reload ServiceInfo...",
			p.LiquidServiceType, p.LiquidServiceInfo.Version, resp.InfoVersion)
	}
	for resourceName, resource := range p.LiquidServiceInfo.Resources {
		perAZ := resp.Resources[resourceName].PerAZ
		toplogy := resource.Topology
		err := MatchLiquidReportToTopology(perAZ, toplogy)
		if err != nil {
			return nil, nil, fmt.Errorf("service: %s, resource: %s: %w", p.ServiceType, resourceName, err)
		}
	}

	result = make(map[liquid.ResourceName]core.ResourceData, len(p.LiquidServiceInfo.Resources))
	for resName, resInfo := range p.LiquidServiceInfo.Resources {
		resReport := resp.Resources[resName]
		if resReport == nil {
			return nil, nil, fmt.Errorf("missing report for resource %q", resName)
		}

		var resData core.ResourceData
		if resReport.Quota != nil {
			resData.Quota = *resReport.Quota
		}
		if resReport.Forbidden {
			resData.MaxQuota = p2u64(0) // this is a temporary approximation; TODO: make Forbidden a first-class citizen in Limes core
		}

		resData.UsageData = make(core.PerAZ[core.UsageData], len(resReport.PerAZ))
		for az, azReport := range resReport.PerAZ {
			resData.UsageData[az] = &core.UsageData{
				Usage:         azReport.Usage,
				PhysicalUsage: azReport.PhysicalUsage,
				Subresources:  castSliceToAny(azReport.Subresources),
			}
			if resInfo.Topology == liquid.AZSeparatedResourceTopology && azReport.Quota != nil {
				resData.UsageData[az].Quota = *azReport.Quota
			}
		}

		result[resName] = resData
	}

	serializedMetrics, err = liquidSerializeMetrics(p.LiquidServiceInfo.UsageMetricFamilies, resp.Metrics)
	if err != nil {
		return nil, nil, fmt.Errorf("while serializing metrics: %w", err)
	}
	return result, serializedMetrics, nil
}

func (p *liquidQuotaPlugin) BuildServiceUsageRequest(project core.KeystoneProject, allAZs []limes.AvailabilityZone) (liquid.ServiceUsageRequest, error) {
	req := liquid.ServiceUsageRequest{AllAZs: allAZs}
	if p.LiquidServiceInfo.UsageReportNeedsProjectMetadata {
		req.ProjectMetadata = project.ForLiquid()
	}
	return req, nil
}

func castSliceToAny[T any](input []T) (output []any) {
	if input == nil {
		return nil
	}
	output = make([]any, len(input))
	for idx, val := range input {
		output[idx] = val
	}
	return output
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *liquidQuotaPlugin) SetQuota(ctx context.Context, project core.KeystoneProject, quotas map[liquid.ResourceName]uint64) error {
	req := liquid.ServiceQuotaRequest{
		Resources: make(map[liquid.ResourceName]liquid.ResourceQuotaRequest, len(quotas)),
	}
	for resName, quota := range quotas {
		req.Resources[resName] = liquid.ResourceQuotaRequest{Quota: quota}
	}
	if p.LiquidServiceInfo.QuotaUpdateNeedsProjectMetadata {
		req.ProjectMetadata = project.ForLiquid()
	}

	return p.LiquidClient.PutQuota(ctx, project.UUID, req)
}

// Rates implements the core.QuotaPlugin interface.
func (p *liquidQuotaPlugin) Rates() map[liquid.RateName]liquid.RateInfo {
	return p.LiquidServiceInfo.Rates
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *liquidQuotaPlugin) ScrapeRates(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone, prevSerializedState string) (result map[liquid.RateName]*big.Int, serializedState string, err error) {
	// shortcut for liquids that do not have rates
	if len(p.LiquidServiceInfo.Rates) == 0 {
		return nil, "", nil
	}

	req := liquid.ServiceUsageRequest{
		AllAZs:          allAZs,
		SerializedState: json.RawMessage(prevSerializedState),
	}
	if p.LiquidServiceInfo.QuotaUpdateNeedsProjectMetadata {
		req.ProjectMetadata = project.ForLiquid()
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
			var x big.Int
			result[rateName] = x.Add(result[rateName], azReport.Usage)
		}
	}

	return result, string(resp.SerializedState), nil
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *liquidQuotaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	liquidDescribeMetrics(ch, p.LiquidServiceInfo.UsageMetricFamilies,
		[]string{"domain_id", "project_id"},
	)
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *liquidQuotaPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	return liquidCollectMetrics(ch, serializedMetrics, p.LiquidServiceInfo.UsageMetricFamilies,
		[]string{"domain_id", "project_id"},
		[]string{project.Domain.UUID, project.UUID},
	)
}
