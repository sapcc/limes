// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cinder

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/quotasets"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/pagination"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/logg"
)

// ScanUsage implements the liquidapi.Logic interface.
func (l *Logic) ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error) {
	var data struct {
		QuotaSet map[string]quotaSetField `json:"quota_set"`
	}
	err := quotasets.GetUsage(ctx, l.CinderV3, projectUUID).ExtractInto(&data)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	vtAccessMap := l.VolumeTypeAccess.Get()
	resources := make(map[liquid.ResourceName]*liquid.ResourceUsageReport)
	for volumeType := range l.VolumeTypes.Get() {
		isForbidden := false
		if _, ok := vtAccessMap[volumeType]; ok {
			// ^ This check ensures that public volume types never set Forbidden (because they do not have an entry in `vtAccessMap`).
			_, isAllowed := vtAccessMap[volumeType][ProjectID(projectUUID)]
			isForbidden = !isAllowed
		}
		resources[volumeType.capacityResourceName()] = data.QuotaSet[volumeType.capacityQuotaName()].toResourceReport(req.AllAZs, isForbidden)
		resources[volumeType.snapshotsResourceName()] = data.QuotaSet[volumeType.snapshotsQuotaName()].toResourceReport(req.AllAZs, isForbidden)
		resources[volumeType.volumesResourceName()] = data.QuotaSet[volumeType.volumesQuotaName()].toResourceReport(req.AllAZs, isForbidden)
	}

	// NOTE: We always enumerate volume subresources because we need them for the
	// AZ breakdown, even if we don't end up reporting them.
	var metrics usageMetrics
	placementForVolumeUUID, err := l.collectVolumeSubresources(ctx, projectUUID, req.AllAZs, resources, &metrics)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}
	if l.WithSnapshotSubresources {
		err = l.collectSnapshotSubresources(ctx, projectUUID, req.AllAZs, placementForVolumeUUID, resources, &metrics)
		if err != nil {
			return liquid.ServiceUsageReport{}, err
		}
	}

	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources:   resources,
		Metrics: map[liquid.MetricName][]liquid.Metric{
			"liquid_cinder_snapshots_with_unknown_volume_type_size": {{
				Value: float64(metrics.UnknownVolumeType.SnapshotSizeGiB),
			}},
			"liquid_cinder_volumes_with_unknown_volume_type_size": {{
				Value: float64(metrics.UnknownVolumeType.VolumeSizeGiB),
			}},
		},
	}, nil
}

type usageMetrics struct {
	UnknownVolumeType struct {
		SnapshotSizeGiB uint64
		VolumeSizeGiB   uint64
	}
}

func (l *Logic) collectVolumeSubresources(ctx context.Context, projectUUID string, allAZs []liquid.AvailabilityZone, resources map[liquid.ResourceName]*liquid.ResourceUsageReport, metrics *usageMetrics) (placementForVolumeUUID map[string]volumePlacement, err error) {
	placementForVolumeUUID = make(map[string]volumePlacement)
	isKnownVolumeType := make(map[volumeType]bool)
	for vt := range l.VolumeTypes.Get() {
		isKnownVolumeType[vt] = true
	}

	listOpts := volumes.ListOpts{
		AllTenants: true,
		TenantID:   projectUUID,
	}

	err = volumes.List(l.CinderV3, listOpts).EachPage(ctx, func(ctx context.Context, page pagination.Page) (bool, error) {
		vols, err := volumes.ExtractVolumes(page)
		if err != nil {
			return false, err
		}

		for _, volume := range vols {
			az := liquid.NormalizeAZ(volume.AvailabilityZone, allAZs)
			vt := volumeType(volume.VolumeType)
			placementForVolumeUUID[volume.ID] = volumePlacement{vt, az}

			if !isKnownVolumeType[vt] {
				logg.Info("volume %s in project %s has unknown volume type %q", volume.ID, projectUUID, volume.VolumeType)
				metrics.UnknownVolumeType.VolumeSizeGiB += liquidapi.AtLeastZero(volume.Size)
				continue
			}

			if az != liquid.AvailabilityZoneUnknown {
				resources[vt.capacityResourceName()].AddLocalizedUsage(az, liquidapi.AtLeastZero(volume.Size))
				resources[vt.volumesResourceName()].AddLocalizedUsage(az, 1)
			}

			if l.WithVolumeSubresources {
				subresource, err := liquid.SubresourceBuilder[volumeAttributes]{
					ID:   volume.ID,
					Name: volume.Name,
					Attributes: volumeAttributes{
						SizeGiB: liquidapi.AtLeastZero(volume.Size),
						Status:  volume.Status,
					},
				}.Finalize()
				if err != nil {
					return false, err
				}
				usageData := resources[vt.volumesResourceName()].PerAZ[az]
				usageData.Subresources = append(usageData.Subresources, subresource)
			}
		}
		return true, nil
	})
	return placementForVolumeUUID, err
}

func (l *Logic) collectSnapshotSubresources(ctx context.Context, projectUUID string, allAZs []liquid.AvailabilityZone, placementForVolumeUUID map[string]volumePlacement, resources map[liquid.ResourceName]*liquid.ResourceUsageReport, metrics *usageMetrics) error {
	isKnownVolumeType := make(map[volumeType]bool)
	for vt := range l.VolumeTypes.Get() {
		isKnownVolumeType[vt] = true
	}

	listOpts := snapshots.ListOpts{
		AllTenants: true,
		TenantID:   projectUUID,
	}

	err := snapshots.List(l.CinderV3, listOpts).EachPage(ctx, func(ctx context.Context, page pagination.Page) (bool, error) {
		snaps, err := snapshots.ExtractSnapshots(page)
		if err != nil {
			return false, err
		}

		for _, snapshot := range snaps {
			placement, exists := placementForVolumeUUID[snapshot.VolumeID]
			if !exists {
				volume, err := volumes.Get(ctx, l.CinderV3, snapshot.VolumeID).Extract()
				if err != nil {
					return false, fmt.Errorf("could not get info on volume %s that owns snapshot %s in project %s: %w",
						snapshot.VolumeID, snapshot.ID, projectUUID, err)
				}
				vt := volumeType(volume.VolumeType)
				az := liquid.NormalizeAZ(volume.AvailabilityZone, allAZs)
				placement = volumePlacement{vt, az}
			}

			vt := placement.VolumeType
			if !isKnownVolumeType[vt] {
				logg.Info("volume %s that owns snapshot %s in project %s has unknown volume type %q",
					snapshot.VolumeID, snapshot.ID, projectUUID, vt)
				metrics.UnknownVolumeType.SnapshotSizeGiB += liquidapi.AtLeastZero(snapshot.Size)
				continue
			}

			az := placement.AvailabilityZone
			if az != liquid.AvailabilityZoneUnknown {
				resources[vt.capacityResourceName()].AddLocalizedUsage(az, liquidapi.AtLeastZero(snapshot.Size))
				resources[vt.snapshotsResourceName()].AddLocalizedUsage(az, 1)
			}

			if l.WithSnapshotSubresources {
				subresource, err := liquid.SubresourceBuilder[snapshotAttributes]{
					ID:   snapshot.ID,
					Name: snapshot.Name,
					Attributes: snapshotAttributes{
						SizeGiB:  liquidapi.AtLeastZero(snapshot.Size),
						Status:   snapshot.Status,
						VolumeID: snapshot.VolumeID,
					},
				}.Finalize()
				if err != nil {
					return false, err
				}
				usageData := resources[vt.snapshotsResourceName()].PerAZ[az]
				usageData.Subresources = append(usageData.Subresources, subresource)
			}
		}
		return true, nil
	})
	return err
}

////////////////////////////////////////////////////////////////////////////////
// internal types for usage measurement and reporting

type volumePlacement struct {
	VolumeType       volumeType
	AvailabilityZone liquid.AvailabilityZone
}

// volumeAttributes is the Attributes payload for a Cinder volume subresource.
type volumeAttributes struct {
	SizeGiB uint64 `json:"size_gib"`
	Status  string `json:"status"`
}

// snapshotAttributes is the Attributes payload for a Cinder snapshot subresource.
type snapshotAttributes struct {
	SizeGiB  uint64 `json:"size_gib"`
	Status   string `json:"status"`
	VolumeID string `json:"volume_id"`
}

////////////////////////////////////////////////////////////////////////////////
// custom types for Cinder APIs

type quotaSetField struct {
	Quota int64
	Usage uint64
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (f *quotaSetField) UnmarshalJSON(buf []byte) error {
	// The `quota_set` field in the os-quota-sets response is mostly
	// map[string]quotaSetField, but there is also an "id" key containing a
	// string. Skip deserialization of that value.
	if buf[0] == '"' {
		return nil
	}

	var data struct {
		Quota int64  `json:"limit"`
		Usage uint64 `json:"in_use"`
	}
	err := json.Unmarshal(buf, &data)
	if err == nil {
		f.Quota = data.Quota
		f.Usage = data.Usage
	}
	return err
}

func (f quotaSetField) toResourceReport(allAZs []liquid.AvailabilityZone, isForbidden bool) *liquid.ResourceUsageReport {
	return &liquid.ResourceUsageReport{
		Quota:     Some(f.Quota),
		PerAZ:     liquid.AZResourceUsageReport{Usage: f.Usage}.PrepareForBreakdownInto(allAZs),
		Forbidden: isForbidden,
	}
}
