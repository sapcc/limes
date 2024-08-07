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
	"errors"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/sapcc/go-api-declarations/liquid"
)

// TODO: note to self, this will render subresources and subcapacities in the LIQUID format - to maintain compatibility within the v1 API, we will use a translation layer on the API level to translate subresources and subcapacities back into the known formats
// TODO: build out the category mapping facility in type liquidQuotaPlugin

type Logic struct {
	// configuration
	VolumeTypes map[VolumeType]struct {
		VolumeBackendName string `json:"volume_backend_name"`
	} `json:"volume_types"`
	WithSubcapacities        bool `json:"with_subcapacities"`
	WithVolumeSubresources   bool `json:"with_volume_subresources"`
	WithSnapshotSubresources bool `json:"with_snapshot_subresources"`
	// connections
	CinderV3 *gophercloud.ServiceClient `json:"-"`
	// state
	StartupTime time.Time `json:"-"`
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	if len(l.VolumeTypes) == 0 {
		return errors.New("missing configuration value: volume_types")
	}

	l.StartupTime = time.Now()
	l.CinderV3, err = openstack.NewBlockStorageV3(provider, eo)
	return err
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
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

	resources := make(map[liquid.ResourceName]liquid.ResourceInfo, 3*len(l.VolumeTypes))
	for volumeType := range l.VolumeTypes {
		resources[volumeType.CapacityResourceName()] = resInfoForCapacity
		resources[volumeType.SnapshotsResourceName()] = resInfoForObjects
		resources[volumeType.VolumesResourceName()] = resInfoForObjects
	}

	return liquid.ServiceInfo{
		Version:   l.StartupTime.Unix(),
		Resources: resources,
	}, nil
}

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	pools, err := l.listStoragePools(ctx)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}

	TODO("finish")
}

// ScanUsage implements the liquidapi.Logic interface.
func (l *Logic) ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error) {
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
}
