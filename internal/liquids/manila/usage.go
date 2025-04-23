/******************************************************************************
*
*  Copyright 2024 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package manila

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/liquids"
)

// ScanUsage implements the liquidapi.Logic interface.
func (l *Logic) ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error) {
	// the share_networks quota is only shown when querying for no share_type in particular
	qs, err := l.getQuotaSet(ctx, projectUUID, "")
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}
	resources := map[liquid.ResourceName]*liquid.ResourceUsageReport{
		"share_networks": {
			Quota: Some(qs.ShareNetworks.Quota),
			PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: qs.ShareNetworks.Usage}),
		},
	}

	// all other quotas and usages are grouped under their respective share types
	for _, vst := range l.VirtualShareTypes {
		subresult, err := l.scanUsageForShareType(ctx, projectUUID, vst, req)
		if err != nil {
			return liquid.ServiceUsageReport{}, err
		}
		resources[vst.SharesResourceName()] = subresult.Shares
		resources[vst.SnapshotsResourceName()] = subresult.Snapshots
		resources[vst.ShareCapacityResourceName()] = subresult.ShareCapacity
		resources[vst.SnapshotCapacityResourceName()] = subresult.SnapshotCapacity
		if l.NetappMetrics != nil {
			resources[vst.SnapmirrorCapacityResourceName()] = subresult.SnapmirrorCapacity
		}
	}

	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources:   resources,
	}, nil
}

type shareTypeUsageReport struct {
	Shares             *liquid.ResourceUsageReport
	Snapshots          *liquid.ResourceUsageReport
	ShareCapacity      *liquid.ResourceUsageReport
	SnapshotCapacity   *liquid.ResourceUsageReport
	SnapmirrorCapacity *liquid.ResourceUsageReport
}

func (l *Logic) scanUsageForShareType(ctx context.Context, projectUUID string, vst VirtualShareType, req liquid.ServiceUsageRequest) (shareTypeUsageReport, error) {
	projectMetadata, ok := req.ProjectMetadata.Unpack()
	if !ok {
		return shareTypeUsageReport{}, errors.New("projectMetadata is missing")
	}

	rst, omit := vst.RealShareTypeIn(projectMetadata)
	if omit {
		return l.reportShareTypeAsForbidden(req), nil
	}

	// start with the quota data from Manila
	qs, err := l.getQuotaSet(ctx, projectUUID, rst)
	if err != nil {
		return shareTypeUsageReport{}, err
	}

	withAZMetrics := l.AZMetrics != nil
	result := shareTypeUsageReport{
		Shares:           qs.Shares.ToResourceReport(req.AllAZs, withAZMetrics),
		Snapshots:        qs.Snapshots.ToResourceReport(req.AllAZs, withAZMetrics),
		ShareCapacity:    qs.Gigabytes.ToResourceReport(req.AllAZs, withAZMetrics),
		SnapshotCapacity: qs.SnapshotGigabytes.ToResourceReport(req.AllAZs, withAZMetrics),
	}
	if vst.ReplicationEnabled {
		result.Shares = qs.Replicas.ToResourceReport(req.AllAZs, withAZMetrics)
		result.ShareCapacity = qs.ReplicaGigabytes.ToResourceReport(req.AllAZs, withAZMetrics)

		// if share quotas and replica quotas disagree, report quota = -1 to force Limes to reapply the replica quota
		if qs.Shares.Quota != qs.Replicas.Quota {
			logg.Info("found mismatch between share quota (%d) and replica quota (%d) for share type %q in project %s",
				qs.Shares.Quota, qs.Replicas.Quota, rst, projectUUID)
			result.Shares.Quota = Some[int64](-1)
		}
		if qs.Gigabytes.Quota != qs.ReplicaGigabytes.Quota {
			logg.Info("found mismatch between share capacity quota (%d) and replica capacity quota (%d) for share type %q in project %s",
				qs.Gigabytes.Quota, qs.ReplicaGigabytes.Quota, rst, projectUUID)
			result.ShareCapacity.Quota = Some[int64](-1)
		}
	}

	// add data from Prometheus metrics to break down usage by AZ, if available
	if l.AZMetrics != nil {
		shareTypeID, exists := l.ShareTypeIDByName.Get()[rst]
		if !exists {
			return shareTypeUsageReport{}, fmt.Errorf("cannot find ID for share type with name %q", rst)
		}
		for _, az := range req.AllAZs {
			azm, err := l.AZMetrics.Get(ctx, azMetricsKey{
				AvailabilityZone: az,
				ProjectUUID:      projectUUID,
				ShareTypeID:      shareTypeID,
			})
			if err != nil {
				return shareTypeUsageReport{}, err
			}

			result.Shares.AddLocalizedUsage(az, azm.ShareCount)
			result.ShareCapacity.AddLocalizedUsage(az, azm.ShareCapacityGiB)
			result.Snapshots.AddLocalizedUsage(az, azm.SnapshotCount)
			result.SnapshotCapacity.AddLocalizedUsage(az, azm.SnapshotCapacityGiB)
		}
	}

	// add data from Netapp metrics, if available
	if l.NetappMetrics != nil {
		result.SnapmirrorCapacity = &liquid.ResourceUsageReport{
			Quota: None[int64](),
			PerAZ: make(map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport),
		}

		for _, az := range req.AllAZs {
			nm, err := l.NetappMetrics.Get(ctx, netappMetricsKey{
				AvailabilityZone: az,
				ProjectUUID:      projectUUID,
				ShareTypeName:    rst,
			})
			if err != nil {
				return shareTypeUsageReport{}, err
			}

			result.ShareCapacity.PerAZ[az].PhysicalUsage = Some(nm.SharePhysicalUsage)
			result.SnapshotCapacity.PerAZ[az].PhysicalUsage = Some(nm.SnapshotPhysicalUsage)
			result.SnapmirrorCapacity.PerAZ[az] = &liquid.AZResourceUsageReport{
				Usage:         nm.SnapmirrorUsage,
				PhysicalUsage: Some(nm.SnapmirrorPhysicalUsage),
			}
		}
	}

	return result, nil
}

func (l *Logic) reportShareTypeAsForbidden(req liquid.ServiceUsageRequest) shareTypeUsageReport {
	withAZMetrics := l.AZMetrics != nil
	emptyQuotaDetail := QuotaDetail{Quota: 0, Usage: 0}
	forbiddenWithQuota := emptyQuotaDetail.ToResourceReport(req.AllAZs, withAZMetrics)
	forbiddenWithQuota.Forbidden = true
	forbiddenWithoutQuota := emptyQuotaDetail.ToResourceReport(req.AllAZs, withAZMetrics)
	forbiddenWithoutQuota.Forbidden = true
	forbiddenWithoutQuota.Quota = None[int64]()

	return shareTypeUsageReport{
		Shares:             forbiddenWithQuota,
		Snapshots:          forbiddenWithQuota,
		ShareCapacity:      forbiddenWithQuota,
		SnapshotCapacity:   forbiddenWithQuota,
		SnapmirrorCapacity: forbiddenWithoutQuota,
	}
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	projectMetadata, ok := req.ProjectMetadata.Unpack()
	if !ok {
		return errors.New("projectMetadata is missing")
	}

	// collect quotas by share type
	quotaSets := make(map[RealShareType]QuotaSet)
	anyReplicationEnabled := false
	for _, vst := range l.VirtualShareTypes {
		quotaSet := QuotaSet{
			Shares:            req.Resources[vst.SharesResourceName()].Quota,
			Snapshots:         req.Resources[vst.SnapshotsResourceName()].Quota,
			Gigabytes:         req.Resources[vst.ShareCapacityResourceName()].Quota,
			SnapshotGigabytes: req.Resources[vst.SnapshotCapacityResourceName()].Quota,
		}
		if vst.ReplicationEnabled {
			anyReplicationEnabled = true
			quotaSet.Replicas = &quotaSet.Shares
			quotaSet.ReplicaGigabytes = &quotaSet.Gigabytes
		}

		rst, omit := vst.RealShareTypeIn(projectMetadata)
		if omit {
			if !quotaSet.IsEmpty() {
				return fmt.Errorf("share type %q may not be used in this project", vst.Name)
			}
		} else {
			quotaSets[rst] = quotaSet
		}
	}

	// compute overall quotas
	overallQuotas := QuotaSet{
		ShareNetworks: liquids.PointerTo(req.Resources["share_networks"].Quota),
	}
	if anyReplicationEnabled {
		overallQuotas.Replicas = liquids.PointerTo(uint64(0))
		overallQuotas.ReplicaGigabytes = liquids.PointerTo(uint64(0))
	}
	for _, vst := range l.VirtualShareTypes {
		rst, omit := vst.RealShareTypeIn(projectMetadata)
		if omit {
			continue
		}

		quotaSet := quotaSets[rst]
		overallQuotas.Shares += quotaSet.Shares
		overallQuotas.Snapshots += quotaSet.Snapshots
		overallQuotas.Gigabytes += quotaSet.Gigabytes
		overallQuotas.SnapshotGigabytes += quotaSet.SnapshotGigabytes
		if vst.ReplicationEnabled {
			*overallQuotas.Replicas += *quotaSet.Replicas
			*overallQuotas.ReplicaGigabytes += *quotaSet.ReplicaGigabytes
		}
	}

	// we need to set overall quotas first, otherwise share-type-specific quotas
	// may get rejected for not fitting in the overall quota
	err := l.putQuotaSet(ctx, projectUUID, "", overallQuotas)
	if err != nil {
		return err
	}
	for rst, qs := range quotaSets {
		err := l.putQuotaSet(ctx, projectUUID, rst, qs)
		if err != nil {
			return err
		}
	}
	return nil
}

////////////////////////////////////////////////////////////////////////////////
// custom types for Manila APIs (the quota API is not modeled in Gophercloud upstream)

// QuotaSetDetail is used when reading quota and usage.
type QuotaSetDetail struct {
	Shares            QuotaDetail `json:"shares"`
	Snapshots         QuotaDetail `json:"snapshots"`
	Gigabytes         QuotaDetail `json:"gigabytes"`
	SnapshotGigabytes QuotaDetail `json:"snapshot_gigabytes"`
	ShareNetworks     QuotaDetail `json:"share_networks,omitempty"`
	Replicas          QuotaDetail `json:"share_replicas"`
	ReplicaGigabytes  QuotaDetail `json:"replica_gigabytes"`
}

// QuotaDetail appears in type QuotaSetDetail.
type QuotaDetail struct {
	Quota int64  `json:"limit"`
	Usage uint64 `json:"in_use"`
}

// ToResourceReport converts this QuotaDetail into a ResourceUsageReport.
func (q QuotaDetail) ToResourceReport(allAZs []liquid.AvailabilityZone, withAZMetrics bool) *liquid.ResourceUsageReport {
	var perAZ map[liquid.AvailabilityZone]*liquid.AZResourceUsageReport
	if withAZMetrics {
		// if we have AZ metrics, we initially put usage into "unknown",
		// to be shifted into the correct AZs via AddLocalizedUsage()
		perAZ = liquid.AZResourceUsageReport{Usage: q.Usage}.PrepareForBreakdownInto(allAZs)
	} else {
		perAZ = liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: q.Usage})
	}
	return &liquid.ResourceUsageReport{
		Quota: Some(q.Quota),
		PerAZ: perAZ,
	}
}

// PrepareForBreakdownInto converts this QuotaDetail into a ResourceUsageReport.
func (q QuotaDetail) PrepareForBreakdownInto(allAZs []liquid.AvailabilityZone) *liquid.ResourceUsageReport {
	return &liquid.ResourceUsageReport{
		Quota: Some(q.Quota),
		PerAZ: liquid.AZResourceUsageReport{Usage: q.Usage}.PrepareForBreakdownInto(allAZs),
	}
}

// Returns the quota for a specific share type in the given project
// (or the overall quota, if the share type is the empty string).
func (l *Logic) getQuotaSet(ctx context.Context, projectUUID string, st RealShareType) (QuotaSetDetail, error) {
	url := l.ManilaV2.ServiceURL("quota-sets", projectUUID, "detail")
	if st != "" {
		url += "?share_type=" + string(st)
	}

	var result gophercloud.Result
	_, result.Err = l.ManilaV2.Get(ctx, url, &result.Body, nil) //nolint:bodyclose // already closed by gophercloud
	var data struct {
		QuotaSet QuotaSetDetail `json:"quota_set"`
	}
	err := result.ExtractInto(&data)
	return data.QuotaSet, err
}

// QuotaSet is used when writing quotas.
type QuotaSet struct {
	Shares            uint64  `json:"shares"`
	Snapshots         uint64  `json:"snapshots"`
	Gigabytes         uint64  `json:"gigabytes"`
	SnapshotGigabytes uint64  `json:"snapshot_gigabytes"`
	ShareNetworks     *uint64 `json:"share_networks,omitempty"`
	Replicas          *uint64 `json:"share_replicas,omitempty"`
	ReplicaGigabytes  *uint64 `json:"replica_gigabytes,omitempty"`
}

// IsEmpty returns whether there is no non-zero value in this QuotaSet.
func (qs QuotaSet) IsEmpty() bool {
	return qs.Shares == 0 &&
		qs.Snapshots == 0 &&
		qs.Gigabytes == 0 &&
		qs.SnapshotGigabytes == 0 &&
		(qs.ShareNetworks == nil || *qs.ShareNetworks == 0) &&
		(qs.Replicas == nil || *qs.Replicas == 0) &&
		(qs.ReplicaGigabytes == nil || *qs.ReplicaGigabytes == 0)
}

// Writes the quota for a specific share type in the given project
// (or the overall quota, if the share type is the empty string).
func (l *Logic) putQuotaSet(ctx context.Context, projectUUID string, rst RealShareType, qs QuotaSet) error {
	if logg.ShowDebug {
		buf, err := json.Marshal(qs)
		if err == nil {
			logg.Debug("manila: PUT quota-sets project_id=%q share_type=%q: %s", projectUUID, string(rst), string(buf))
		}
	}

	url := l.ManilaV2.ServiceURL("quota-sets", projectUUID)
	if rst != "" {
		url += "?share_type=" + string(rst)
	}

	opts := gophercloud.RequestOpts{OkCodes: []int{http.StatusOK}}
	_, err := l.ManilaV2.Put(ctx, url, map[string]any{"quota_set": qs}, nil, &opts) //nolint:bodyclose // already closed by gophercloud
	return err
}
