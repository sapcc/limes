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
	"errors"
	"fmt"
	"math"

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

func init() {
	core.RegisterQuotaPlugin(func(c core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		return &manilaPlugin{c}
	})
}

//Init implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	if len(p.cfg.ShareV2.ShareTypes) == 0 {
		return errors.New("quota plugin sharev2: missing required configuration field sharev2.share_types")
	}
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
	result := make([]limes.ResourceInfo, 0, 1+4*len(p.cfg.ShareV2.ShareTypes))
	result = append(result, limes.ResourceInfo{
		Name:     "share_networks",
		Unit:     limes.UnitNone,
		Category: "sharev2",
	})
	for _, shareType := range p.cfg.ShareV2.ShareTypes {
		category := p.makeResourceName("sharev2", shareType)
		result = append(result,
			limes.ResourceInfo{
				Name:     p.makeResourceName("share_capacity", shareType),
				Unit:     limes.UnitGibibytes,
				Category: category,
			},
			limes.ResourceInfo{
				Name:     p.makeResourceName("shares", shareType),
				Unit:     limes.UnitNone,
				Category: category,
			},
			limes.ResourceInfo{
				Name:     p.makeResourceName("snapshot_capacity", shareType),
				Unit:     limes.UnitGibibytes,
				Category: category,
			},
			limes.ResourceInfo{
				Name:     p.makeResourceName("share_snapshots", shareType),
				Unit:     limes.UnitNone,
				Category: category,
			},
		)
	}
	return result
}

func (p *manilaPlugin) makeResourceName(kind, shareType string) string {
	if p.cfg.ShareV2.ShareTypes[0] == shareType {
		//the resources for the first share type don't get the share type suffix
		//for backwards compatibility reasons
		return kind
	}
	return kind + "_" + shareType
}

type manilaUsage struct {
	ShareCount                map[string]uint64
	SnapshotCount             map[string]uint64
	ShareNetworkCount         uint64
	Gigabytes                 map[string]uint64
	GigabytesPhysical         *uint64
	SnapshotGigabytes         map[string]uint64
	SnapshotGigabytesPhysical *uint64
}
type manilaQuotaSet struct {
	Gigabytes         int64 `json:"gigabytes"`
	Shares            int64 `json:"shares"`
	SnapshotGigabytes int64 `json:"snapshot_gigabytes"`
	Snapshots         int64 `json:"snapshots"`
	ShareNetworks     int64 `json:"share_networks"`
}

//Scrape implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string) (map[string]core.ResourceData, error) {
	client, err := openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return nil, err
	}
	client.Microversion = "2.39" //for share-type-specific quota

	quotaSets := make(map[string]manilaQuotaSet)
	for _, shareType := range p.cfg.ShareV2.ShareTypes {
		quotaSets[shareType], err = manilaCollectQuota(client, projectUUID, shareType)
		if err != nil {
			return nil, err
		}
	}

	//the share_networks quota is only shown when quering for no share_type in particular
	quotaSets[""], err = manilaCollectQuota(client, projectUUID, "")
	if err != nil {
		return nil, err
	}

	usage, err := manilaCollectUsage(client, projectUUID, p.cfg.ShareV2.ShareTypes)
	if err != nil {
		return nil, err
	}

	if p.cfg.ShareV2.PrometheusAPIConfig != nil {
		err := manilaCollectPhysicalUsage(&usage, projectUUID, p.cfg.ShareV2.PrometheusAPIConfig)
		if err != nil {
			return nil, err
		}
	}

	result := map[string]core.ResourceData{
		"share_networks": {
			Quota: quotaSets[""].ShareNetworks,
			Usage: usage.ShareNetworkCount,
		},
	}
	for idx, shareType := range p.cfg.ShareV2.ShareTypes {
		gigabytesPhysical := (*uint64)(nil)
		snapshotGigabytesPhysical := (*uint64)(nil)
		if idx == 0 {
			//FIXME: report physical usage per share type
			gigabytesPhysical = usage.GigabytesPhysical
			snapshotGigabytesPhysical = usage.SnapshotGigabytesPhysical
		}

		result[p.makeResourceName("shares", shareType)] = core.ResourceData{
			Quota: quotaSets[shareType].Shares,
			Usage: usage.ShareCount[shareType],
		}
		result[p.makeResourceName("share_snapshots", shareType)] = core.ResourceData{
			Quota: quotaSets[shareType].Snapshots,
			Usage: usage.SnapshotCount[shareType],
		}
		result[p.makeResourceName("share_capacity", shareType)] = core.ResourceData{
			Quota:         quotaSets[shareType].Gigabytes,
			Usage:         usage.Gigabytes[shareType],
			PhysicalUsage: gigabytesPhysical,
		}
		result[p.makeResourceName("share_snapshot_capacity", shareType)] = core.ResourceData{
			Quota:         quotaSets[shareType].SnapshotGigabytes,
			Usage:         usage.SnapshotGigabytes[shareType],
			PhysicalUsage: snapshotGigabytesPhysical,
		}
	}
	return result, nil
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *manilaPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	client, err := openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return err
	}
	client.Microversion = "2.39" //for share-type-specific quota

	expect200 := &gophercloud.RequestOpts{OkCodes: []int{200}}
	overallQuotas := manilaQuotaSet{
		ShareNetworks: int64(quotas["share_networks"]),
	}

	for _, shareType := range p.cfg.ShareV2.ShareTypes {
		quotasForType := manilaQuotaSet{
			Shares:            int64(quotas[p.makeResourceName("shares", shareType)]),
			Gigabytes:         int64(quotas[p.makeResourceName("share_capacity", shareType)]),
			Snapshots:         int64(quotas[p.makeResourceName("share_snapshots", shareType)]),
			SnapshotGigabytes: int64(quotas[p.makeResourceName("snapshot_capacity", shareType)]),
		}

		url := client.ServiceURL("quota-sets", projectUUID) + "?share_type=" + shareType
		_, err = client.Put(url, map[string]interface{}{"quota_set": quotasForType}, nil, expect200)
		if err != nil {
			return fmt.Errorf("could not set quotas for share type %q: %s", shareType, err.Error())
		}

		overallQuotas.Shares += quotasForType.Shares
		overallQuotas.Gigabytes += quotasForType.Gigabytes
		overallQuotas.Snapshots += quotasForType.Snapshots
		overallQuotas.SnapshotGigabytes += quotasForType.SnapshotGigabytes
	}

	url := client.ServiceURL("quota-sets", projectUUID)
	_, err = client.Put(url, map[string]interface{}{"quota_set": overallQuotas}, nil, expect200)
	return err
}

////////////////////////////////////////////////////////////////////////////////

func manilaCollectQuota(client *gophercloud.ServiceClient, projectUUID string, shareType string) (manilaQuotaSet, error) {
	var result gophercloud.Result
	url := client.ServiceURL("quota-sets", projectUUID)
	if shareType != "" {
		url += "?share_type=" + shareType
	}
	_, err := client.Get(url, &result.Body, nil)
	if err != nil {
		return manilaQuotaSet{}, err
	}

	var manilaQuotaData struct {
		QuotaSet manilaQuotaSet `json:"quota_set"`
	}
	err = result.ExtractInto(&manilaQuotaData)
	return manilaQuotaData.QuotaSet, err
}

////////////////////////////////////////////////////////////////////////////////

func manilaCollectUsage(client *gophercloud.ServiceClient, projectUUID string, shareTypes []string) (result manilaUsage, err error) {
	result = manilaUsage{
		ShareCount:        make(map[string]uint64, len(shareTypes)),
		SnapshotCount:     make(map[string]uint64, len(shareTypes)),
		Gigabytes:         make(map[string]uint64, len(shareTypes)),
		SnapshotGigabytes: make(map[string]uint64, len(shareTypes)),
	}
	for _, shareType := range shareTypes {
		result.ShareCount[shareType] = 0
		result.SnapshotCount[shareType] = 0
		result.Gigabytes[shareType] = 0
		result.SnapshotGigabytes[shareType] = 0
	}

	shares, err := manilaGetShares(client, projectUUID)
	if err != nil {
		return manilaUsage{}, err
	}
	shareTypeByID := make(map[string]string, len(shares))
	for _, share := range shares {
		shareType := share.Type
		_, knownShareType := result.ShareCount[shareType]
		if !knownShareType {
			//group shares with unknown share type into the default share type
			shareType = shareTypes[0]
		}

		shareTypeByID[share.ID] = shareType
		result.ShareCount[shareType]++
		result.Gigabytes[shareType] += share.Size
	}

	//Get usage of snapshots per project
	snapshots, err := manilaGetSnapshots(client, projectUUID)
	if err != nil {
		return manilaUsage{}, err
	}
	for _, snapshot := range snapshots {
		shareType, knownShareType := shareTypeByID[snapshot.ShareID]
		if !knownShareType {
			//group snapshots with invalid share reference into the default share type
			shareType = shareTypes[0]
		}
		result.SnapshotCount[shareType]++
		result.SnapshotGigabytes[shareType] += snapshot.ShareSize
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
	Type string `json:"share_type_name"`
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
	ShareID   string `json:"share_id"`
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

func manilaCollectPhysicalUsage(usage *manilaUsage, projectUUID string, promAPIConfig *core.PrometheusAPIConfiguration) error {
	client, err := prometheusClient(*promAPIConfig)
	if err != nil {
		return err
	}

	roundUp := func(bytes float64) *uint64 {
		gigabytes := uint64(math.Ceil(bytes / (1 << 30)))
		return &gigabytes
	}
	defaultValue := float64(0)

	//NOTE: The `max by (share_id)` is necessary for when a share is being
	//migrated to another shareserver and thus appears in the metrics twice.
	queryStr := fmt.Sprintf(
		`sum(max by (share_id) (netapp_volume_used_bytes{project_id=%q}))`,
		projectUUID,
	)
	bytesPhysical, err := prometheusGetSingleValue(client, queryStr, &defaultValue)
	if err != nil {
		return err
	}
	usage.GigabytesPhysical = roundUp(bytesPhysical)

	queryStr = fmt.Sprintf(
		`sum(max by (share_id) (netapp_volume_snapshot_used_bytes{project_id=%q}))`,
		projectUUID,
	)
	snapshotBytesPhysical, err := prometheusGetSingleValue(client, queryStr, &defaultValue)
	if err != nil {
		return err
	}
	usage.SnapshotGigabytesPhysical = roundUp(snapshotBytesPhysical)

	return nil
}
