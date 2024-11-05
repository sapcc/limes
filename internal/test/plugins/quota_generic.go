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
	"errors"
	"fmt"
	"math/big"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &GenericQuotaPlugin{} })
}

// GenericQuotaPlugin is a core.QuotaPlugin implementation for unit tests. It
// mostly reports static data and offers several controls to simulate failed
// operations.
type GenericQuotaPlugin struct {
	ServiceType              db.ServiceType                             `yaml:"-"`
	StaticRateInfos          map[liquid.RateName]liquid.RateInfo        `yaml:"rate_infos"`
	StaticResourceData       map[liquid.ResourceName]*core.ResourceData `yaml:"-"`
	StaticResourceAttributes map[liquid.ResourceName]map[string]any     `yaml:"-"`
	OverrideQuota            map[string]map[liquid.ResourceName]uint64  `yaml:"-"` // first key is project UUID
	// behavior flags that can be set by a unit test
	ScrapeFails   bool                           `yaml:"-"`
	SetQuotaFails bool                           `yaml:"-"`
	MinQuota      map[liquid.ResourceName]uint64 `yaml:"-"`
	MaxQuota      map[liquid.ResourceName]uint64 `yaml:"-"`
}

// Init implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, serviceType db.ServiceType) error {
	p.ServiceType = serviceType
	p.StaticResourceData = map[liquid.ResourceName]*core.ResourceData{
		"things": {
			Quota: 42,
			UsageData: core.PerAZ[core.UsageData]{
				"az-one": {Usage: 2},
				"az-two": {Usage: 2},
			},
		},
		"capacity": {
			Quota: 100,
			UsageData: core.PerAZ[core.UsageData]{
				"az-one": {Usage: 0},
				"az-two": {Usage: 0},
			},
		},
	}
	p.OverrideQuota = make(map[string]map[liquid.ResourceName]uint64)
	return nil
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) PluginTypeID() string {
	return "--test-generic"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) ServiceInfo() core.ServiceInfo {
	return core.ServiceInfo{
		Area:        string(p.ServiceType),
		ProductName: "generic-" + string(p.ServiceType),
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) Resources() map[liquid.ResourceName]liquid.ResourceInfo {
	result := map[liquid.ResourceName]liquid.ResourceInfo{
		"capacity":         {Unit: limes.UnitBytes, HasQuota: true},
		"capacity_portion": {Unit: limes.UnitBytes, HasQuota: false}, // NOTE: This used to be `ContainedIn: "capacity"` before we removed support for this relation.
		"things":           {Unit: limes.UnitNone, HasQuota: true},
	}

	for resName, resInfo := range result {
		attrs := p.StaticResourceAttributes[resName]
		if len(attrs) > 0 {
			buf := must.Return(json.Marshal(attrs))
			resInfo.Attributes = json.RawMessage(buf)
			result[resName] = resInfo
		}
	}

	return result
}

// Rates implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) Rates() map[liquid.RateName]liquid.RateInfo {
	return p.StaticRateInfos
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) ScrapeRates(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone, prevSerializedState string) (result map[liquid.RateName]*big.Int, serializedState string, err error) {
	if p.ScrapeFails {
		return nil, "", errors.New("ScrapeRates failed as requested")
	}

	// this dummy implementation lets itself be influenced by the existing state, but also alters it a bit
	state := make(map[liquid.RateName]int64)
	if prevSerializedState == "" {
		for rateName := range p.StaticRateInfos {
			state[rateName] = 0
		}
	} else {
		err := json.Unmarshal([]byte(prevSerializedState), &state)
		if err != nil {
			return nil, "", err
		}
		for rateName := range p.StaticRateInfos {
			state[rateName] += 1024
		}
	}

	result = make(map[liquid.RateName]*big.Int)
	for rateName := range p.StaticRateInfos {
		result[rateName] = big.NewInt(state[rateName] + int64(len(rateName)))
	}
	serializedStateBytes, _ := json.Marshal(state) //nolint:errcheck
	return result, string(serializedStateBytes), nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) Scrape(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[liquid.ResourceName]core.ResourceData, serializedMetrics []byte, err error) {
	if p.ScrapeFails {
		return nil, nil, errors.New("Scrape failed as requested")
	}

	result = make(map[liquid.ResourceName]core.ResourceData)
	for key, val := range p.StaticResourceData {
		copyOfVal := core.ResourceData{
			Quota:     val.Quota,
			UsageData: val.UsageData.Clone(),
		}

		// test coverage for PhysicalUsage != Usage
		if key == "capacity" {
			for _, data := range copyOfVal.UsageData {
				physUsage := data.Usage / 2
				data.PhysicalUsage = &physUsage
			}

			// derive a resource that does not track quota
			portionUsage := make(core.PerAZ[core.UsageData])
			for az, data := range copyOfVal.UsageData {
				portionUsage[az] = &core.UsageData{Usage: data.Usage / 4}
			}
			result["capacity_portion"] = core.ResourceData{
				UsageData: portionUsage,
			}
		}

		// test MinQuota/MaxQuota if requested
		minQuota, exists := p.MinQuota[key]
		if exists {
			copyOfVal.MinQuota = &minQuota
		}
		maxQuota, exists := p.MaxQuota[key]
		if exists {
			copyOfVal.MaxQuota = &maxQuota
		}

		result[key] = copyOfVal
	}

	data, exists := p.OverrideQuota[project.UUID]
	if exists {
		for resourceName, quota := range data {
			resData := result[resourceName]
			resData.Quota = int64(quota) //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63
			result[resourceName] = resData
		}
	}

	// make up some subresources for "things"
	counter := 0
	for _, az := range result["things"].UsageData.Keys() {
		thingsUsage := result["things"].UsageData[az].Usage
		subresources := make([]any, thingsUsage)
		for idx := range thingsUsage {
			subresources[idx] = map[string]any{"index": counter}
			counter++
		}
		result["things"].UsageData[az].Subresources = subresources
	}

	// make up some serialized metrics (reporting usage as a metric is usually
	// nonsensical since limes-collect already reports all usages as metrics, but
	// this is only a testcase anyway)
	serializedMetrics = []byte(fmt.Sprintf(`{"capacity_usage":%d,"things_usage":%d}`,
		result["capacity"].UsageData.Sum().Usage,
		result["things"].UsageData.Sum().Usage))

	return result, serializedMetrics, nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) SetQuota(ctx context.Context, project core.KeystoneProject, quotas map[liquid.ResourceName]uint64) error {
	if p.SetQuotaFails {
		return errors.New("SetQuota failed as requested")
	}
	p.OverrideQuota[project.UUID] = quotas
	return nil
}

var (
	unittestCapacityUsageMetric = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "limes_unittest_capacity_usage"},
		[]string{"domain_id", "project_id"},
	)
	unittestThingsUsageMetric = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "limes_unittest_things_usage"},
		[]string{"domain_id", "project_id"},
	)
)

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	unittestCapacityUsageMetric.Describe(ch)
	unittestThingsUsageMetric.Describe(ch)
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	if len(serializedMetrics) == 0 {
		return nil
	}

	var data struct {
		CapacityUsage uint64 `json:"capacity_usage"`
		ThingsUsage   uint64 `json:"things_usage"`
	}
	err := json.Unmarshal(serializedMetrics, &data)
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
		project.Domain.UUID, project.UUID,
	)
	ch <- prometheus.MustNewConstMetric(
		unittestThingsUsageDesc, prometheus.GaugeValue, float64(data.ThingsUsage),
		project.Domain.UUID, project.UUID,
	)
	return nil
}
