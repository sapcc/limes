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

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *LiquidCapacityPlugin) PluginTypeID() string {
	return "liquid"
}

// Init implements the core.CapacityPlugin interface.
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

// ServiceInfo implements the core.CapacityPlugin interface.
func (p *LiquidCapacityPlugin) ServiceInfo() liquid.ServiceInfo {
	return p.LiquidServiceInfo
}

// Scrape implements the core.CapacityPlugin interface.
// we assume that each resource only has one source of info (else, the last source wins and overwrites the previous data)
// we fail the whole collection explicitly in case any of the sources fails
func (p *LiquidCapacityPlugin) Scrape(ctx context.Context, backchannel core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (result liquid.ServiceCapacityReport, err error) {
	req, err := p.BuildServiceCapacityRequest(backchannel, allAZs)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}

	result, err = p.LiquidClient.GetCapacityReport(ctx, req)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}
	if result.InfoVersion != p.LiquidServiceInfo.Version {
		logg.Fatal("ServiceInfo version for %s changed from %d to %d; restarting now to reload ServiceInfo...",
			p.LiquidServiceType, p.LiquidServiceInfo.Version, result.InfoVersion)
	}
	err = liquid.ValidateServiceInfo(p.LiquidServiceInfo)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}
	err = liquid.ValidateCapacityReport(result, req, p.LiquidServiceInfo)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}

	// manual capacity collection
	fixedCapaConfig, exists := p.FixedCapacityConfiguration.Unpack()
	if exists {
		if result.Resources == nil {
			result.Resources = make(map[liquid.ResourceName]*liquid.ResourceCapacityReport)
		}
		for resName, capacity := range fixedCapaConfig.Values {
			result.Resources[resName] = &liquid.ResourceCapacityReport{
				PerAZ: liquid.InAnyAZ(liquid.AZResourceCapacityReport{Capacity: capacity}),
			}
		}
	}

	// prometheus capacity collection
	prometheusCapaConfig, exists := p.PrometheusCapacityConfiguration.Unpack()
	if exists {
		if result.Resources == nil {
			result.Resources = make(map[liquid.ResourceName]*liquid.ResourceCapacityReport)
		}
		client, err := prometheusCapaConfig.APIConfig.Connect()
		if err != nil {
			return liquid.ServiceCapacityReport{}, err
		}
		for resName, query := range prometheusCapaConfig.Queries {
			azReports, err := prometheusCapaConfig.prometheusScrapeOneResource(ctx, client, query, allAZs)
			if err != nil {
				return liquid.ServiceCapacityReport{}, fmt.Errorf("while scraping prometheus capacity %q/%q: %w", p.ServiceType, resName, err)
			}
			result.Resources[resName] = &liquid.ResourceCapacityReport{
				PerAZ: azReports,
			}
		}
	}

	return result, nil
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

func (p prometheusCapacityConfiguration) prometheusScrapeOneResource(ctx context.Context, client promquery.Client, query string, allAZs []limes.AvailabilityZone) (map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport, error) {
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
	result := make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport)
	for az, sample := range matchedSamples {
		result[az] = &liquid.AZResourceCapacityReport{
			Capacity: uint64(sample.Value),
		}
	}
	if len(result) == 0 || len(unmatchedSamples) > 0 {
		unmatchedCapacity := float64(0.0)
		for _, sample := range unmatchedSamples {
			unmatchedCapacity += float64(sample.Value)
		}
		result[limes.AvailabilityZoneUnknown] = &liquid.AZResourceCapacityReport{
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
