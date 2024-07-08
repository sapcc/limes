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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/sharedfilesystems/apiversions"
	"github.com/gophercloud/gophercloud/v2/openstack/sharedfilesystems/v2/sharetypes"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/model"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/promquery"

	"github.com/sapcc/limes/internal/core"
)

type manilaPlugin struct {
	// configuration
	ShareTypes                          []ManilaShareTypeSpec `yaml:"share_types"`
	PrometheusAPIConfigForAZAwareness   *promquery.Config     `yaml:"prometheus_api_for_az_awareness"`
	PrometheusAPIConfigForNetappMetrics *promquery.Config     `yaml:"prometheus_api_for_netapp_metrics"`
	// connections
	ManilaV2      *gophercloud.ServiceClient                                             `yaml:"-"`
	AZMetrics     *promquery.BulkQueryCache[manilaAZMetricsKey, manilaAZMetrics]         `yaml:"-"`
	NetappMetrics *promquery.BulkQueryCache[manilaNetappMetricsKey, manilaNetappMetrics] `yaml:"-"`
	// caches
	ShareTypeIDByName map[string]string `yaml:"-"`
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &manilaPlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	if len(p.ShareTypes) == 0 {
		return errors.New("quota plugin sharev2: missing required configuration field params.share_types")
	}
	if p.PrometheusAPIConfigForAZAwareness == nil {
		return errors.New("quota plugin sharev2: missing required configuration field params.prometheus_api_for_az_awareness")
	}

	// initialize connection to Manila
	p.ManilaV2, err = openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return err
	}
	microversion, err := p.findMicroversion(ctx, p.ManilaV2)
	if err != nil {
		return err
	}
	if microversion == 0 {
		return errors.New(`cannot find API microversion: no version of the form "2.x" found in advertisement`)
	}
	if microversion < 53 {
		return fmt.Errorf("need at least Manila microversion 2.53 (for replica quotas), but got 2.%d", microversion)
	}
	p.ManilaV2.Microversion = "2.53"

	// we need to enumerate all share types once to establish an ID-name mapping
	// (the Manila quota API exclusively deals with share type names, but some Prometheus metrics need the share type ID)
	pager, err := sharetypes.List(p.ManilaV2, &sharetypes.ListOpts{IsPublic: "all"}).AllPages(ctx)
	if err != nil {
		return fmt.Errorf("cannot enumerate Manila share types: %w", err)
	}
	shareTypes, err := sharetypes.ExtractShareTypes(pager)
	if err != nil {
		return fmt.Errorf("cannot unmarshal Manila share types: %w", err)
	}
	p.ShareTypeIDByName = make(map[string]string)
	for _, shareType := range shareTypes {
		p.ShareTypeIDByName[shareType.Name] = shareType.ID
	}

	// initialize connection to Prometheus
	promClientForAZAwareness, err := p.PrometheusAPIConfigForAZAwareness.Connect()
	if err != nil {
		return err
	}
	p.AZMetrics = promquery.NewBulkQueryCache(manilaAZMetricsQueries, 2*time.Minute, promClientForAZAwareness)

	if p.PrometheusAPIConfigForNetappMetrics != nil {
		promClientForNetappMetrics, err := p.PrometheusAPIConfigForNetappMetrics.Connect()
		if err != nil {
			return err
		}
		p.NetappMetrics = promquery.NewBulkQueryCache(manilaNetappMetricsQueries, 2*time.Minute, promClientForNetappMetrics)
	}

	return nil
}

func (p *manilaPlugin) findMicroversion(ctx context.Context, client *gophercloud.ServiceClient) (int, error) {
	pager, err := apiversions.List(client).AllPages(ctx)
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

	// no 2.x version found at all
	return 0, nil
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *manilaPlugin) PluginTypeID() string {
	return "sharev2"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *manilaPlugin) ServiceInfo(serviceType limes.ServiceType) limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        serviceType,
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
		category := string(p.makeResourceName("sharev2", shareType))
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
		if p.NetappMetrics != nil {
			result = append(result, limesresources.ResourceInfo{
				Name:        p.makeResourceName("snapmirror_capacity", shareType),
				Unit:        limes.UnitGibibytes,
				Category:    category,
				NoQuota:     true,
				ContainedIn: p.makeResourceName("share_capacity", shareType),
			})
		}
	}
	return result
}

// Rates implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Rates() []limesrates.RateInfo {
	return nil
}

func (p *manilaPlugin) makeResourceName(kind string, shareType ManilaShareTypeSpec) limesresources.ResourceName {
	if p.ShareTypes[0].Name == shareType.Name {
		// the resources for the first share type don't get the share type suffix
		// for backwards compatibility reasons
		return limesresources.ResourceName(kind)
	}
	return limesresources.ResourceName(kind + "_" + shareType.Name)
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
func (p *manilaPlugin) ScrapeRates(ctx context.Context, project core.KeystoneProject, prevSerializedState string) (result map[limesrates.RateName]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *manilaPlugin) Scrape(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[limesresources.ResourceName]core.ResourceData, serializedMetrics []byte, err error) {
	// the share_networks quota is only shown when querying for no share_type in particular
	qs, err := manilaGetQuotaSet(ctx, p.ManilaV2, project.UUID, "")
	if err != nil {
		return nil, nil, err
	}
	result = map[limesresources.ResourceName]core.ResourceData{
		"share_networks": qs.ShareNetworks.ToResourceDataInAnyAZ(),
	}

	// all other quotas and usages are grouped under their respective share type
	for _, shareType := range p.ShareTypes {
		subresult, err := p.scrapeShareType(ctx, project, allAZs, shareType)
		if err != nil {
			return nil, nil, err
		}
		result[p.makeResourceName("shares", shareType)] = subresult.Shares
		result[p.makeResourceName("share_capacity", shareType)] = subresult.ShareCapacity
		result[p.makeResourceName("share_snapshots", shareType)] = subresult.Snapshots
		result[p.makeResourceName("snapshot_capacity", shareType)] = subresult.SnapshotCapacity
		if p.NetappMetrics != nil {
			result[p.makeResourceName("snapmirror_capacity", shareType)] = subresult.SnapmirrorCapacity
		}
	}
	return result, nil, nil
}

// All ResourceData for a single share type.
type manilaResourceData struct {
	Shares             core.ResourceData
	ShareCapacity      core.ResourceData
	Snapshots          core.ResourceData
	SnapshotCapacity   core.ResourceData
	SnapmirrorCapacity core.ResourceData // only filled if p.NetappMetrics != nil
}

func (p *manilaPlugin) scrapeShareType(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone, shareType ManilaShareTypeSpec) (manilaResourceData, error) {
	// return all-zero data if this share type is not enabled for this project,
	// and also set MaxQuota = 0 to keep Limes from auto-assigning quota
	stName := resolveManilaShareType(shareType, project)
	if stName == "" {
		noQuotaAllowed := core.ResourceData{
			Quota:     0,
			MaxQuota:  pointerTo(uint64(0)),
			UsageData: core.PerAZ[core.UsageData]{}.AndZeroInTheseAZs(allAZs),
		}
		return manilaResourceData{
			Shares:             noQuotaAllowed,
			ShareCapacity:      noQuotaAllowed,
			Snapshots:          noQuotaAllowed,
			SnapshotCapacity:   noQuotaAllowed,
			SnapmirrorCapacity: noQuotaAllowed,
		}, nil
	}

	// start with the quota data from Manila
	qs, err := manilaGetQuotaSet(ctx, p.ManilaV2, project.UUID, stName)
	if err != nil {
		return manilaResourceData{}, err
	}
	result := manilaResourceData{
		Shares:           qs.Shares.ToResourceDataFor(allAZs),
		ShareCapacity:    qs.Gigabytes.ToResourceDataFor(allAZs),
		Snapshots:        qs.Snapshots.ToResourceDataFor(allAZs),
		SnapshotCapacity: qs.SnapshotGigabytes.ToResourceDataFor(allAZs),
	}
	if shareType.ReplicationEnabled {
		result.Shares = qs.Replicas.ToResourceDataFor(allAZs)
		result.ShareCapacity = qs.ReplicaGigabytes.ToResourceDataFor(allAZs)

		// if share quotas and replica quotas disagree, report quota = -1 to force Limes to reapply the replica quota
		if qs.Shares.Quota != result.Shares.Quota {
			logg.Info("found mismatch between share quota (%d) and replica quota (%d) for share type %q in project %s",
				result.Shares.Quota, qs.Replicas.Quota, stName, project.UUID)
			result.Shares.Quota = -1
		}
		if qs.Gigabytes.Quota != result.ShareCapacity.Quota {
			logg.Info("found mismatch between share capacity quota (%d) and replica capacity quota (%d) for share type %q in project %s",
				result.ShareCapacity.Quota, qs.ReplicaGigabytes.Quota, stName, project.UUID)
			result.ShareCapacity.Quota = -1
		}
	}

	// add data from Prometheus metrics to break down usage by AZ
	stID, exists := p.ShareTypeIDByName[stName]
	if !exists {
		return manilaResourceData{}, fmt.Errorf("cannot find ID for share type with name %q", stName)
	}
	for _, az := range allAZs {
		azm, err := p.AZMetrics.Get(manilaAZMetricsKey{
			AvailabilityZone: az,
			ProjectUUID:      project.UUID,
			ShareTypeID:      stID,
		})
		if err != nil {
			return manilaResourceData{}, err
		}

		result.Shares.AddLocalizedUsage(az, azm.ShareCount)
		result.ShareCapacity.AddLocalizedUsage(az, azm.ShareCapacityGiB)
		result.Snapshots.AddLocalizedUsage(az, azm.SnapshotCount)
		result.SnapshotCapacity.AddLocalizedUsage(az, azm.SnapshotCapacityGiB)
	}

	// add data from Netapp metrics if available
	if p.NetappMetrics != nil {
		emptyQuota := manilaQuotaDetail{Quota: 0, Usage: 0}
		result.SnapmirrorCapacity = emptyQuota.ToResourceDataFor(allAZs)

		for _, az := range allAZs {
			nm, err := p.NetappMetrics.Get(manilaNetappMetricsKey{
				AvailabilityZone: az,
				ProjectUUID:      project.UUID,
				ShareTypeName:    stName,
			})
			if err != nil {
				return manilaResourceData{}, err
			}

			result.ShareCapacity.UsageData[az].PhysicalUsage = &nm.SharePhysicalUsage
			result.SnapshotCapacity.UsageData[az].PhysicalUsage = &nm.SnapshotPhysicalUsage
			result.SnapmirrorCapacity.UsageData[az] = &core.UsageData{
				Usage:         nm.SnapmirrorUsage,
				PhysicalUsage: &nm.SnapmirrorPhysicalUsage,
			}
		}
	}

	return result, nil
}

func (p *manilaPlugin) rejectInaccessibleShareType(project core.KeystoneProject, quotas map[limesresources.ResourceName]uint64) error {
	// check if an inaccessible share type is used
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
func (p *manilaPlugin) SetQuota(ctx context.Context, project core.KeystoneProject, quotas map[limesresources.ResourceName]uint64) error {
	err := p.rejectInaccessibleShareType(project, quotas)
	if err != nil {
		return err
	}

	expect200 := &gophercloud.RequestOpts{OkCodes: []int{http.StatusOK}}

	// General note: Even though it complicates the code, we need to set overall
	// quotas first, otherwise share-type-specific quotas may get rejected for not
	// fitting in the overall quota.

	shareNetworkQuota := quotas["share_networks"]
	overallQuotas := manilaQuotaSet{
		ShareNetworks: &shareNetworkQuota,
	}
	shareTypeQuotas := make(map[string]manilaQuotaSet)
	anyReplicationEnabled := false

	for _, shareType := range p.ShareTypes {
		stName := resolveManilaShareType(shareType, project)
		if stName == "" {
			for _, resName := range []string{"shares", "share_capacity", "share_snapshots", "snapshot_capacity"} {
				if quotas[p.makeResourceName(resName, shareType)] > 0 {
					return fmt.Errorf("share type %q may not be used in this project", shareType.Name)
				}
			}
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
		if shareType.ReplicationEnabled {
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

	if anyReplicationEnabled {
		overallQuotas.ReplicasPtr = &overallQuotas.Replicas
		overallQuotas.ReplicaGigabytesPtr = &overallQuotas.ReplicaGigabytes
	}

	url := p.ManilaV2.ServiceURL("quota-sets", project.UUID)
	logDebugSetQuota(project.UUID, "overall", overallQuotas)
	_, err = p.ManilaV2.Put(ctx, url, map[string]any{"quota_set": overallQuotas}, nil, expect200) //nolint:bodyclose // already closed by gophercloud
	if err != nil {
		return fmt.Errorf("could not set overall share quotas: %s", err.Error())
	}

	for shareTypeName, quotasForType := range shareTypeQuotas {
		logDebugSetQuota(project.UUID, shareTypeName, quotasForType)
		url := p.ManilaV2.ServiceURL("quota-sets", project.UUID) + "?share_type=" + shareTypeName
		_, err = p.ManilaV2.Put(ctx, url, map[string]any{"quota_set": quotasForType}, nil, expect200) //nolint:bodyclose // already closed by gophercloud
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
	// not used by this plugin
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *manilaPlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	// not used by this plugin
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

func (q manilaQuotaDetail) ToResourceDataInAnyAZ() core.ResourceData {
	return core.ResourceData{
		Quota:     q.Quota,
		UsageData: core.InAnyAZ(core.UsageData{Usage: q.Usage}),
	}
}

func (q manilaQuotaDetail) ToResourceDataFor(allAZs []limes.AvailabilityZone) core.ResourceData {
	return core.ResourceData{
		Quota:     q.Quota,
		UsageData: core.InUnknownAZUnlessEmpty(core.UsageData{Usage: q.Usage}).AndZeroInTheseAZs(allAZs),
	}
}

func manilaGetQuotaSet(ctx context.Context, client *gophercloud.ServiceClient, projectUUID, shareTypeName string) (manilaQuotaSetDetail, error) {
	var result gophercloud.Result
	url := client.ServiceURL("quota-sets", projectUUID, "detail")
	if shareTypeName != "" {
		url += "?share_type=" + shareTypeName
	}
	_, err := client.Get(ctx, url, &result.Body, nil) //nolint:bodyclose // already closed by gophercloud
	if err != nil {
		return manilaQuotaSetDetail{}, err
	}

	var manilaQuotaData struct {
		QuotaSet manilaQuotaSetDetail `json:"quota_set"`
	}
	err = result.ExtractInto(&manilaQuotaData)
	return manilaQuotaData.QuotaSet, err
}

//////////////////////////////////////////////////////////////////////////////////
// Prometheus queries and related types

const (
	// NOTE: In these queries, the `last_over_time(...[15m])` part guards against temporary unavailability of metrics resulting in spurious zero values.

	// queries for AZ awareness metrics
	manilaShareCountQuery       = `count by (availability_zone_name, project_id, share_type_id) (max by (availability_zone_name, id, project_id, share_id, share_type_id) (last_over_time(openstack_manila_replicas_count_gauge[15m])))`
	manilaShareCapacityQuery    = `sum   by (availability_zone_name, project_id, share_type_id) (max by (availability_zone_name, id, project_id, share_id, share_type_id) (last_over_time(openstack_manila_replicas_size_gauge[15m])))`
	manilaSnapshotCountQuery    = `count by (availability_zone_name, project_id, share_type_id) (max by (availability_zone_name, id, project_id, share_id, share_type_id) (last_over_time(openstack_manila_snapshot_count_gauge[15m])))`
	manilaSnapshotCapacityQuery = `sum   by (availability_zone_name, project_id, share_type_id) (max by (availability_zone_name, id, project_id, share_id, share_type_id) (last_over_time(openstack_manila_snapshot_size_gauge[15m])))`

	// queries for netapp-exporter metrics
	manilaSharePhysicalUsageQuery      = `sum by (availability_zone, project_id, share_type) (max by (availability_zone, project_id, share_id, share_type) (last_over_time(netapp_volume_used_bytes         {project_id!="",share_type!="",volume_type!="dp",volume_state="online"}[15m])))`
	manilaSnapshotPhysicalUsageQuery   = `sum by (availability_zone, project_id, share_type) (max by (availability_zone, project_id, share_id, share_type) (last_over_time(netapp_volume_snapshot_used_bytes{project_id!="",share_type!="",volume_type!="dp",volume_state="online"}[15m])))`
	manilaSnapmirrorUsageQuery         = `sum by (availability_zone, project_id, share_type) (max by (availability_zone, project_id, share_id, share_type) (last_over_time(netapp_volume_total_bytes        {project_id!="",share_type!="",volume_type!="dp",volume_state="online",snapshot_policy="EC2_Backups"}[15m])))`
	manilaSnapmirrorPhysicalUsageQuery = `sum by (availability_zone, project_id, share_type) (max by (availability_zone, project_id, share_id, share_type) (last_over_time(netapp_volume_used_bytes         {project_id!="",share_type!="",volume_type!="dp",volume_state="online",snapshot_policy="EC2_Backups"}[15m])))`
)

type manilaAZMetricsKey struct {
	AvailabilityZone limes.AvailabilityZone
	ProjectUUID      string
	ShareTypeID      string
}

func manilaAZMetricsKeyer(sample *model.Sample) manilaAZMetricsKey {
	return manilaAZMetricsKey{
		AvailabilityZone: limes.AvailabilityZone(sample.Metric["availability_zone_name"]),
		ProjectUUID:      string(sample.Metric["project_id"]),
		ShareTypeID:      string(sample.Metric["share_type_id"]),
	}
}

type manilaNetappMetricsKey struct {
	AvailabilityZone limes.AvailabilityZone
	ProjectUUID      string
	ShareTypeName    string
}

func manilaNetappMetricsKeyer(sample *model.Sample) manilaNetappMetricsKey {
	return manilaNetappMetricsKey{
		AvailabilityZone: limes.AvailabilityZone(sample.Metric["availability_zone"]),
		ProjectUUID:      string(sample.Metric["project_id"]),
		ShareTypeName:    string(sample.Metric["share_type"]),
	}
}

type manilaAZMetrics struct {
	ShareCount          uint64
	ShareCapacityGiB    uint64
	SnapshotCount       uint64
	SnapshotCapacityGiB uint64
}

type manilaNetappMetrics struct {
	// all in GiB
	SharePhysicalUsage      uint64
	SnapshotPhysicalUsage   uint64
	SnapmirrorUsage         uint64
	SnapmirrorPhysicalUsage uint64
}

var (
	//nolint:dupl
	manilaAZMetricsQueries = []promquery.BulkQuery[manilaAZMetricsKey, manilaAZMetrics]{
		{
			Query:       manilaShareCountQuery,
			Description: "shares usage data",
			Keyer:       manilaAZMetricsKeyer,
			Filler: func(entry *manilaAZMetrics, sample *model.Sample) {
				entry.ShareCount = roundUp(float64(sample.Value))
			},
		},
		{
			Query:       manilaShareCapacityQuery,
			Description: "share_capacity usage data",
			Keyer:       manilaAZMetricsKeyer,
			Filler: func(entry *manilaAZMetrics, sample *model.Sample) {
				entry.ShareCapacityGiB = roundUp(float64(sample.Value))
			},
		},
		{
			Query:       manilaSnapshotCountQuery,
			Description: "share_snapshots usage data",
			Keyer:       manilaAZMetricsKeyer,
			Filler: func(entry *manilaAZMetrics, sample *model.Sample) {
				entry.SnapshotCount = roundUp(float64(sample.Value))
			},
		},
		{
			Query:       manilaSnapshotCapacityQuery,
			Description: "snapshot_capacity usage data",
			Keyer:       manilaAZMetricsKeyer,
			Filler: func(entry *manilaAZMetrics, sample *model.Sample) {
				entry.SnapshotCapacityGiB = roundUp(float64(sample.Value))
			},
		},
	}
	//nolint:dupl
	manilaNetappMetricsQueries = []promquery.BulkQuery[manilaNetappMetricsKey, manilaNetappMetrics]{
		{
			Query:       manilaSharePhysicalUsageQuery,
			Description: "share_capacity physical usage data",
			Keyer:       manilaNetappMetricsKeyer,
			Filler: func(entry *manilaNetappMetrics, sample *model.Sample) {
				entry.SharePhysicalUsage = roundUpIntoGigabytes(float64(sample.Value))
			},
		},
		{
			Query:       manilaSnapshotPhysicalUsageQuery,
			Description: "snapshot_capacity physical usage data",
			Keyer:       manilaNetappMetricsKeyer,
			Filler: func(entry *manilaNetappMetrics, sample *model.Sample) {
				entry.SnapshotPhysicalUsage = roundUpIntoGigabytes(float64(sample.Value))
			},
		},
		{
			Query:       manilaSnapmirrorUsageQuery,
			Description: "snapmirror_capacity usage data",
			Keyer:       manilaNetappMetricsKeyer,
			Filler: func(entry *manilaNetappMetrics, sample *model.Sample) {
				entry.SnapmirrorUsage = roundUpIntoGigabytes(float64(sample.Value))
			},
		},
		{
			Query:       manilaSnapmirrorPhysicalUsageQuery,
			Description: "snapmirror_capacity physical usage data",
			Keyer:       manilaNetappMetricsKeyer,
			Filler: func(entry *manilaNetappMetrics, sample *model.Sample) {
				entry.SnapmirrorPhysicalUsage = roundUpIntoGigabytes(float64(sample.Value))
			},
		},
	}
)

func roundUp(number float64) uint64 {
	return uint64(math.Ceil(number))
}

func roundUpIntoGigabytes(bytes float64) uint64 {
	return uint64(math.Ceil(bytes / (1 << 30)))
}
