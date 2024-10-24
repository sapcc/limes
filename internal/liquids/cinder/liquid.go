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
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumetypes"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/liquids"
)

type Logic struct {
	// configuration
	WithSubcapacities        bool `json:"with_subcapacities"`
	WithVolumeSubresources   bool `json:"with_volume_subresources"`
	WithSnapshotSubresources bool `json:"with_snapshot_subresources"`
	// connections
	CinderV3 *gophercloud.ServiceClient `json:"-"`
	// state
	VolumeTypes liquids.State[map[VolumeType]VolumeTypeInfo] `json:"-"`
}

// VolumeType is a type with convenience functions for deriving resource names.
type VolumeType string

func (vt VolumeType) CapacityResourceName() liquid.ResourceName {
	return liquid.ResourceName("capacity_" + string(vt))
}
func (vt VolumeType) SnapshotsResourceName() liquid.ResourceName {
	return liquid.ResourceName("snapshots_" + string(vt))
}
func (vt VolumeType) VolumesResourceName() liquid.ResourceName {
	return liquid.ResourceName("volumes_" + string(vt))
}

func (vt VolumeType) CapacityQuotaName() string {
	return "gigabytes_" + string(vt)
}
func (vt VolumeType) SnapshotsQuotaName() string {
	return "snapshots_" + string(vt)
}
func (vt VolumeType) VolumesQuotaName() string {
	return "volumes_" + string(vt)
}

// VolumeTypeInfo contains configuration for a VolumeType.
// We need this for matching pools with their VolumeType.
type VolumeTypeInfo struct {
	VolumeBackendName string
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.CinderV3, err = openstack.NewBlockStorageV3(provider, eo)
	return err
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	// discover volume types
	allPages, err := volumetypes.List(l.CinderV3, volumetypes.ListOpts{}).AllPages(ctx)
	if err != nil {
		return liquid.ServiceInfo{}, err
	}
	vtSpecs, err := volumetypes.ExtractVolumeTypes(allPages)
	if err != nil {
		return liquid.ServiceInfo{}, err
	}
	volumeTypes := make(map[VolumeType]VolumeTypeInfo, len(vtSpecs))
	for _, vtSpec := range vtSpecs {
		if vtSpec.IsPublic && vtSpec.PublicAccess {
			volumeTypes[VolumeType(vtSpec.Name)] = VolumeTypeInfo{
				VolumeBackendName: vtSpec.ExtraSpecs["volume_backend_name"],
			}
		}
	}
	l.VolumeTypes.Set(volumeTypes)

	// build ResourceInfo set
	resInfoForCapacity := liquid.ResourceInfo{
		Unit:        liquid.UnitGibibytes,
		HasCapacity: true,
		HasQuota:    true,
	}
	resInfoForObjects := liquid.ResourceInfo{
		Unit:        liquid.UnitNone,
		HasCapacity: false,
		HasQuota:    true,
	}
	resources := make(map[liquid.ResourceName]liquid.ResourceInfo, 3*len(volumeTypes))
	for vt := range volumeTypes {
		resources[vt.CapacityResourceName()] = resInfoForCapacity
		resources[vt.SnapshotsResourceName()] = resInfoForObjects
		resources[vt.VolumesResourceName()] = resInfoForObjects
	}

	return liquid.ServiceInfo{
		Version:   time.Now().Unix(),
		Resources: resources,
		UsageMetricFamilies: map[liquid.MetricName]liquid.MetricFamilyInfo{
			"liquid_cinder_snapshots_with_unknown_volume_type_size": {
				Type: liquid.MetricTypeGauge,
				Help: "Total size of snapshots that do not have a volume type known to liquid-cinder (and thus Limes), grouped per project.",
			},
			"liquid_cinder_volumes_with_unknown_volume_type_size": {
				Type: liquid.MetricTypeGauge,
				Help: "Total size of volumes that do not have a volume type known to liquid-cinder (and thus Limes), grouped per project.",
			},
		},
	}, nil
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	var requestData struct {
		QuotaSet map[string]uint64 `json:"quota_set"`
	}
	requestData.QuotaSet = make(map[string]uint64)

	for volumeType := range l.VolumeTypes.Get() {
		quotaCapacity := req.Resources[volumeType.CapacityResourceName()].Quota
		requestData.QuotaSet[volumeType.CapacityQuotaName()] = quotaCapacity
		requestData.QuotaSet["gigabytes"] += quotaCapacity

		quotaSnapshots := req.Resources[volumeType.SnapshotsResourceName()].Quota
		requestData.QuotaSet[volumeType.SnapshotsQuotaName()] = quotaSnapshots
		requestData.QuotaSet["snapshots"] += quotaSnapshots

		quotaVolumes := req.Resources[volumeType.VolumesResourceName()].Quota
		requestData.QuotaSet[volumeType.VolumesQuotaName()] = quotaVolumes
		requestData.QuotaSet["volumes"] += quotaVolumes
	}

	url := l.CinderV3.ServiceURL("os-quota-sets", projectUUID)
	opts := gophercloud.RequestOpts{OkCodes: []int{200}}
	_, err := l.CinderV3.Put(ctx, url, requestData, nil, &opts)
	return err
}
