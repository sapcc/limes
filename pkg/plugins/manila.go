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
	"fmt"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/sharedfilesystems/v2/sharenetworks"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

type manilaPlugin struct {
	cfg limes.ServiceConfiguration
}

var manilaResources = []limes.ResourceInfo{
	{
		Name: "share_networks",
		Unit: limes.UnitNone,
	},
	{
		Name: "share_capacity",
		Unit: limes.UnitGibibytes,
	},
	{
		Name: "shares",
		Unit: limes.UnitNone,
	},
	{
		Name: "snapshot_capacity",
		Unit: limes.UnitGibibytes,
	},
	{
		Name: "share_snapshots",
		Unit: limes.UnitNone,
	},
}

func init() {
	limes.RegisterQuotaPlugin(func(c limes.ServiceConfiguration) limes.QuotaPlugin {
		return &manilaPlugin{c}
	})
}

//ServiceInfo implements the limes.QuotaPlugin interface.
func (p *manilaPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type: "sharev2",
		Area: "storage",
	}
}

//Resources implements the limes.QuotaPlugin interface.
func (p *manilaPlugin) Resources() []limes.ResourceInfo {
	return manilaResources
}

func (p *manilaPlugin) Client(driver limes.Driver) (*gophercloud.ServiceClient, error) {
	return openstack.NewSharedFileSystemV2(driver.Client(),
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
}

//Scrape implements the limes.QuotaPlugin interface.
func (p *manilaPlugin) Scrape(driver limes.Driver, domainUUID, projectUUID string) (map[string]limes.ResourceData, error) {
	client, err := p.Client(driver)
	if err != nil {
		return nil, err
	}

	var result gophercloud.Result
	var totalShareUsage, totalSnapshotUsage, totalShareNetworksUsage = uint64(0), uint64(0), uint64(0)

	//Get absolute quota limits per project
	url := client.ServiceURL("os-quota-sets", projectUUID)
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}
	var manilaQuotaData struct {
		QuotaSet struct {
			Gigabytes         int64 `json:"gigabytes"`
			Shares            int64 `json:"shares"`
			SnapshotGigabytes int64 `json:"snapshot_gigabytes"`
			Snapshots         int64 `json:"snapshots"`
			ShareNetworks     int64 `json:"share_networks"`
		} `json:"quota_set"`
	}
	err = result.ExtractInto(&manilaQuotaData)
	if err != nil {
		return nil, err
	}

	//Get usage of shares per project
	url = client.ServiceURL("shares", "detail") + fmt.Sprintf("?project_id=%s&all_tenants=1", projectUUID)
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	var manilaShareUsageData struct {
		Shares []struct {
			Size uint64 `json:"size"`
		} `json:"shares"`
	}
	err = result.ExtractInto(&manilaShareUsageData)
	if err != nil {
		return nil, err
	}

	for _, element := range manilaShareUsageData.Shares {
		totalShareUsage += element.Size
	}

	//Get usage of snapshots per project
	url = client.ServiceURL("snapshots", "detail") + fmt.Sprintf("?project_id=%s&all_tenants=1", projectUUID)
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	var manilaSnapshotUsageData struct {
		Snapshots []struct {
			ShareSize uint64 `json:"share_size"`
		} `json:"snapshots"`
	}
	err = result.ExtractInto(&manilaSnapshotUsageData)
	if err != nil {
		return nil, err
	}

	for _, element := range manilaSnapshotUsageData.Snapshots {
		totalSnapshotUsage += element.ShareSize
	}

	//Get usage of shared networks
	pages := 0
	sharenetworks.ListDetail(client, sharenetworks.ListOpts{ProjectID: projectUUID}).EachPage(func(page pagination.Page) (bool, error) {
		pages++
		sn, err := sharenetworks.ExtractShareNetworks(page)
		if err != nil {
			return false, err
		}
		totalShareNetworksUsage = uint64(len(sn))
		return true, nil
	})

	util.LogDebug("Scraped quota and usage for service: sharev2.")

	return map[string]limes.ResourceData{
		"shares": {
			Quota: manilaQuotaData.QuotaSet.Shares,
			Usage: uint64(len(manilaShareUsageData.Shares)),
		},
		"share_snapshots": {
			Quota: manilaQuotaData.QuotaSet.Snapshots,
			Usage: uint64(len(manilaSnapshotUsageData.Snapshots)),
		},
		"share_networks": {
			Quota: manilaQuotaData.QuotaSet.ShareNetworks,
			Usage: uint64(totalShareNetworksUsage),
		},
		"share_capacity": {
			Quota: manilaQuotaData.QuotaSet.Gigabytes,
			Usage: uint64(totalShareUsage),
		},
		"snapshot_capacity": {
			Quota: manilaQuotaData.QuotaSet.SnapshotGigabytes,
			Usage: uint64(totalSnapshotUsage),
		},
	}, err
}

//SetQuota implements the limes.QuotaPlugin interface.
func (p *manilaPlugin) SetQuota(driver limes.Driver, domainUUID, projectUUID string, quotas map[string]uint64) error {
	client, err := p.Client(driver)
	if err != nil {
		return err
	}

	requestData := map[string]map[string]uint64{
		"quota_set": {
			"gigabytes":          quotas["share_capacity"],
			"snapshots":          quotas["share_snapshots"],
			"snapshot_gigabytes": quotas["snapshot_capacity"],
			"shares":             quotas["shares"],
			"share_networks":     quotas["share_networks"],
		},
	}

	url := client.ServiceURL("os-quota-sets", projectUUID)
	_, err = client.Put(url, requestData, nil, &gophercloud.RequestOpts{OkCodes: []int{200}})

	return err
}
