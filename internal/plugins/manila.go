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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/promquery"

	"github.com/sapcc/limes/internal/core"
)

type manilaPlugin struct {
	ShareTypes          []ManilaShareTypeSpec `yaml:"share_types"`
	PrometheusAPIConfig *promquery.Config     `yaml:"prometheus_api"`
	hasReplicaQuotas    bool                  `yaml:"-"`
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &manilaPlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubresources map[string]bool) error {
	if len(p.ShareTypes) == 0 {
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
	logg.Info("Manila microversion = 2.%d", microversion)
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

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *manilaPlugin) PluginTypeID() string {
	return "sharev2"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *manilaPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "sharev2",
		ProductName: "manila",
		Area:        "storage",
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Resources() []limesresources.ResourceInfo {
	result := make([]limesresources.ResourceInfo, 0, 1+4*len(p.ShareTypes))
	result = append(result, limesresources.ResourceInfo{
		Name:     "share_networks",
		Unit:     limes.UnitNone,
		Category: "sharev2",
	})
	for _, shareType := range p.ShareTypes {
		category := p.makeResourceName("sharev2", shareType)
		result = append(result,
			limesresources.ResourceInfo{
				Name:     p.makeResourceName("share_capacity", shareType),
				Unit:     limes.UnitGibibytes,
				Category: category,
			},
			limesresources.ResourceInfo{
				Name:     p.makeResourceName("shares", shareType),
				Unit:     limes.UnitNone,
				Category: category,
			},
			limesresources.ResourceInfo{
				Name:     p.makeResourceName("snapshot_capacity", shareType),
				Unit:     limes.UnitGibibytes,
				Category: category,
			},
			limesresources.ResourceInfo{
				Name:     p.makeResourceName("share_snapshots", shareType),
				Unit:     limes.UnitNone,
				Category: category,
			},
		)
		if p.PrometheusAPIConfig != nil {
			result = append(result, limesresources.ResourceInfo{
				Name:     p.makeResourceName("snapmirror_capacity", shareType),
				Unit:     limes.UnitGibibytes,
				Category: category,
				NoQuota:  true,
			})
		}
	}
	return result
}

// Rates implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Rates() []limesrates.RateInfo {
	return nil
}

func (p *manilaPlugin) makeResourceName(kind string, shareType ManilaShareTypeSpec) string {
	if p.ShareTypes[0].Name == shareType.Name {
		//the resources for the first share type don't get the share type suffix
		//for backwards compatibility reasons
		return kind
	}
	return kind + "_" + shareType.Name
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

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *manilaPlugin) ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject) (result map[string]core.ResourceData, serializedMetrics string, err error) {
	client, err := openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return nil, "", err
	}
	//share-type-specific quotas need 2.39, replica quotas need 2.53
	if p.hasReplicaQuotas {
		client.Microversion = "2.53"
	} else {
		client.Microversion = "2.39"
	}

	quotaSets := make(map[string]manilaQuotaSetDetail)
	for _, shareType := range p.ShareTypes {
		stName := resolveManilaShareType(shareType, project)
		if stName == "" {
			continue
		}
		quotaSets[stName], err = manilaCollectQuota(client, project.UUID, stName)
		if err != nil {
			return nil, "", err
		}
	}

	//the share_networks quota is only shown when querying for no share_type in particular
	quotaSets[""], err = manilaCollectQuota(client, project.UUID, "")
	if err != nil {
		return nil, "", err
	}

	var physUsage manilaPhysicalUsage
	if p.PrometheusAPIConfig != nil {
		physUsage, err = p.collectPhysicalUsage(project)
		if err != nil {
			return nil, "", err
		}
	}

	result = map[string]core.ResourceData{
		"share_networks": quotaSets[""].ShareNetworks.ToResourceData(nil),
	}
	for idx, shareType := range p.ShareTypes {
		stName := resolveManilaShareType(shareType, project)
		if stName == "" {
			result[p.makeResourceName("shares", shareType)] = core.ResourceData{Quota: 0, Usage: 0}
			result[p.makeResourceName("share_capacity", shareType)] = core.ResourceData{Quota: 0, Usage: 0}
			result[p.makeResourceName("share_snapshots", shareType)] = core.ResourceData{Quota: 0, Usage: 0}
			result[p.makeResourceName("snapshot_capacity", shareType)] = core.ResourceData{Quota: 0, Usage: 0}
			if p.PrometheusAPIConfig != nil {
				result[p.makeResourceName("snapmirror_capacity", shareType)] = core.ResourceData{Quota: 0, Usage: 0}
			}
			continue
		}

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
			//if share quotas and replica quotas disagree, report quota = -1 to force Limes to reapply the replica quota
			if quotaSets[stName].Replicas.Quota != sharesData.Quota {
				logg.Info("found mismatch between share quota (%d) and replica quota (%d) for share type %q in project %s",
					sharesData.Quota, quotaSets[stName].Replicas.Quota, stName, project.UUID)
				sharesData.Quota = -1
			}
			if quotaSets[stName].ReplicaGigabytes.Quota != shareCapacityData.Quota {
				logg.Info("found mismatch between share capacity quota (%d) and replica capacity quota (%d) for share type %q in project %s",
					shareCapacityData.Quota, quotaSets[stName].ReplicaGigabytes.Quota, stName, project.UUID)
				shareCapacityData.Quota = -1
			}
		}

		result[p.makeResourceName("shares", shareType)] = sharesData
		result[p.makeResourceName("share_capacity", shareType)] = shareCapacityData
		result[p.makeResourceName("share_snapshots", shareType)] = quotaSets[stName].Snapshots.ToResourceData(nil)
		result[p.makeResourceName("snapshot_capacity", shareType)] = quotaSets[stName].SnapshotGigabytes.ToResourceData(snapshotGigabytesPhysical)

		if p.PrometheusAPIConfig != nil {
			result[p.makeResourceName("snapmirror_capacity", shareType)], err = p.collectSnapmirrorUsage(project, stName)
			if err != nil {
				return nil, "", err
			}
		}
	}
	return result, "", nil
}

// IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *manilaPlugin) IsQuotaAcceptableForProject(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	//check if an inaccessible share type is used
	for _, shareType := range p.ShareTypes {
		stName := resolveManilaShareType(shareType, project)
		if stName == "" {
			for _, kind := range []string{"shares", "share_capacity", "share_snapshots", "snapshot_capacity"} {
				if quotas[p.makeResourceName(kind, shareType)] > 0 {
					return fmt.Errorf("share type %q may not be used in this project", shareType.Name)
				}
			}
		}
	}

	return nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *manilaPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	err := p.IsQuotaAcceptableForProject(provider, eo, project, quotas)
	if err != nil {
		return err
	}

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

	for _, shareType := range p.ShareTypes {
		stName := resolveManilaShareType(shareType, project)
		if stName == "" {
			//NOTE: In this case, we already know that all quotas for this share type
			//are 0 since we called IsQuotaAcceptableForProject at the start of this
			//function. So we are guaranteed to not ignore non-zero quotas here.
			continue
		}

		quotasForType := manilaQuotaSet{
			Shares:            quotas[p.makeResourceName("shares", shareType)],
			Gigabytes:         quotas[p.makeResourceName("share_capacity", shareType)],
			Snapshots:         quotas[p.makeResourceName("share_snapshots", shareType)],
			SnapshotGigabytes: quotas[p.makeResourceName("snapshot_capacity", shareType)],
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

	url := client.ServiceURL("quota-sets", project.UUID)
	logDebugSetQuota(project.UUID, "overall", overallQuotas)
	_, err = client.Put(url, map[string]interface{}{"quota_set": overallQuotas}, nil, expect200) //nolint:bodyclose // already closed by gophercloud
	if err != nil {
		return fmt.Errorf("could not set overall share quotas: %s", err.Error())
	}

	for shareTypeName, quotasForType := range shareTypeQuotas {
		logDebugSetQuota(project.UUID, shareTypeName, quotasForType)
		url := client.ServiceURL("quota-sets", project.UUID) + "?share_type=" + shareTypeName
		_, err = client.Put(url, map[string]interface{}{"quota_set": quotasForType}, nil, expect200) //nolint:bodyclose // already closed by gophercloud
		if err != nil {
			return fmt.Errorf("could not set quotas for share type %q: %s", shareTypeName, err.Error())
		}
	}

	return nil
}

func logDebugSetQuota(projectUUID, shareTypeName string, quotas manilaQuotaSet) {
	if logg.ShowDebug {
		if buf, err := json.Marshal(quotas); err == nil {
			logg.Debug("manila: PUT quota-sets %s %s: %s", projectUUID, shareTypeName, string(buf))
		} else {
			logg.Error("manila: could not marshal quota-sets %s %s in PUT request: %s", projectUUID, shareTypeName, err.Error())
		}
	}
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *manilaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *manilaPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics string) error {
	//not used by this plugin
	return nil
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

func manilaCollectQuota(client *gophercloud.ServiceClient, projectUUID, shareTypeName string) (manilaQuotaSetDetail, error) {
	var result gophercloud.Result
	url := client.ServiceURL("quota-sets", projectUUID, "detail")
	if shareTypeName != "" {
		url += "?share_type=" + shareTypeName
	}
	_, err := client.Get(url, &result.Body, nil) //nolint:bodyclose // already closed by gophercloud
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

func (p *manilaPlugin) collectPhysicalUsage(project core.KeystoneProject) (manilaPhysicalUsage, error) {
	usage := manilaPhysicalUsage{
		Gigabytes:         make(map[string]uint64),
		SnapshotGigabytes: make(map[string]uint64),
	}

	client, err := p.PrometheusAPIConfig.Connect()
	if err != nil {
		return manilaPhysicalUsage{}, err
	}

	defaultValue := float64(0)

	for _, shareType := range p.ShareTypes {
		stName := resolveManilaShareType(shareType, project)
		if stName == "" {
			continue
		}

		//NOTE: The `max by (share_id)` is necessary for when a share is being
		//migrated to another shareserver and thus appears in the metrics twice.
		queryStr := fmt.Sprintf(
			`sum(max by (share_id) (netapp_volume_used_bytes{project_id=%q,volume_type!="dp",share_type=%q}))`,
			project.UUID, stName,
		)
		bytesPhysical, err := client.GetSingleValue(queryStr, &defaultValue)
		if err != nil {
			return manilaPhysicalUsage{}, err
		}
		usage.Gigabytes[stName] = roundUpIntoGigabytes(bytesPhysical)

		queryStr = fmt.Sprintf(
			`sum(max by (share_id) (netapp_volume_snapshot_used_bytes{project_id=%q,volume_type!="dp",share_type=%q}))`,
			project.UUID, stName,
		)
		snapshotBytesPhysical, err := client.GetSingleValue(queryStr, &defaultValue)
		if err != nil {
			return manilaPhysicalUsage{}, err
		}
		usage.SnapshotGigabytes[stName] = roundUpIntoGigabytes(snapshotBytesPhysical)
	}

	return usage, nil
}

func (p *manilaPlugin) collectSnapmirrorUsage(project core.KeystoneProject, shareTypeName string) (core.ResourceData, error) {
	client, err := p.PrometheusAPIConfig.Connect()
	if err != nil {
		return core.ResourceData{}, err
	}

	//snapmirror backups are only known to the underlying NetApp filer, not to
	//Manila itself, so we have to collect usage from NetApp metrics instead
	defaultValue := float64(0)
	queryStr := fmt.Sprintf(
		`sum(max by (volume) (netapp_volume_total_bytes{project_id="%s",share_type="%s",volume_type="dp",volume=~".*EC2BKP"}))`,
		project.UUID, shareTypeName,
	)
	bytesTotal, err := client.GetSingleValue(queryStr, &defaultValue)
	if err != nil {
		return core.ResourceData{}, err
	}

	queryStr = fmt.Sprintf(
		`sum(max by (volume) (netapp_volume_used_bytes{project_id="%s",share_type="%s",volume_type="dp",volume=~".*EC2BKP"}))`,
		project.UUID, shareTypeName,
	)
	bytesUsed, err := client.GetSingleValue(queryStr, &defaultValue)
	if err != nil {
		return core.ResourceData{}, err
	}
	bytesUsedAsUint64 := roundUpIntoGigabytes(bytesUsed)

	return core.ResourceData{
		Quota:         0, //NoQuota = true
		Usage:         roundUpIntoGigabytes(bytesTotal),
		PhysicalUsage: &bytesUsedAsUint64,
	}, nil
}

func roundUpIntoGigabytes(bytes float64) uint64 {
	return uint64(math.Ceil(bytes / (1 << 30)))
}
