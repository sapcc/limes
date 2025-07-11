// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package cinder

import (
	"context"
	"fmt"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/volumetypes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/regexpext"
)

type Logic struct {
	// configuration
	WithSubcapacities        bool                    `json:"with_subcapacities"`
	WithVolumeSubresources   bool                    `json:"with_volume_subresources"`
	WithSnapshotSubresources bool                    `json:"with_snapshot_subresources"`
	ManagePrivateVolumeTypes regexpext.BoundedRegexp `json:"manage_private_volume_types"`
	IgnorePublicVolumeTypes  regexpext.BoundedRegexp `json:"ignore_public_volume_types"`
	// connections
	CinderV3 *gophercloud.ServiceClient `json:"-"`
	// state
	VolumeTypes liquidapi.State[map[VolumeType]VolumeTypeInfo] `json:"-"`

	VolumeTypeAccess liquidapi.State[map[VolumeType]map[ProjectID]struct{}]
}

// VolumeType is a type with convenience functions for deriving resource names.
type VolumeType string
type ProjectID string

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
	StorageProtocol   string
	QualityType       string
	VendorName        string
}

// String returns a string representation of this VolumeTypeInfo for log messages.
func (i VolumeTypeInfo) String() string {
	return fmt.Sprintf("volume_backend_name = %q, storage_protocol = %q, quality_type = %q ", i.VolumeBackendName, i.StorageProtocol, i.QualityType)
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	gophercloud.ServiceTypeAliases["block-storage"] = []string{"volumev3"}
	l.CinderV3, err = openstack.NewBlockStorageV3(provider, eo)
	return err
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	// discover volume types. The option 'IsPublic: "none"' retrieves public and private volume types.
	allPages, err := volumetypes.List(l.CinderV3, ListOpts{IsPublic: "none"}).AllPages(ctx)
	if err != nil {
		return liquid.ServiceInfo{}, err
	}
	vtSpecs, err := volumetypes.ExtractVolumeTypes(allPages)
	if err != nil {
		return liquid.ServiceInfo{}, err
	}

	volumeTypes := make(map[VolumeType]VolumeTypeInfo, len(vtSpecs))
	vtAccess := make(map[VolumeType]map[ProjectID]struct{})
	for _, vtSpec := range vtSpecs {
		vtIsPrivate := !vtSpec.IsPublic && !vtSpec.PublicAccess
		if vtIsPrivate && !l.ManagePrivateVolumeTypes.MatchString(vtSpec.Name) {
			continue
		}
		if !vtIsPrivate && l.IgnorePublicVolumeTypes.MatchString(vtSpec.Name) {
			continue
		}

		volumeTypes[VolumeType(vtSpec.Name)] = VolumeTypeInfo{
			VolumeBackendName: vtSpec.ExtraSpecs["volume_backend_name"],
			StorageProtocol:   vtSpec.ExtraSpecs["storage_protocol"],
			QualityType:       vtSpec.ExtraSpecs["quality_type"],
			VendorName:        vtSpec.ExtraSpecs["vendor_name"],
		}

		if vtIsPrivate {
			vtAccessPages, err := volumetypes.ListAccesses(l.CinderV3, vtSpec.ID).AllPages(ctx)
			if err != nil {
				return liquid.ServiceInfo{}, err
			}
			accessResults, err := volumetypes.ExtractAccesses(vtAccessPages)
			if err != nil {
				return liquid.ServiceInfo{}, err
			}

			accessMap := make(map[ProjectID]struct{}, len(accessResults))
			for _, result := range accessResults {
				accessMap[ProjectID(result.ProjectID)] = struct{}{}
			}
			vtAccess[VolumeType(vtSpec.Name)] = accessMap
		}
	}
	l.VolumeTypes.Set(volumeTypes)
	l.VolumeTypeAccess.Set(vtAccess)

	// build ResourceInfo set
	resInfoForCapacity := liquid.ResourceInfo{
		Unit:                liquid.UnitGibibytes,
		Topology:            liquid.AZAwareTopology,
		HasCapacity:         true,
		NeedsResourceDemand: true,
		HasQuota:            true,
	}
	resInfoForObjects := liquid.ResourceInfo{
		Unit:        liquid.UnitNone,
		Topology:    liquid.AZAwareTopology,
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
