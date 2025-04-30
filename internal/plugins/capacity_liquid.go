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
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	. "github.com/majewsky/gg/option"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/promquery"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

type prometheusCapacityConfiguration struct {
	APIConfig         promquery.Config               `yaml:"api"`
	Queries           map[liquid.ResourceName]string `yaml:"queries"`
	AllowZeroCapacity bool                           `yaml:"allow_zero_capacity"`
}

type fixedCapacityConfiguration struct {
	Values map[liquid.ResourceName]uint64 `yaml:"values"`
}

type LiquidCapacityPlugin struct {
	// configuration
	ServiceType                     db.ServiceType                          `yaml:"service_type"`
	LiquidServiceType               string                                  `yaml:"liquid_service_type"`
	FixedCapacityConfiguration      Option[fixedCapacityConfiguration]      `yaml:"fixed_capacity_values"`
	PrometheusCapacityConfiguration Option[prometheusCapacityConfiguration] `yaml:"capacity_values_from_prometheus"`

	// state
	LiquidServiceInfo liquid.ServiceInfo `yaml:"-"`
	LiquidClient      core.LiquidClient  `yaml:"-"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &LiquidCapacityPlugin{} })
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *LiquidCapacityPlugin) PluginTypeID() string {
	return "liquid"
}

// Init implements the core.QuotaPlugin interface.
func (p *LiquidCapacityPlugin) Init(ctx context.Context, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	if p.ServiceType == "" {
		return errors.New("missing required value: params.service_type")
	}
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

// Scrape implements the core.QuotaPlugin interface.
// we assume that each resource only has one source of info (else, the last source wins and overwrites the previous data)
// we fail the whole collection explicitly in case any of the sources fails
func (p *LiquidCapacityPlugin) Scrape(ctx context.Context, backchannel core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (result map[db.ServiceType]map[liquid.ResourceName]core.PerAZ[core.CapacityData], serializedMetrics []byte, err error) {
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
	err = liquid.ValidateServiceInfo(p.LiquidServiceInfo)
	if err != nil {
		return nil, nil, err
	}
	err = liquid.ValidateCapacityReport(resp, req, p.LiquidServiceInfo)
	if err != nil {
		return nil, nil, err
	}

	resultInService := make(map[liquid.ResourceName]core.PerAZ[core.CapacityData], len(p.LiquidServiceInfo.Resources))
	for resName, resInfo := range p.LiquidServiceInfo.Resources {
		if !resInfo.HasCapacity {
			continue
		}
		resReport := resp.Resources[resName]
		if resReport == nil {
			return nil, nil, fmt.Errorf("missing liquid report for resource %q/%q", p.ServiceType, resName)
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

	// manual capacity collection
	fixedCapaConfig, exists := p.FixedCapacityConfiguration.Unpack()
	if exists {
		for resName, capacity := range fixedCapaConfig.Values {
			resultInService[resName] = core.InAnyAZ(core.CapacityData{Capacity: capacity})
		}
	}

	// prometheus capacity collection
	prometheusCapaConfig, exists := p.PrometheusCapacityConfiguration.Unpack()
	if exists {
		client, err := prometheusCapaConfig.APIConfig.Connect()
		if err != nil {
			return nil, nil, err
		}
		for resName, query := range prometheusCapaConfig.Queries {
			resultInService[resName], err = prometheusCapaConfig.prometheusScrapeOneResource(ctx, client, query, allAZs)
			if err != nil {
				return nil, nil, fmt.Errorf("while scraping prometheus capacity %q/%q: %w", p.ServiceType, resName, err)
			}
		}
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

func (p *LiquidCapacityPlugin) BuildServiceCapacityRequest(backchannel core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (liquid.ServiceCapacityRequest, error) {
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
func (p *LiquidCapacityPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	liquidDescribeMetrics(ch, p.LiquidServiceInfo.CapacityMetricFamilies, nil)
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *LiquidCapacityPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte, capacitorID string) error {
	return liquidCollectMetrics(ch, serializedMetrics, p.LiquidServiceInfo.CapacityMetricFamilies, nil, nil)
}

func (p prometheusCapacityConfiguration) prometheusScrapeOneResource(ctx context.Context, client promquery.Client, query string, allAZs []limes.AvailabilityZone) (core.PerAZ[core.CapacityData], error) {
	vector, err := client.GetVector(ctx, query)
	if err != nil {
		return nil, err
	}

	// for known AZs, we expect exactly one result;
	// all unknown AZs get lumped into AvailabilityZoneUnknown
	matchedSamples := make(map[limes.AvailabilityZone]*model.Sample)
	var unmatchedSamples []*model.Sample
	for _, sample := range vector {
		az := limes.AvailabilityZone(sample.Metric["az"])
		switch {
		case az == "":
			return nil, fmt.Errorf(`missing label "az" on metric %v = %g`, sample.Metric, sample.Value)
		case slices.Contains(allAZs, az) || az == limes.AvailabilityZoneAny:
			if matchedSamples[az] != nil {
				other := matchedSamples[az]
				return nil, fmt.Errorf(`multiple samples for az=%q: found %v = %g and %v = %g`, az, sample.Metric, sample.Value, other.Metric, other.Value)
			}
			matchedSamples[az] = sample
		default:
			unmatchedSamples = append(unmatchedSamples, sample)
		}
	}

	// build result
	result := core.PerAZ[core.CapacityData]{}
	for az, sample := range matchedSamples {
		result[az] = &core.CapacityData{
			Capacity: uint64(sample.Value),
		}
	}
	if len(result) == 0 || len(unmatchedSamples) > 0 {
		unmatchedCapacity := float64(0.0)
		for _, sample := range unmatchedSamples {
			unmatchedCapacity += float64(sample.Value)
		}
		result[limes.AvailabilityZoneUnknown] = &core.CapacityData{
			Capacity: uint64(unmatchedCapacity),
		}
	}

	// validate result
	if !p.AllowZeroCapacity {
		totalCapacity := uint64(0)
		for _, azData := range result {
			totalCapacity += azData.Capacity
		}
		if totalCapacity == 0 {
			return nil, errors.New("got 0 total capacity, but allow_zero_capacity = false")
		}
	}

	return result, nil
}
