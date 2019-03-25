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
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
)

type manilaPlugin struct {
	cfg core.ServiceConfiguration
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
	core.RegisterQuotaPlugin(func(c core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		return &manilaPlugin{c}
	})
}

//Init implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

//ServiceInfo implements the core.QuotaPlugin interface.
func (p *manilaPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "sharev2",
		ProductName: "manila",
		Area:        "storage",
	}
}

//Resources implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Resources() []limes.ResourceInfo {
	return manilaResources
}

type manilaUsage struct {
	ShareCount                uint64
	SnapshotCount             uint64
	ShareNetworkCount         uint64
	Gigabytes                 uint64
	GigabytesPhysical         *uint64
	SnapshotGigabytes         uint64
	SnapshotGigabytesPhysical *uint64
}

//Scrape implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string) (map[string]core.ResourceData, error) {
	client, err := openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return nil, err
	}

	var result gophercloud.Result

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

	usage, err := manilaCollectUsage(client, projectUUID)
	if err != nil {
		return nil, err
	}

	return map[string]core.ResourceData{
		"shares": {
			Quota: manilaQuotaData.QuotaSet.Shares,
			Usage: usage.ShareCount,
		},
		"share_snapshots": {
			Quota: manilaQuotaData.QuotaSet.Snapshots,
			Usage: usage.SnapshotCount,
		},
		"share_networks": {
			Quota: manilaQuotaData.QuotaSet.ShareNetworks,
			Usage: usage.ShareNetworkCount,
		},
		"share_capacity": {
			Quota:         manilaQuotaData.QuotaSet.Gigabytes,
			Usage:         usage.Gigabytes,
			PhysicalUsage: usage.GigabytesPhysical,
		},
		"snapshot_capacity": {
			Quota:         manilaQuotaData.QuotaSet.SnapshotGigabytes,
			Usage:         usage.SnapshotGigabytes,
			PhysicalUsage: usage.SnapshotGigabytesPhysical,
		},
	}, err
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *manilaPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	client, err := openstack.NewSharedFileSystemV2(provider, eo)
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

////////////////////////////////////////////////////////////////////////////////

func manilaCollectUsage(client *gophercloud.ServiceClient, projectUUID string) (result manilaUsage, err error) {
	shares, err := manilaGetShares(client, projectUUID)
	if err != nil {
		return manilaUsage{}, err
	}
	result.ShareCount = uint64(len(shares))
	for _, share := range shares {
		result.Gigabytes += share.Size
	}

	//Get usage of snapshots per project
	snapshots, err := manilaGetSnapshots(client, projectUUID)
	if err != nil {
		return manilaUsage{}, err
	}
	result.SnapshotCount = uint64(len(snapshots))
	for _, snapshot := range snapshots {
		result.SnapshotGigabytes += snapshot.ShareSize
	}

	//Get usage of shared networks
	err = sharenetworks.ListDetail(client, sharenetworks.ListOpts{ProjectID: projectUUID}).EachPage(func(page pagination.Page) (bool, error) {
		sn, err := sharenetworks.ExtractShareNetworks(page)
		if err != nil {
			return false, err
		}
		result.ShareNetworkCount += uint64(len(sn))
		return true, nil
	})
	if err != nil {
		return manilaUsage{}, err
	}

	return
}

type manilaShare struct {
	ID   string `json:"id"`
	Size uint64 `json:"size"`
}

func manilaGetShares(client *gophercloud.ServiceClient, projectUUID string) (result []manilaShare, err error) {
	page := 0
	pageSize := 250

	for {
		url := client.ServiceURL("shares", "detail") + fmt.Sprintf("?project_id=%s&all_tenants=1&limit=%d&offset=%d", projectUUID, pageSize, page*pageSize)
		var r gophercloud.Result
		_, err = client.Get(url, &r.Body, nil)
		if err != nil {
			return nil, err
		}

		var data struct {
			Shares []manilaShare `json:"shares"`
		}
		err = r.ExtractInto(&data)
		if err != nil {
			return nil, err
		}

		if len(data.Shares) > 0 {
			result = append(result, data.Shares...)
			page++
		} else {
			//last page reached
			return
		}
	}
}

type manilaSnapshot struct {
	ID        string `json:"id"`
	ShareSize uint64 `json:"share_size"`
}

func manilaGetSnapshots(client *gophercloud.ServiceClient, projectUUID string) (result []manilaSnapshot, err error) {
	page := 0
	pageSize := 250

	for {
		url := client.ServiceURL("snapshots", "detail") + fmt.Sprintf("?project_id=%s&all_tenants=1&limit=%d&offset=%d", projectUUID, pageSize, page*pageSize)
		var r gophercloud.Result
		_, err = client.Get(url, &r.Body, nil)
		if err != nil {
			return nil, err
		}

		var data struct {
			Snapshots []manilaSnapshot `json:"snapshots"`
		}
		err = r.ExtractInto(&data)
		if err != nil {
			return nil, err
		}

		if len(data.Snapshots) > 0 {
			result = append(result, data.Snapshots...)
			page++
		} else {
			//last page reached
			return
		}
	}
}
