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

	"github.com/sapcc/limes/internal/core"
)

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &GenericQuotaPlugin{} })
}

// GenericQuotaPlugin is a core.QuotaPlugin implementation for unit tests. It
// mostly reports static data and offers several controls to simulate failed
// operations.
type GenericQuotaPlugin struct {
	StaticRateInfos    []limesrates.RateInfo         `yaml:"rate_infos"`
	StaticResourceData map[string]*core.ResourceData `yaml:"-"`
	OverrideQuota      map[string]map[string]uint64  `yaml:"-"`
	//behavior flags that can be set by a unit test
	ScrapeFails          bool `yaml:"-"`
	QuotaIsNotAcceptable bool `yaml:"-"`
	SetQuotaFails        bool `yaml:"-"`
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

// Init implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubresources map[string]bool) error {
	p.StaticResourceData = map[string]*core.ResourceData{
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
	p.OverrideQuota = make(map[string]map[string]uint64)
	return nil
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) PluginTypeID() string {
	return "--test-generic"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) ServiceInfo(serviceType string) limes.ServiceInfo {
	return limes.ServiceInfo{
		Type: serviceType,
		Area: serviceType,
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) Resources() []limesresources.ResourceInfo {
	return resources
}

// Rates implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) Rates() []limesrates.RateInfo {
	return p.StaticRateInfos
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) ScrapeRates(project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
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
func (p *GenericQuotaPlugin) Scrape(project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[string]core.ResourceData, serializedMetrics []byte, err error) {
	if p.ScrapeFails {
		return nil, nil, errors.New("Scrape failed as requested")
	}

	result = make(map[string]core.ResourceData)
	for key, val := range p.StaticResourceData {
		copyOfVal := core.ResourceData{
			Quota:     val.Quota,
			UsageData: val.UsageData.Clone(),
		}

		//test coverage for PhysicalUsage != Usage
		if key == "capacity" {
			for _, data := range copyOfVal.UsageData {
				physUsage := data.Usage / 2
				data.PhysicalUsage = &physUsage
			}

			//derive a resource that does not track quota
			portionUsage := make(core.PerAZ[core.UsageData])
			for az, data := range copyOfVal.UsageData {
				portionUsage[az] = &core.UsageData{Usage: data.Usage / 4}
			}
			result["capacity_portion"] = core.ResourceData{
				UsageData: portionUsage,
			}
		}

		result[key] = copyOfVal
	}

	data, exists := p.OverrideQuota[project.UUID]
	if exists {
		for resourceName, quota := range data {
			result[resourceName] = core.ResourceData{
				Quota:     int64(quota),
				UsageData: result[resourceName].UsageData,
			}
		}
	}

	//make up some subresources for "things"
	counter := 0
	for _, az := range result["things"].UsageData.Keys() {
		thingsUsage := result["things"].UsageData[az].Usage
		subresources := make([]any, thingsUsage)
		for idx := uint64(0); idx < thingsUsage; idx++ {
			subresources[idx] = map[string]any{"index": counter}
			counter++
		}
		result["things"].UsageData[az].Subresources = subresources
	}

	//make up some serialized metrics (reporting usage as a metric is usually
	//nonsensical since limes-collect already reports all usages as metrics, but
	//this is only a testcase anyway)
	serializedMetrics = []byte(fmt.Sprintf(`{"capacity_usage":%d,"things_usage":%d}`,
		result["capacity"].UsageData.Sum().Usage,
		result["things"].UsageData.Sum().Usage))

	return result, serializedMetrics, nil
}

// IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) IsQuotaAcceptableForProject(project core.KeystoneProject, fullQuotas map[string]map[string]uint64, allServiceInfos []limes.ServiceInfo) error {
	if p.QuotaIsNotAcceptable {
		var quotasStr []string
		for srvType, srvQuotas := range fullQuotas {
			for resName, quota := range srvQuotas {
				quotasStr = append(quotasStr, fmt.Sprintf("%s/%s=%d", srvType, resName, quota))
			}
		}
		sort.Strings(quotasStr)
		return fmt.Errorf("IsQuotaAcceptableForProject failed as requested for quota set %s", strings.Join(quotasStr, ", "))
	}
	return nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *GenericQuotaPlugin) SetQuota(project core.KeystoneProject, quotas map[string]uint64) error {
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
