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

package test

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"

	"github.com/sapcc/limes/pkg/core"
)

// Plugin is a core.QuotaPlugin implementation for unit tests.
type Plugin struct {
	StaticServiceType  string
	StaticRateInfos    []limesrates.RateInfo
	StaticResourceData map[string]*core.ResourceData
	StaticCapacity     map[string]uint64
	OverrideQuota      map[string]map[string]uint64
	//behavior flags that can be set by a unit test
	ScrapeFails          bool
	QuotaIsNotAcceptable bool
	SetQuotaFails        bool
}

var resources = []limesresources.ResourceInfo{
	{
		Name: "capacity",
		Unit: limes.UnitBytes,
	},
	{
		Name:        "capacity_portion",
		Unit:        limes.UnitBytes,
		NoQuota:     true,
		ContainedIn: "capacity",
	},
	{
		Name: "things",
		Unit: limes.UnitNone,
	},
}

// NewPlugin creates a new Plugin for the given service type.
func NewPlugin(serviceType string, rates ...limesrates.RateInfo) *Plugin {
	return &Plugin{
		StaticServiceType: serviceType,
		StaticRateInfos:   rates,
		StaticResourceData: map[string]*core.ResourceData{
			"things":   {Quota: 42, Usage: 2},
			"capacity": {Quota: 100, Usage: 0},
		},
		OverrideQuota: make(map[string]map[string]uint64),
	}
}

// NewPluginFactory creates a new PluginFactory for core.RegisterQuotaPlugin.
func NewPluginFactory(serviceType string) func(core.ServiceConfiguration, map[string]bool) core.QuotaPlugin {
	return func(cfg core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		//cfg and scrapeSubresources is ignored
		return NewPlugin(serviceType)
	}
}

// Init implements the core.QuotaPlugin interface.
func (p *Plugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, cfg core.ServiceConfiguration, scrapeSubresources map[string]bool) error {
	return nil
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *Plugin) PluginTypeID() string {
	return p.StaticServiceType
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *Plugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type: p.StaticServiceType,
		Area: p.StaticServiceType,
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *Plugin) Resources() []limesresources.ResourceInfo {
	return resources
}

// Rates implements the core.QuotaPlugin interface.
func (p *Plugin) Rates() []limesrates.RateInfo {
	return p.StaticRateInfos
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *Plugin) ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	if p.ScrapeFails {
		return nil, "", errors.New("ScrapeRates failed as requested")
	}

	//this dummy implementation lets itself be influenced by the existing state, but also alters it a bit
	state := make(map[string]int64)
	if prevSerializedState == "" {
		for _, rate := range p.StaticRateInfos {
			state[rate.Name] = 0
		}
	} else {
		err := json.Unmarshal([]byte(prevSerializedState), &state)
		if err != nil {
			return nil, "", err
		}
		for _, rate := range p.StaticRateInfos {
			state[rate.Name] += 1024
		}
	}

	result = make(map[string]*big.Int)
	for _, rate := range p.StaticRateInfos {
		result[rate.Name] = big.NewInt(state[rate.Name] + int64(len(rate.Name)))
	}
	serializedStateBytes, _ := json.Marshal(state) //nolint:errcheck
	return result, string(serializedStateBytes), nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *Plugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject) (result map[string]core.ResourceData, serializedMetrics string, err error) {
	if p.ScrapeFails {
		return nil, "", errors.New("Scrape failed as requested")
	}

	result = make(map[string]core.ResourceData)
	for key, val := range p.StaticResourceData {
		copyOfVal := *val

		//test coverage for PhysicalUsage != Usage
		if key == "capacity" {
			physUsage := val.Usage / 2
			copyOfVal.PhysicalUsage = &physUsage

			//derive a resource that does not track quota
			result["capacity_portion"] = core.ResourceData{
				Usage: val.Usage / 4,
			}
		}

		result[key] = copyOfVal
	}

	data, exists := p.OverrideQuota[project.UUID]
	if exists {
		for resourceName, quota := range data {
			result[resourceName] = core.ResourceData{
				Quota:         int64(quota),
				Usage:         result[resourceName].Usage,
				PhysicalUsage: result[resourceName].PhysicalUsage,
			}
		}
	}

	//make up some subresources for "things"
	thingsUsage := int(result["things"].Usage)
	subres := make([]interface{}, thingsUsage)
	for idx := 0; idx < thingsUsage; idx++ {
		subres[idx] = map[string]interface{}{
			"index": idx,
		}
	}
	result["things"] = core.ResourceData{
		Quota:        result["things"].Quota,
		Usage:        result["things"].Usage,
		Subresources: subres,
	}

	//make up some serialized metrics (reporting usage as a metric is usually
	//nonsensical since limes-collect already reports all usages as metrics, but
	//this is only a testcase anyway)
	serializedMetrics = fmt.Sprintf(`{"capacity_usage":%d,"things_usage":%d}`,
		result["capacity"].Usage, result["things"].Usage)

	return result, serializedMetrics, nil
}

// IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *Plugin) IsQuotaAcceptableForProject(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	if p.QuotaIsNotAcceptable {
		var quotasStr []string
		for resName, quota := range quotas {
			quotasStr = append(quotasStr, fmt.Sprintf("%s=%d", resName, quota))
		}
		sort.Strings(quotasStr)
		return fmt.Errorf("IsQuotaAcceptableForProject failed as requested for quota set %s", strings.Join(quotasStr, ", "))
	}
	return nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *Plugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	if p.SetQuotaFails {
		return errors.New("SetQuota failed as requested")
	}
	p.OverrideQuota[project.UUID] = quotas
	return nil
}

var (
	unittestCapacityUsageMetric = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "limes_unittest_capacity_usage"},
		[]string{"os_cluster", "domain_id", "project_id"},
	)
	unittestThingsUsageMetric = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "limes_unittest_things_usage"},
		[]string{"os_cluster", "domain_id", "project_id"},
	)
)

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *Plugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	unittestCapacityUsageMetric.Describe(ch)
	unittestThingsUsageMetric.Describe(ch)
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *Plugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID string, project core.KeystoneProject, serializedMetrics string) error {
	if serializedMetrics == "" {
		return nil
	}

	var data struct {
		CapacityUsage uint64 `json:"capacity_usage"`
		ThingsUsage   uint64 `json:"things_usage"`
	}
	err := json.Unmarshal([]byte(serializedMetrics), &data)
	if err != nil {
		return err
	}

	descCh := make(chan *prometheus.Desc, 1)
	unittestCapacityUsageMetric.Describe(descCh)
	unittestCapacityUsageDesc := <-descCh
	unittestThingsUsageMetric.Describe(descCh)
	unittestThingsUsageDesc := <-descCh

	ch <- prometheus.MustNewConstMetric(
		unittestCapacityUsageDesc, prometheus.GaugeValue, float64(data.CapacityUsage),
		clusterID, project.Domain.UUID, project.UUID,
	)
	ch <- prometheus.MustNewConstMetric(
		unittestThingsUsageDesc, prometheus.GaugeValue, float64(data.ThingsUsage),
		clusterID, project.Domain.UUID, project.UUID,
	)
	return nil
}

// CapacityPlugin is a core.CapacityPlugin implementation for unit tests.
type CapacityPlugin struct {
	PluginType        string   `yaml:"-"`
	Resources         []string `yaml:"-"` //each formatted as "servicetype/resourcename"
	Capacity          uint64   `yaml:"-"`
	WithAZCapData     bool     `yaml:"-"`
	WithSubcapacities bool     `yaml:"-"`
}

// NewCapacityPlugin creates a new CapacityPlugin.
func NewCapacityPlugin(pluginType string, resources ...string) *CapacityPlugin {
	return &CapacityPlugin{pluginType, resources, 42, false, false}
}

// Init implements the core.CapacityPlugin interface.
func (p *CapacityPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubcapacities map[string]map[string]bool) error {
	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *CapacityPlugin) PluginTypeID() string {
	return p.PluginType
}

// Scrape implements the core.CapacityPlugin interface.
func (p *CapacityPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (result map[string]map[string]core.CapacityData, serializedMetrics string, err error) {
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

	var subcapacities []interface{}
	if p.WithSubcapacities {
		smallerHalf := p.Capacity / 3
		largerHalf := p.Capacity - smallerHalf
		subcapacities = []interface{}{
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
	unittestCapacitySmallerHalfMetric = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "limes_unittest_capacity_smaller_half"},
		[]string{"os_cluster"},
	)
	unittestCapacityLargerHalfMetric = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "limes_unittest_capacity_larger_half"},
		[]string{"os_cluster"},
	)
)

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *CapacityPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	if p.WithSubcapacities {
		unittestCapacitySmallerHalfMetric.Describe(ch)
		unittestCapacityLargerHalfMetric.Describe(ch)
	}
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *CapacityPlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID, serializedMetrics string) error {
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
		clusterID,
	)
	ch <- prometheus.MustNewConstMetric(
		unittestCapacityLargerHalfDesc, prometheus.GaugeValue, float64(data.LargerHalf),
		clusterID,
	)
	return nil
}
