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
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
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
	LiquidClient      *LiquidV1Client    `yaml:"-"`
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

	p.LiquidClient, err = NewLiquidV1(client, eo, p.LiquidServiceType, p.EndpointOverride)
	if err != nil {
		return fmt.Errorf("cannot initialize ServiceClient for liquid-%s: %w", serviceType, err)
	}
	p.LiquidServiceInfo, err = p.LiquidClient.GetInfo(ctx)
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
	req := liquid.ServiceUsageRequest{AllAZs: allAZs}
	if p.LiquidServiceInfo.UsageReportNeedsProjectMetadata {
		req.ProjectMetadata = project.ForLiquid()
	}

	resp, err := p.LiquidClient.GetUsageReport(ctx, project.UUID, req)
	if err != nil {
		return nil, nil, err
	}
	if resp.InfoVersion != p.LiquidServiceInfo.Version {
		logg.Fatal("ServiceInfo version for %s changed from %d to %d; restarting now to reload ServiceInfo...",
			p.LiquidServiceType, p.LiquidServiceInfo.Version, resp.InfoVersion)
	}

	result = make(map[liquid.ResourceName]core.ResourceData, len(p.LiquidServiceInfo.Resources))
	for resName := range p.LiquidServiceInfo.Resources {
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
		}

		result[resName] = resData
	}

	serializedMetrics, err = liquidSerializeMetrics(p.LiquidServiceInfo.UsageMetricFamilies, resp.Metrics)
	if err != nil {
		return nil, nil, fmt.Errorf("while serializing metrics: %w", err)
	}
	return result, serializedMetrics, nil
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
	return nil
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *liquidQuotaPlugin) ScrapeRates(ctx context.Context, project core.KeystoneProject, prevSerializedState string) (result map[liquid.RateName]*big.Int, serializedState string, err error) {
	return nil, "", nil
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
