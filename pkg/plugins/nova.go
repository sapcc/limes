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
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/limits"
	"github.com/gophercloud/gophercloud/openstack/compute/v2/extensions/quotasets"
	"github.com/sapcc/limes/pkg/limes"
)

type novaPlugin struct {
	cfg limes.ServiceConfiguration
}

var novaResources = []limes.ResourceInfo{
	{
		Name: "cores",
		Unit: limes.UnitNone,
	},
	{
		Name: "instances",
		Unit: limes.UnitNone,
	},
	{
		Name: "ram",
		Unit: limes.UnitMebibytes,
	},
}

func init() {
	limes.RegisterQuotaPlugin(func(c limes.ServiceConfiguration, scrapeSubresources map[string]bool) limes.QuotaPlugin {
		return &novaPlugin{c}
	})
}

//ServiceInfo implements the limes.QuotaPlugin interface.
func (p *novaPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type: "compute",
		Area: "compute",
	}
}

//Resources implements the limes.QuotaPlugin interface.
func (p *novaPlugin) Resources() []limes.ResourceInfo {
	return novaResources
}

func (p *novaPlugin) Client(driver limes.Driver) (*gophercloud.ServiceClient, error) {
	return openstack.NewComputeV2(driver.Client(),
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
}

//Scrape implements the limes.QuotaPlugin interface.
func (p *novaPlugin) Scrape(driver limes.Driver, domainUUID, projectUUID string) (map[string]limes.ResourceData, error) {
	client, err := p.Client(driver)
	if err != nil {
		return nil, err
	}

	quotas, err := quotasets.Get(client, projectUUID).Extract()
	if err != nil {
		return nil, err
	}

	limits, err := limits.Get(client, limits.GetOpts{TenantID: projectUUID}).Extract()
	if err != nil {
		return nil, err
	}

	return map[string]limes.ResourceData{
		"cores": {
			Quota: int64(quotas.Cores),
			Usage: uint64(limits.Absolute.TotalCoresUsed),
		},
		"instances": {
			Quota: int64(quotas.Instances),
			Usage: uint64(limits.Absolute.TotalInstancesUsed),
		},
		"ram": {
			Quota: int64(quotas.Ram),
			Usage: uint64(limits.Absolute.TotalRAMUsed),
		},
	}, nil
}

//SetQuota implements the limes.QuotaPlugin interface.
func (p *novaPlugin) SetQuota(driver limes.Driver, domainUUID, projectUUID string, quotas map[string]uint64) error {
	client, err := p.Client(driver)
	if err != nil {
		return err
	}

	return quotasets.Update(client, projectUUID, quotasets.UpdateOpts{
		Cores:     makeIntPointer(int(quotas["cores"])),
		Instances: makeIntPointer(int(quotas["instances"])),
		Ram:       makeIntPointer(int(quotas["ram"])),
	}).Err
}

func makeIntPointer(value int) *int {
	return &value
}
