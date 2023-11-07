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
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/majewsky/schwift"
	"github.com/majewsky/schwift/gopherschwift"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
)

type swiftPlugin struct {
	//connections
	ResellerAccount *schwift.Account `yaml:"-"`
}

var swiftResources = []limesresources.ResourceInfo{
	{
		Name: "capacity",
		Unit: limes.UnitBytes,
	},
}

var (
	swiftObjectsCountGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "limes_swift_objects_per_container",
			Help: "Number of objects for each Swift container.",
		},
		[]string{"domain_id", "project_id", "container_name"},
	)
	swiftBytesUsedGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "limes_swift_size_bytes_per_container",
			Help: "Total object size in bytes for each Swift container.",
		},
		[]string{"domain_id", "project_id", "container_name"},
	)
)

// This is a purely internal format, so we use 1-character keys to save a few
// bytes and thus a few CPU cycles.
type swiftSerializedMetrics struct {
	Containers map[string]swiftSerializedContainerMetrics `json:"c"`
}
type swiftSerializedContainerMetrics struct {
	ObjectCount uint64 `json:"o"`
	BytesUsed   uint64 `json:"b"`
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &swiftPlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *swiftPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubresources map[string]bool) error {
	client, err := openstack.NewObjectStorageV1(provider, eo)
	if err != nil {
		return err
	}
	p.ResellerAccount, err = gopherschwift.Wrap(client, nil)
	return err
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *swiftPlugin) PluginTypeID() string {
	return "object-store"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *swiftPlugin) ServiceInfo(serviceType string) limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        serviceType,
		ProductName: "swift",
		Area:        "storage",
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *swiftPlugin) Resources() []limesresources.ResourceInfo {
	return swiftResources
}

// Rates implements the core.QuotaPlugin interface.
func (p *swiftPlugin) Rates() []limesrates.RateInfo {
	return nil
}

func (p *swiftPlugin) Account(projectUUID string) *schwift.Account {
	return p.ResellerAccount.SwitchAccount("AUTH_" + projectUUID)
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *swiftPlugin) ScrapeRates(project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *swiftPlugin) Scrape(project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[string]core.ResourceData, serializedMetrics []byte, err error) {
	account := p.Account(project.UUID)
	headers, err := account.Headers()
	if schwift.Is(err, http.StatusNotFound) || schwift.Is(err, http.StatusGone) {
		//Swift account does not exist or was deleted and not yet reaped, but the keystone project exists
		return map[string]core.ResourceData{
			"capacity": {
				Quota:     0,
				UsageData: core.InAnyAZ(core.UsageData{Usage: 0}),
			},
		}, nil, nil
	} else if err != nil {
		return nil, nil, err
	}

	// collect object count metrics per container
	containerInfos, err := account.Containers().CollectDetailed()
	if err != nil {
		return nil, nil, fmt.Errorf("cannot list containers: %w", err)
	}
	var metrics swiftSerializedMetrics
	metrics.Containers = make(map[string]swiftSerializedContainerMetrics, len(containerInfos))
	for _, info := range containerInfos {
		metrics.Containers[info.Container.Name()] = swiftSerializedContainerMetrics{
			ObjectCount: info.ObjectCount,
			BytesUsed:   info.BytesUsed,
		}
	}
	serializedMetrics, err = json.Marshal(metrics)
	if err != nil {
		return nil, nil, err
	}

	//optimization: skip submitting metrics entirely if there are no metrics to submit
	if len(containerInfos) == 0 {
		serializedMetrics = nil
	}

	data := core.ResourceData{
		Quota: int64(headers.BytesUsedQuota().Get()),
		UsageData: core.InAnyAZ(core.UsageData{
			Usage: headers.BytesUsed().Get(),
		}),
	}
	if !headers.BytesUsedQuota().Exists() {
		data.Quota = -1
	}
	return map[string]core.ResourceData{"capacity": data}, serializedMetrics, nil
}

// IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *swiftPlugin) IsQuotaAcceptableForProject(project core.KeystoneProject, fullQuotas map[string]map[string]uint64, allServiceInfos []limes.ServiceInfo) error {
	//not required for this plugin
	return nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *swiftPlugin) SetQuota(project core.KeystoneProject, quotas map[string]uint64) error {
	headers := schwift.NewAccountHeaders()
	headers.BytesUsedQuota().Set(quotas["capacity"])
	//this header brought to you by https://github.com/sapcc/swift-addons
	headers.Set("X-Account-Project-Domain-Id-Override", project.Domain.UUID)

	account := p.Account(project.UUID)
	err := account.Update(headers, nil)
	if schwift.Is(err, http.StatusNotFound) && quotas["capacity"] > 0 {
		//account does not exist yet - if there is a non-zero quota, enable it now
		err = account.Create(headers.ToOpts())
		if err == nil {
			logg.Info("Swift Account %s created", project.UUID)
		}
	}
	return err
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *swiftPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	swiftObjectsCountGauge.Describe(ch)
	swiftBytesUsedGauge.Describe(ch)
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *swiftPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	if len(serializedMetrics) == 0 {
		return nil
	}
	var metrics swiftSerializedMetrics
	err := json.Unmarshal(serializedMetrics, &metrics)
	if err != nil {
		return err
	}

	descCh := make(chan *prometheus.Desc, 1)
	swiftObjectsCountGauge.Describe(descCh)
	swiftObjectsCountDesc := <-descCh
	swiftBytesUsedGauge.Describe(descCh)
	swiftBytesUsedDesc := <-descCh

	for containerName, containerMetrics := range metrics.Containers {
		ch <- prometheus.MustNewConstMetric(
			swiftObjectsCountDesc,
			prometheus.GaugeValue, float64(containerMetrics.ObjectCount),
			project.Domain.UUID, project.UUID, containerName,
		)
		ch <- prometheus.MustNewConstMetric(
			swiftBytesUsedDesc,
			prometheus.GaugeValue, float64(containerMetrics.BytesUsed),
			project.Domain.UUID, project.UUID, containerName,
		)
	}

	return nil
}
