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
	"math"
	"math/big"
	"regexp"
	"strconv"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/sharedfilesystems/apiversions"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
)

type manilaPlugin struct {
	cfg              core.ServiceConfiguration
	hasReplicaQuotas bool
}

func init() {
	core.RegisterQuotaPlugin(func(c core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		return &manilaPlugin{c, false}
	})
}

//Init implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	if len(p.cfg.ShareV2.ShareTypes) == 0 {
		return errors.New("quota plugin sharev2: missing required configuration field sharev2.share_types")
	}

	client, err := openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return err
	}
	microversion, err := p.findMicroversion(client)
	if err != nil {
		return err
	}
	if microversion == 0 {
		return errors.New(`cannot find API microversion: no version of the form "2.x" found in advertisement`)
	}
	p.hasReplicaQuotas = microversion >= 53

	return nil
}

func (p *manilaPlugin) findMicroversion(client *gophercloud.ServiceClient) (int, error) {
	pager, err := apiversions.List(client).AllPages()
	if err != nil {
		return 0, err
	}
	versions, err := apiversions.ExtractAPIVersions(pager)
	if err != nil {
		return 0, err
	}

	versionRx := regexp.MustCompile(`^2\.(\d+)$`)
	for _, version := range versions {
		match := versionRx.FindStringSubmatch(version.Version)
		if match != nil {
			return strconv.Atoi(match[1])
		}
	}

	//no 2.x version found at all
	return 0, nil
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
		stName := shareType.Name
		category := p.makeResourceName("sharev2", stName)
		result = append(result,
			limes.ResourceInfo{
				Name:     p.makeResourceName("share_capacity", stName),
				Unit:     limes.UnitGibibytes,
				Category: category,
			},
			limes.ResourceInfo{
				Name:     p.makeResourceName("shares", stName),
				Unit:     limes.UnitNone,
				Category: category,
			},
			limes.ResourceInfo{
				Name:     p.makeResourceName("snapshot_capacity", stName),
				Unit:     limes.UnitGibibytes,
				Category: category,
			},
			limes.ResourceInfo{
				Name:     p.makeResourceName("share_snapshots", stName),
				Unit:     limes.UnitNone,
				Category: category,
			},
		)
	}
	return result
}

//Rates implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Rates() []limes.RateInfo {
	return nil
}

func (p *manilaPlugin) makeResourceName(kind, shareTypeName string) string {
	if p.cfg.ShareV2.ShareTypes[0].Name == shareTypeName {
		//the resources for the first share type don't get the share type suffix
		//for backwards compatibility reasons
		return kind
	}
	return kind + "_" + shareTypeName
}

type manilaQuotaSet struct {
	Gigabytes           uint64  `json:"gigabytes"`
	Shares              uint64  `json:"shares"`
	SnapshotGigabytes   uint64  `json:"snapshot_gigabytes"`
	Snapshots           uint64  `json:"snapshots"`
	ReplicaGigabytes    uint64  `json:"-"`
	Replicas            uint64  `json:"-"`
	ShareNetworks       *uint64 `json:"share_networks,omitempty"`
	ReplicaGigabytesPtr *uint64 `json:"replica_gigabytes,omitempty"`
	ReplicasPtr         *uint64 `json:"share_replicas,omitempty"`
}

//ScrapeRates implements the core.QuotaPlugin interface.
func (p *manilaPlugin) ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

//Scrape implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string) (map[string]core.ResourceData, error) {
	client, err := openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return nil, err
	}
	//share-type-specific quotas need 2.39, replica quotas need 2.53
	if p.hasReplicaQuotas {
		client.Microversion = "2.53"
	} else {
		client.Microversion = "2.39"
	}

	quotaSets := make(map[string]manilaQuotaSetDetail)
	for _, shareType := range p.cfg.ShareV2.ShareTypes {
		quotaSets[shareType.Name], err = manilaCollectQuota(client, projectUUID, shareType.Name)
		if err != nil {
			return nil, err
		}
	}

	//the share_networks quota is only shown when querying for no share_type in particular
	quotaSets[""], err = manilaCollectQuota(client, projectUUID, "")
	if err != nil {
		return nil, err
	}

	var physUsage manilaPhysicalUsage
	if p.cfg.ShareV2.PrometheusAPIConfig != nil {
		physUsage, err = p.collectPhysicalUsage(projectUUID)
		if err != nil {
			return nil, err
		}
	}

	result := map[string]core.ResourceData{
		"share_networks": quotaSets[""].ShareNetworks.ToResourceData(nil),
	}
	for idx, shareType := range p.cfg.ShareV2.ShareTypes {
		stName := shareType.Name
		gigabytesPhysical := (*uint64)(nil)
		snapshotGigabytesPhysical := (*uint64)(nil)
		if idx == 0 {
			if val, exists := physUsage.Gigabytes[stName]; exists {
				gigabytesPhysical = &val
			}
			if val, exists := physUsage.SnapshotGigabytes[stName]; exists {
				snapshotGigabytesPhysical = &val
			}
		}

		sharesData := quotaSets[stName].Shares.ToResourceData(nil)
		shareCapacityData := quotaSets[stName].Gigabytes.ToResourceData(gigabytesPhysical)
		if p.hasReplicaQuotas && shareType.ReplicationEnabled {
			sharesData.Usage = quotaSets[stName].Replicas.Usage
			shareCapacityData.Usage = quotaSets[stName].ReplicaGigabytes.Usage
		}

		result[p.makeResourceName("shares", stName)] = sharesData
		result[p.makeResourceName("share_capacity", stName)] = shareCapacityData
		result[p.makeResourceName("share_snapshots", stName)] = quotaSets[stName].Snapshots.ToResourceData(nil)
		result[p.makeResourceName("snapshot_capacity", stName)] = quotaSets[stName].SnapshotGigabytes.ToResourceData(snapshotGigabytesPhysical)
	}
	return result, nil
}

func derefOrZero(val *int64) int64 {
	if val == nil {
		return 0
	}
	return *val
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *manilaPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	client, err := openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return err
	}
	client.Microversion = "2.39" //for share-type-specific quota
	expect200 := &gophercloud.RequestOpts{OkCodes: []int{200}}

	//General note: Even though it complicates the code, we need to set overall
	//quotas first, otherwise share-type-specific quotas may get rejected for not
	//fitting in the overall quota.

	shareNetworkQuota := quotas["share_networks"]
	overallQuotas := manilaQuotaSet{
		ShareNetworks: &shareNetworkQuota,
	}
	shareTypeQuotas := make(map[string]manilaQuotaSet)
	anyReplicationEnabled := false

	for _, shareType := range p.cfg.ShareV2.ShareTypes {
		stName := shareType.Name
		quotasForType := manilaQuotaSet{
			Shares:            quotas[p.makeResourceName("shares", stName)],
			Gigabytes:         quotas[p.makeResourceName("share_capacity", stName)],
			Snapshots:         quotas[p.makeResourceName("share_snapshots", stName)],
			SnapshotGigabytes: quotas[p.makeResourceName("snapshot_capacity", stName)],
			Replicas:          0,
			ReplicaGigabytes:  0,
			ShareNetworks:     nil,
		}
		if p.hasReplicaQuotas && shareType.ReplicationEnabled {
			anyReplicationEnabled = true
			quotasForType.Replicas = quotasForType.Shares
			quotasForType.ReplicaGigabytes = quotasForType.Gigabytes
			quotasForType.ReplicasPtr = &quotasForType.Replicas
			quotasForType.ReplicaGigabytesPtr = &quotasForType.ReplicaGigabytes
		}
		shareTypeQuotas[stName] = quotasForType

		overallQuotas.Shares += quotasForType.Shares
		overallQuotas.Gigabytes += quotasForType.Gigabytes
		overallQuotas.Snapshots += quotasForType.Snapshots
		overallQuotas.SnapshotGigabytes += quotasForType.SnapshotGigabytes
		overallQuotas.Replicas += quotasForType.Replicas
		overallQuotas.ReplicaGigabytes += quotasForType.ReplicaGigabytes
	}

	if p.hasReplicaQuotas && anyReplicationEnabled {
		overallQuotas.ReplicasPtr = &overallQuotas.Replicas
		overallQuotas.ReplicaGigabytesPtr = &overallQuotas.ReplicaGigabytes
	}

	url := client.ServiceURL("quota-sets", projectUUID)
	logDebugSetQuota(projectUUID, "overall", overallQuotas)
	_, err = client.Put(url, map[string]interface{}{"quota_set": overallQuotas}, nil, expect200)
	if err != nil {
		return fmt.Errorf("could not set overall share quotas: %s", err.Error())
	}

	for shareTypeName, quotasForType := range shareTypeQuotas {
		logDebugSetQuota(projectUUID, shareTypeName, quotasForType)
		url := client.ServiceURL("quota-sets", projectUUID) + "?share_type=" + shareTypeName
		_, err = client.Put(url, map[string]interface{}{"quota_set": quotasForType}, nil, expect200)
		if err != nil {
			return fmt.Errorf("could not set quotas for share type %q: %s", shareTypeName, err.Error())
		}
	}

	return nil
}

func logDebugSetQuota(projectUUID, shareTypeName string, quotas manilaQuotaSet) {
	if logg.ShowDebug {
		buf, _ := json.Marshal(quotas)
		logg.Debug("manila: PUT quota-sets %s %s: %s", projectUUID, shareTypeName, string(buf))
	}
}

////////////////////////////////////////////////////////////////////////////////

type manilaQuotaSetDetail struct {
	Gigabytes         manilaQuotaDetail `json:"gigabytes"`
	Shares            manilaQuotaDetail `json:"shares"`
	SnapshotGigabytes manilaQuotaDetail `json:"snapshot_gigabytes"`
	Snapshots         manilaQuotaDetail `json:"snapshots"`
	ReplicaGigabytes  manilaQuotaDetail `json:"replica_gigabytes"`
	Replicas          manilaQuotaDetail `json:"share_replicas"`
	ShareNetworks     manilaQuotaDetail `json:"share_networks,omitempty"`
}

type manilaQuotaDetail struct {
	Quota int64  `json:"limit"`
	Usage uint64 `json:"in_use"`
}

func (q manilaQuotaDetail) ToResourceData(physicalUsage *uint64) core.ResourceData {
	return core.ResourceData{
		Quota:         q.Quota,
		Usage:         q.Usage,
		PhysicalUsage: physicalUsage,
	}
}

func manilaCollectQuota(client *gophercloud.ServiceClient, projectUUID string, shareTypeName string) (manilaQuotaSetDetail, error) {
	var result gophercloud.Result
	url := client.ServiceURL("quota-sets", projectUUID, "detail")
	if shareTypeName != "" {
		url += "?share_type=" + shareTypeName
	}
	_, err := client.Get(url, &result.Body, nil)
	if err != nil {
		return manilaQuotaSetDetail{}, err
	}

	var manilaQuotaData struct {
		QuotaSet manilaQuotaSetDetail `json:"quota_set"`
	}
	err = result.ExtractInto(&manilaQuotaData)
	return manilaQuotaData.QuotaSet, err
}

////////////////////////////////////////////////////////////////////////////////

type manilaPhysicalUsage struct {
	Gigabytes         map[string]uint64
	SnapshotGigabytes map[string]uint64
}

func (p *manilaPlugin) collectPhysicalUsage(projectUUID string) (manilaPhysicalUsage, error) {
	usage := manilaPhysicalUsage{
		Gigabytes:         make(map[string]uint64),
		SnapshotGigabytes: make(map[string]uint64),
	}

	client, err := prometheusClient(*p.cfg.ShareV2.PrometheusAPIConfig)
	if err != nil {
		return manilaPhysicalUsage{}, err
	}

	roundUp := func(bytes float64) uint64 {
		return uint64(math.Ceil(bytes / (1 << 30)))
	}
	defaultValue := float64(0)

	for _, shareType := range p.cfg.ShareV2.ShareTypes {
		stName := shareType.Name

		//NOTE: The `max by (share_id)` is necessary for when a share is being
		//migrated to another shareserver and thus appears in the metrics twice.
		queryStr := fmt.Sprintf(
			`sum(max by (share_id) (netapp_volume_used_bytes{project_id=%q,share_type=%q}))`,
			projectUUID, stName,
		)
		bytesPhysical, err := prometheusGetSingleValue(client, queryStr, &defaultValue)
		if err != nil {
			return manilaPhysicalUsage{}, err
		}
		usage.Gigabytes[stName] = roundUp(bytesPhysical)

		queryStr = fmt.Sprintf(
			`sum(max by (share_id) (netapp_volume_snapshot_used_bytes{project_id=%q,share_type=%q}))`,
			projectUUID, stName,
		)
		snapshotBytesPhysical, err := prometheusGetSingleValue(client, queryStr, &defaultValue)
		if err != nil {
			return manilaPhysicalUsage{}, err
		}
		usage.SnapshotGigabytes[stName] = roundUp(snapshotBytesPhysical)
	}

	return usage, nil
}
