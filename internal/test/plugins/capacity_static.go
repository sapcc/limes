/*******************************************************************************
*
* Copyright 2017-2020 SAP SE
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
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// StaticCapacityPlugin is a core.CapacityPlugin implementation for unit tests.
type StaticCapacityPlugin struct {
	Resources         []string `yaml:"resources"` // each formatted as "servicetype/resourcename"
	Capacity          uint64   `yaml:"capacity"`
	WithAZCapData     bool     `yaml:"with_capacity_per_az"`
	WithSubcapacities bool     `yaml:"with_subcapacities"`
	WithoutUsage      bool     `yaml:"without_usage"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &StaticCapacityPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *StaticCapacityPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *StaticCapacityPlugin) PluginTypeID() string {
	return "--test-static"
}

// Scrape implements the core.CapacityPlugin interface.
func (p *StaticCapacityPlugin) Scrape(ctx context.Context, _ core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (result map[db.ServiceType]map[liquid.ResourceName]core.PerAZ[core.CapacityData], serializedMetrics []byte, err error) {
	makeAZCapa := func(az string, capacity, usage uint64) *core.CapacityData {
		var subcapacities []any
		if p.WithSubcapacities {
			smallerHalf := capacity / 3
			largerHalf := capacity - smallerHalf
			subcapacities = []any{
				map[string]any{"az": az, "smaller_half": smallerHalf},
				map[string]any{"az": az, "larger_half": largerHalf},
			}
		}
		result := core.CapacityData{
			Capacity:      capacity,
			Usage:         &usage,
			Subcapacities: subcapacities,
		}
		if p.WithoutUsage {
			result.Usage = nil
		}
		return &result
	}

	fullCapa := core.PerAZ[core.CapacityData]{
		"az-one": makeAZCapa("az-one", p.Capacity/2, p.Capacity/10),
		"az-two": makeAZCapa("az-two", p.Capacity-p.Capacity/2, p.Capacity/10),
	}
	if !p.WithAZCapData {
		fullCapa = core.InAnyAZ(fullCapa.Sum())
	}

	if p.WithSubcapacities {
		// for historical reasons, serialized metrics are tested at the same time as subcapacities
		smallerHalf := p.Capacity / 3
		largerHalf := p.Capacity - smallerHalf
		serializedMetrics = []byte(fmt.Sprintf(`{"smaller_half":%d,"larger_half":%d}`, smallerHalf, largerHalf))
	}

	result = make(map[db.ServiceType]map[liquid.ResourceName]core.PerAZ[core.CapacityData])
	for _, str := range p.Resources {
		parts := strings.SplitN(str, "/", 2)
		serviceType := db.ServiceType(parts[0])
		resourceName := liquid.ResourceName(parts[1])

		_, exists := result[serviceType]
		if !exists {
			result[serviceType] = make(map[liquid.ResourceName]core.PerAZ[core.CapacityData])
		}
		result[serviceType][resourceName] = fullCapa
	}
	return result, serializedMetrics, nil
}

func (p *StaticCapacityPlugin) BuildServiceCapacityRequest(backchannel core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (liquid.ServiceCapacityRequest, error) {
	return liquid.ServiceCapacityRequest{
		AllAZs: []liquid.AvailabilityZone{"az-one", "az-two"},
		DemandByResource: map[liquid.ResourceName]liquid.ResourceDemand{
			"capacity": {
				OvercommitFactor: 1.5,
				PerAZ: map[liquid.AvailabilityZone]liquid.ResourceDemandInAZ{
					liquid.AvailabilityZoneAny: {
						Usage:              10,
						UnusedCommitments:  0,
						PendingCommitments: 0,
					},
				},
			},
		},
	}, nil
}

var (
	unittestCapacitySmallerHalfMetric = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "limes_unittest_capacity_smaller_half"},
	)
	unittestCapacityLargerHalfMetric = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "limes_unittest_capacity_larger_half"},
	)
)

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *StaticCapacityPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	if p.WithSubcapacities {
		unittestCapacitySmallerHalfMetric.Describe(ch)
		unittestCapacityLargerHalfMetric.Describe(ch)
	}
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *StaticCapacityPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte, capacitorID string) error {
	if !p.WithSubcapacities {
		return nil
	}

	var data struct {
		SmallerHalf uint64 `json:"smaller_half"`
		LargerHalf  uint64 `json:"larger_half"`
	}
	err := json.Unmarshal(serializedMetrics, &data)
	if err != nil {
		return err
	}

	descCh := make(chan *prometheus.Desc, 1)
	unittestCapacitySmallerHalfMetric.Describe(descCh)
	unittestCapacitySmallerHalfDesc := <-descCh
	unittestCapacityLargerHalfMetric.Describe(descCh)
	unittestCapacityLargerHalfDesc := <-descCh

	ch <- prometheus.MustNewConstMetric(
		unittestCapacitySmallerHalfDesc, prometheus.GaugeValue, float64(data.SmallerHalf),
	)
	ch <- prometheus.MustNewConstMetric(
		unittestCapacityLargerHalfDesc, prometheus.GaugeValue, float64(data.LargerHalf),
	)
	return nil
}
