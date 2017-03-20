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

type novaPlugin struct{}

var novaResources = []limes.ResourceInfo{
	limes.ResourceInfo{
		Name: "cores",
		Unit: limes.UnitNone,
	},
	limes.ResourceInfo{
		Name: "instances",
		Unit: limes.UnitNone,
	},
	limes.ResourceInfo{
		Name: "ram",
		Unit: limes.UnitMebibytes,
	},
}

func init() {
	limes.RegisterPlugin(&novaPlugin{})
}

//ServiceType implements the limes.Plugin interface.
func (p *novaPlugin) ServiceType() string {
	return "compute"
}

//Resources implements the limes.Plugin interface.
func (p *novaPlugin) Resources() []limes.ResourceInfo {
	return novaResources
}

func (p *novaPlugin) Client(driver limes.Driver) (*gophercloud.ServiceClient, error) {
	return openstack.NewComputeV2(driver.Client(),
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
}

//Scrape implements the limes.Plugin interface.
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
		"cores": limes.ResourceData{
			Quota: int64(quotas.Cores),
			Usage: uint64(limits.Absolute.TotalCoresUsed),
		},
		"instances": limes.ResourceData{
			Quota: int64(quotas.Instances),
			Usage: uint64(limits.Absolute.TotalInstancesUsed),
		},
		"ram": limes.ResourceData{
			Quota: int64(quotas.Ram),
			Usage: uint64(limits.Absolute.TotalRAMUsed),
		},
	}, nil
}

//SetQuota implements the limes.Plugin interface.
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

//Capacity implements the limes.Plugin interface.
func (p *novaPlugin) Capacity(driver limes.Driver) (map[string]uint64, error) {
	//TODO implement
	return map[string]uint64{}, nil
}

func makeIntPointer(value int) *int {
	return &value
}
