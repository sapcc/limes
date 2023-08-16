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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/sapcc/limes/internal/core"
)

// StaticCapacityPlugin is a core.CapacityPlugin implementation for unit tests.
type StaticCapacityPlugin struct {
	Resources         []string `yaml:"resources"` //each formatted as "servicetype/resourcename"
	Capacity          uint64   `yaml:"capacity"`
	WithAZCapData     bool     `yaml:"with_capacity_per_az"`
	WithSubcapacities bool     `yaml:"with_subcapacities"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &StaticCapacityPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *StaticCapacityPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubcapacities map[string]map[string]bool) error {
	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *StaticCapacityPlugin) PluginTypeID() string {
	return "--test-static"
}

// Scrape implements the core.CapacityPlugin interface.
func (p *StaticCapacityPlugin) Scrape() (result map[string]map[string]core.CapacityData, serializedMetrics string, err error) {
	var capacityPerAZ map[string]*core.CapacityDataForAZ
	if p.WithAZCapData {
		capacityPerAZ = map[string]*core.CapacityDataForAZ{
			"az-one": {
				Capacity: p.Capacity / 2,
				Usage:    uint64(float64(p.Capacity) * 0.1),
			},
			"az-two": {
				Capacity: p.Capacity / 2,
				Usage:    uint64(float64(p.Capacity) * 0.1),
			},
		}
	}

	var subcapacities []any
	if p.WithSubcapacities {
		smallerHalf := p.Capacity / 3
		largerHalf := p.Capacity - smallerHalf
		subcapacities = []any{
			map[string]uint64{"smaller_half": smallerHalf},
			map[string]uint64{"larger_half": largerHalf},
		}
		//this is also an opportunity to test serialized metrics
		serializedMetrics = fmt.Sprintf(`{"smaller_half":%d,"larger_half":%d}`, smallerHalf, largerHalf)
	}

	result = make(map[string]map[string]core.CapacityData)
	for _, str := range p.Resources {
		parts := strings.SplitN(str, "/", 2)
		_, exists := result[parts[0]]
		if !exists {
			result[parts[0]] = make(map[string]core.CapacityData)
		}
		result[parts[0]][parts[1]] = core.CapacityData{Capacity: p.Capacity, CapacityPerAZ: capacityPerAZ, Subcapacities: subcapacities}
	}
	return result, serializedMetrics, nil
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
func (p *StaticCapacityPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics string) error {
	if !p.WithSubcapacities {
		return nil
	}

	var data struct {
		SmallerHalf uint64 `json:"smaller_half"`
		LargerHalf  uint64 `json:"larger_half"`
	}
	err := json.Unmarshal([]byte(serializedMetrics), &data)
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
