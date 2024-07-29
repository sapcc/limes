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

	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
)

type liquidCapacityPlugin struct {
	// configuration
	ServiceType       limes.ServiceType `yaml:"service_type"`
	LiquidServiceType string            `yaml:"liquid_service_type"`
	EndpointOverride  string            `yaml:"endpoint_override"` // see comment on liquidQuotaPlugin for details

	// state
	LiquidServiceInfo liquid.ServiceInfo `yaml:"-"`
	LiquidClient      *LiquidV1Client    `yaml:"-"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &liquidCapacityPlugin{} })
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *liquidCapacityPlugin) PluginTypeID() string {
	return "liquid"
}

// Init implements the core.QuotaPlugin interface.
func (p *liquidCapacityPlugin) Init(ctx context.Context, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	if p.ServiceType == "" {
		return errors.New("missing required value: params.service_type")
	}
	if p.LiquidServiceType == "" {
		p.LiquidServiceType = "liquid-" + string(p.ServiceType)
	}

	p.LiquidClient, err = NewLiquidV1(client, eo, p.LiquidServiceType, p.EndpointOverride)
	if err != nil {
		return fmt.Errorf("cannot initialize ServiceClient for %s: %w", p.LiquidServiceType, err)
	}
	p.LiquidServiceInfo, err = p.LiquidClient.GetInfo(ctx)
	return err
}

// Scrape implements the core.QuotaPlugin interface.
func (p *liquidCapacityPlugin) Scrape(ctx context.Context, backchannel core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (result map[limes.ServiceType]map[limesresources.ResourceName]core.PerAZ[core.CapacityData], serializedMetrics []byte, err error) {
	req := liquid.ServiceCapacityRequest{
		AllAZs:           allAZs,
		DemandByResource: make(map[liquid.ResourceName]liquid.ResourceDemand, len(p.LiquidServiceInfo.Resources)),
	}

	for resName, resInfo := range p.LiquidServiceInfo.Resources {
		if !resInfo.HasCapacity {
			continue
		}
		if !resInfo.NeedsResourceDemand {
			continue
		}
		req.DemandByResource[resName], err = backchannel.GetResourceDemand(p.ServiceType, resName)
		if err != nil {
			return nil, nil, fmt.Errorf("while getting resource demand for %s/%s: %w", p.ServiceType, resName, err)
		}
	}

	resp, err := p.LiquidClient.GetCapacityReport(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if resp.InfoVersion != p.LiquidServiceInfo.Version {
		logg.Fatal("ServiceInfo version for %s changed from %d to %d; restarting now to reload ServiceInfo...",
			p.LiquidServiceType, p.LiquidServiceInfo.Version, resp.InfoVersion)
	}

	resultInService := make(map[limesresources.ResourceName]core.PerAZ[core.CapacityData], len(p.LiquidServiceInfo.Resources))
	for resName := range p.LiquidServiceInfo.Resources {
		resReport := resp.Resources[resName]

		resData := make(core.PerAZ[core.CapacityData], len(resReport.PerAZ))
		for az, azReport := range resReport.PerAZ {
			resData[az] = &core.CapacityData{
				Capacity:      azReport.Capacity,
				Usage:         azReport.Usage,
				Subcapacities: castSliceToAny(azReport.Subcapacities),
			}
		}
		resultInService[resName] = resData
	}
	result = map[limes.ServiceType]map[limesresources.ResourceName]core.PerAZ[core.CapacityData]{
		p.ServiceType: resultInService,
	}

	serializedMetrics, err = liquidSerializeMetrics(p.LiquidServiceInfo.CapacityMetricFamilies, resp.Metrics)
	if err != nil {
		return nil, nil, fmt.Errorf("while serializing metrics: %w", err)
	}
	return result, serializedMetrics, nil
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *liquidCapacityPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	liquidDescribeMetrics(ch, p.LiquidServiceInfo.CapacityMetricFamilies, nil)
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *liquidCapacityPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte, capacitorID string) error {
	return liquidCollectMetrics(ch, serializedMetrics, p.LiquidServiceInfo.CapacityMetricFamilies, nil, nil)
}
