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
	"math/big"
	"net/http"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/majewsky/schwift"
	"github.com/majewsky/schwift/gopherschwift"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
)

type swiftPlugin struct {
	cfg core.ServiceConfiguration
}

var swiftResources = []limes.ResourceInfo{
	{
		Name: "capacity",
		Unit: limes.UnitBytes,
	},
}

var swiftObjectsCountGauge = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "limes_swift_objects_per_container",
		Help: "Number of objects per Swift container.",
	},
	[]string{"os_cluster", "domain_id", "project_id", "container_name"},
)

func init() {
	core.RegisterQuotaPlugin(func(c core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		return &swiftPlugin{c}
	})

	prometheus.MustRegister(swiftObjectsCountGauge)
}

//Init implements the core.QuotaPlugin interface.
func (p *swiftPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

//ServiceInfo implements the core.QuotaPlugin interface.
func (p *swiftPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "object-store",
		ProductName: "swift",
		Area:        "storage",
	}
}

//Resources implements the core.QuotaPlugin interface.
func (p *swiftPlugin) Resources() []limes.ResourceInfo {
	return swiftResources
}

//Rates implements the core.QuotaPlugin interface.
func (p *swiftPlugin) Rates() []limes.RateInfo {
	return nil
}

func (p *swiftPlugin) Account(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, projectUUID string) (*schwift.Account, error) {
	client, err := openstack.NewObjectStorageV1(provider, eo)
	if err != nil {
		return nil, err
	}
	resellerAccount, err := gopherschwift.Wrap(client, nil)
	if err != nil {
		return nil, err
	}
	//TODO Make Auth prefix configurable
	return resellerAccount.SwitchAccount("AUTH_" + projectUUID), nil
}

//ScrapeRates implements the core.QuotaPlugin interface.
func (p *swiftPlugin) ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

//Scrape implements the core.QuotaPlugin interface.
func (p *swiftPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string) (map[string]core.ResourceData, error) {
	account, err := p.Account(provider, eo, projectUUID)
	if err != nil {
		return nil, err
	}

	headers, err := account.Headers()
	if schwift.Is(err, http.StatusNotFound) || schwift.Is(err, http.StatusGone) {
		//Swift account does not exist or was deleted and not yet reaped, but the keystone project exist
		return map[string]core.ResourceData{
			"capacity": {
				Quota: 0,
				Usage: 0,
			},
		}, nil
	} else if err != nil {
		return nil, err
	}

	// collect object count metrics per container
	metricLabels := prometheus.Labels{
		"os_cluster": clusterID,
		"domain_id":  domainUUID,
		"project_id": projectUUID,
	}

	containerInfos, err := account.Containers().CollectDetailed()
	if err != nil {
		logg.Error("Could not list containers in Swift account '%s': %v", projectUUID, err)
	} else {
		for _, info := range containerInfos {
			metricLabels["container_name"] = info.Container.Name()
			swiftObjectsCountGauge.With(metricLabels).Set(float64(info.ObjectCount))
		}
	}

	data := core.ResourceData{
		Usage: headers.BytesUsed().Get(),
		Quota: int64(headers.BytesUsedQuota().Get()),
	}
	if !headers.BytesUsedQuota().Exists() {
		data.Quota = -1
	}
	return map[string]core.ResourceData{"capacity": data}, nil
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *swiftPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	account, err := p.Account(provider, eo, projectUUID)
	if err != nil {
		return err
	}

	headers := schwift.NewAccountHeaders()
	headers.BytesUsedQuota().Set(quotas["capacity"])
	//this header brought to you by https://github.com/sapcc/swift-addons
	headers.Set("X-Account-Project-Domain-Id-Override", domainUUID)

	err = account.Update(headers, nil)
	if schwift.Is(err, http.StatusNotFound) && quotas["capacity"] > 0 {
		//account does not exist yet - if there is a non-zero quota, enable it now
		err = account.Create(headers.ToOpts())
		if err == nil {
			logg.Info("Swift Account %s created", projectUUID)
		}
	}
	return err
}
