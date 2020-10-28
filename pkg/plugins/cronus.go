/*******************************************************************************
*
* Copyright 2020 SAP SE
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
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
)

type cronusPlugin struct {
	cfg core.ServiceConfiguration
}

var cronusRates = []limes.RateInfo{
	{
		Name: "attachment_size",
		Unit: limes.UnitBytes,
	},
	{
		Name: "data_transfer_in",
		Unit: limes.UnitBytes,
	},
	{
		Name: "data_transfer_out",
		Unit: limes.UnitBytes,
	},
	{
		Name: "recipients",
		Unit: limes.UnitNone,
	},
}

func init() {
	core.RegisterQuotaPlugin(func(c core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		return &cronusPlugin{c}
	})
}

//Init implements the core.QuotaPlugin interface.
func (p *cronusPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

//ServiceInfo implements the core.QuotaPlugin interface.
func (p *cronusPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "email-aws",
		ProductName: "cronus",
		Area:        "email",
	}
}

//Resources implements the core.QuotaPlugin interface.
func (p *cronusPlugin) Resources() []limes.ResourceInfo {
	return nil
}

//Rates implements the core.QuotaPlugin interface.
func (p *cronusPlugin) Rates() []limes.RateInfo {
	return cronusRates
}

//Scrape implements the core.QuotaPlugin interface.
func (p *cronusPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string) (map[string]core.ResourceData, error) {
	return nil, nil
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *cronusPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	return nil
}

//ScrapeRates implements the core.QuotaPlugin interface.
func (p *cronusPlugin) ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	dummyUsage := big.NewInt(time.Now().Unix())
	return map[string]*big.Int{
		"attachment_size":   dummyUsage,
		"data_transfer_in":  dummyUsage,
		"data_transfer_out": dummyUsage,
		"recipients":        dummyUsage,
	}, "", nil
}
