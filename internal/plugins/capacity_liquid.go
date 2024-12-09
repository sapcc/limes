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
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

type liquidCapacityPlugin struct {
	// configuration
	ServiceType       db.ServiceType `yaml:"service_type"`
	LiquidServiceType string         `yaml:"liquid_service_type"`
	EndpointOverride  string         `yaml:"endpoint_override"` // see comment on liquidQuotaPlugin for details

	// state
	LiquidServiceInfo liquid.ServiceInfo `yaml:"-"`
	LiquidClient      *liquidapi.Client  `yaml:"-"`
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

	p.LiquidClient, err = liquidapi.NewClient(client, eo, liquidapi.ClientOpts{
		ServiceType:      p.LiquidServiceType,
		EndpointOverride: p.EndpointOverride,
	})
	if err != nil {
		return fmt.Errorf("cannot initialize ServiceClient for %s: %w", p.LiquidServiceType, err)
	}
	p.LiquidServiceInfo, err = p.LiquidClient.GetInfo(ctx)
	if err != nil {
		return err
	}
	err = CheckResourceTopologies(p.LiquidServiceInfo)
	return err
}

// Scrape implements the core.QuotaPlugin interface.
func (p *liquidCapacityPlugin) Scrape(ctx context.Context, backchannel core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (result map[db.ServiceType]map[liquid.ResourceName]core.PerAZ[core.CapacityData], serializedMetrics []byte, err error) {
	req, err := p.BuildServiceCapacityRequest(backchannel, allAZs)
	if err != nil {
		return nil, nil, err
	}

	resp, err := p.LiquidClient.GetCapacityReport(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	if resp.InfoVersion != p.LiquidServiceInfo.Version {
		logg.Fatal("ServiceInfo version for %s changed from %d to %d; restarting now to reload ServiceInfo...",
			p.LiquidServiceType, p.LiquidServiceInfo.Version, resp.InfoVersion)
	}
	resourceNames := SortedMapKeys(p.LiquidServiceInfo.Resources)
	var errs []error
	for _, resourceName := range resourceNames {
		perAZ := resp.Resources[resourceName].PerAZ
		topology := p.LiquidServiceInfo.Resources[resourceName].Topology
		err := MatchLiquidReportToTopology(perAZ, topology)
		if err != nil {
			errs = append(errs, fmt.Errorf("resource: %s: %w", resourceName, err))
		}
	}
	if len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}

	resultInService := make(map[liquid.ResourceName]core.PerAZ[core.CapacityData], len(p.LiquidServiceInfo.Resources))
	for resName, resInfo := range p.LiquidServiceInfo.Resources {
		if !resInfo.HasCapacity {
			continue
		}
		resReport := resp.Resources[resName]
		if resReport == nil {
			return nil, nil, fmt.Errorf("missing report for resource %q", resName)
		}

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
	result = map[db.ServiceType]map[liquid.ResourceName]core.PerAZ[core.CapacityData]{
		p.ServiceType: resultInService,
	}

	serializedMetrics, err = liquidSerializeMetrics(p.LiquidServiceInfo.CapacityMetricFamilies, resp.Metrics)
	if err != nil {
		return nil, nil, fmt.Errorf("while serializing metrics: %w", err)
	}
	return result, serializedMetrics, nil
}

func (p *liquidCapacityPlugin) BuildServiceCapacityRequest(backchannel core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (liquid.ServiceCapacityRequest, error) {
	req := liquid.ServiceCapacityRequest{
		AllAZs:           allAZs,
		DemandByResource: make(map[liquid.ResourceName]liquid.ResourceDemand, len(p.LiquidServiceInfo.Resources)),
	}

	var err error
	for resName, resInfo := range p.LiquidServiceInfo.Resources {
		if !resInfo.HasCapacity {
			continue
		}
		if !resInfo.NeedsResourceDemand {
			continue
		}
		req.DemandByResource[resName], err = backchannel.GetResourceDemand(p.ServiceType, resName)
		if err != nil {
			return liquid.ServiceCapacityRequest{}, fmt.Errorf("while getting resource demand for %s/%s: %w", p.ServiceType, resName, err)
		}
	}
	return req, nil
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *liquidCapacityPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	liquidDescribeMetrics(ch, p.LiquidServiceInfo.CapacityMetricFamilies, nil)
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *liquidCapacityPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte, capacitorID string) error {
	return liquidCollectMetrics(ch, serializedMetrics, p.LiquidServiceInfo.CapacityMetricFamilies, nil, nil)
}
