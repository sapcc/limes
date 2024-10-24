/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package cinder

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/quotasets"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/snapshots"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumes"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/liquids"
)

// ScanUsage implements the liquidapi.Logic interface.
func (l *Logic) ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error) {
	var data struct {
		QuotaSet map[string]QuotaSetField `json:"quota_set"`
	}
	err := quotasets.GetUsage(ctx, l.CinderV3, projectUUID).ExtractInto(&data)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	resources := make(map[liquid.ResourceName]*liquid.ResourceUsageReport)
	for volumeType := range l.VolumeTypes.Get() {
		resources[volumeType.CapacityResourceName()] = data.QuotaSet[volumeType.CapacityQuotaName()].ToResourceReport(req.AllAZs)
		resources[volumeType.SnapshotsResourceName()] = data.QuotaSet[volumeType.SnapshotsQuotaName()].ToResourceReport(req.AllAZs)
		resources[volumeType.VolumesResourceName()] = data.QuotaSet[volumeType.VolumesQuotaName()].ToResourceReport(req.AllAZs)
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

func (l *Logic) collectVolumeSubresources(ctx context.Context, projectUUID string, allAZs []liquid.AvailabilityZone, resources map[liquid.ResourceName]*liquid.ResourceUsageReport, metrics *usageMetrics) (placementForVolumeUUID map[string]VolumePlacement, err error) {
	placementForVolumeUUID = make(map[string]VolumePlacement)
	isKnownVolumeType := make(map[VolumeType]bool)
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
			az := liquid.AvailabilityZone(volume.AvailabilityZone)
			if !slices.Contains(allAZs, az) {
				az = liquid.AvailabilityZoneUnknown
			}
			vt := VolumeType(volume.VolumeType)
			placementForVolumeUUID[volume.ID] = VolumePlacement{vt, az}

			if !isKnownVolumeType[vt] {
				logg.Info("volume %s in project %s has unknown volume type %q", volume.ID, projectUUID, volume.VolumeType)
				metrics.UnknownVolumeType.VolumeSizeGiB += liquids.AtLeastZero(volume.Size)
				continue
			}

			if az != liquid.AvailabilityZoneUnknown {
				resources[vt.CapacityResourceName()].AddLocalizedUsage(az, liquids.AtLeastZero(volume.Size))
				resources[vt.VolumesResourceName()].AddLocalizedUsage(az, 1)
			}

			if l.WithVolumeSubresources {
				subresource, err := liquid.SubresourceBuilder[VolumeAttributes]{
					ID:   volume.ID,
					Name: volume.Name,
					Attributes: VolumeAttributes{
						SizeGiB: liquids.AtLeastZero(volume.Size),
						Status:  volume.Status,
					},
				}.Finalize()
				if err != nil {
					return false, err
				}
				usageData := resources[vt.VolumesResourceName()].PerAZ[az]
				usageData.Subresources = append(usageData.Subresources, subresource)
			}
		}
		return true, nil
	})
	return placementForVolumeUUID, err
}

func (l *Logic) collectSnapshotSubresources(ctx context.Context, projectUUID string, allAZs []liquid.AvailabilityZone, placementForVolumeUUID map[string]VolumePlacement, resources map[liquid.ResourceName]*liquid.ResourceUsageReport, metrics *usageMetrics) error {
	isKnownVolumeType := make(map[VolumeType]bool)
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
				vt := VolumeType(volume.VolumeType)
				az := liquid.AvailabilityZone(volume.AvailabilityZone)
				if !slices.Contains(allAZs, az) {
					az = liquid.AvailabilityZoneUnknown
				}
				placement = VolumePlacement{vt, az}
			}

			vt := placement.VolumeType
			if !isKnownVolumeType[vt] {
				logg.Info("volume %s that owns snapshot %s in project %s has unknown volume type %q",
					snapshot.VolumeID, snapshot.ID, projectUUID, vt)
				metrics.UnknownVolumeType.SnapshotSizeGiB += liquids.AtLeastZero(snapshot.Size)
				continue
			}

			az := placement.AvailabilityZone
			if az != liquid.AvailabilityZoneUnknown {
				resources[vt.CapacityResourceName()].AddLocalizedUsage(az, liquids.AtLeastZero(snapshot.Size))
				resources[vt.SnapshotsResourceName()].AddLocalizedUsage(az, 1)
			}

			if l.WithSnapshotSubresources {
				subresource, err := liquid.SubresourceBuilder[SnapshotAttributes]{
					ID:   snapshot.ID,
					Name: snapshot.Name,
					Attributes: SnapshotAttributes{
						SizeGiB:  liquids.AtLeastZero(snapshot.Size),
						Status:   snapshot.Status,
						VolumeID: snapshot.VolumeID,
					},
				}.Finalize()
				if err != nil {
					return false, err
				}
				usageData := resources[vt.SnapshotsResourceName()].PerAZ[az]
				usageData.Subresources = append(usageData.Subresources, subresource)
			}
		}
		return true, nil
	})
	return err
}

////////////////////////////////////////////////////////////////////////////////
// internal types for usage measurement and reporting

type VolumePlacement struct {
	VolumeType       VolumeType
	AvailabilityZone liquid.AvailabilityZone
}

// VolumeAttributes is the Attributes payload for a Cinder volume subresource.
type VolumeAttributes struct {
	SizeGiB uint64 `json:"size_gib"`
	Status  string `json:"status"`
}

// SnapshotAttributes is the Attributes payload for a Cinder snapshot subresource.
type SnapshotAttributes struct {
	SizeGiB  uint64 `json:"size_gib"`
	Status   string `json:"status"`
	VolumeID string `json:"volume_id"`
}

////////////////////////////////////////////////////////////////////////////////
// custom types for Cinder APIs

type QuotaSetField struct {
	Quota int64
	Usage uint64
}

func (f *QuotaSetField) UnmarshalJSON(buf []byte) error {
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

func (f QuotaSetField) ToResourceReport(allAZs []liquid.AvailabilityZone) *liquid.ResourceUsageReport {
	return &liquid.ResourceUsageReport{
		Quota: &f.Quota,
		PerAZ: liquid.AZResourceUsageReport{Usage: f.Usage}.PrepareForBreakdownInto(allAZs),
	}
}
