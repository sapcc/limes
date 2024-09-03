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
	"errors"
	"fmt"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/promquery"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

type capacityPrometheusPlugin struct {
	APIConfig         promquery.Config                                  `yaml:"api"`
	Queries           map[db.ServiceType]map[liquid.ResourceName]string `yaml:"queries"`
	AllowZeroCapacity bool                                              `yaml:"allow_zero_capacity"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityPrometheusPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) PluginTypeID() string {
	return "prometheus"
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) Scrape(ctx context.Context, _ core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (result map[db.ServiceType]map[liquid.ResourceName]core.PerAZ[core.CapacityData], _ []byte, err error) {
	client, err := p.APIConfig.Connect()
	if err != nil {
		return nil, nil, err
	}

	result = make(map[db.ServiceType]map[liquid.ResourceName]core.PerAZ[core.CapacityData])
	for serviceType, queries := range p.Queries {
		serviceResult := make(map[liquid.ResourceName]core.PerAZ[core.CapacityData])
		for resourceName, query := range queries {
			serviceResult[resourceName], err = p.scrapeOneResource(ctx, client, query, allAZs)
			if err != nil {
				return nil, nil, fmt.Errorf("while scraping %s/%s capacity: %w", serviceType, resourceName, err)
			}
		}
		result[serviceType] = serviceResult
	}
	return result, nil, nil
}

func (p *capacityPrometheusPlugin) scrapeOneResource(ctx context.Context, client promquery.Client, query string, allAZs []limes.AvailabilityZone) (core.PerAZ[core.CapacityData], error) {
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

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	// not used by this plugin
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityPrometheusPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte, capacitorID string) error {
	// not used by this plugin
	return nil
}
