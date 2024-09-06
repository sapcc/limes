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
	"strings"

	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/schedulerstats"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/services"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/liquids"
)

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	pools, err := l.listStoragePools(ctx)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}

	// list service hosts (the relation of pools to their service hosts is used to establish AZ membership)
	allPages, err := services.List(l.CinderV3, nil).AllPages(ctx)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}
	allServices, err := services.ExtractServices(allPages)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}
	serviceHostsPerAZ := make(map[liquid.AvailabilityZone][]string)
	for _, element := range allServices {
		az := liquid.AvailabilityZone(element.Zone)
		if element.Binary == "cinder-volume" {
			// element.Host has the format backendHostname@backendName
			serviceHostsPerAZ[az] = append(serviceHostsPerAZ[az], element.Host)
		}
	}

	// sort pools by volume type and AZ
	volumeTypesByBackendName := make(map[string]VolumeType)
	sortedPools := make(map[VolumeType]map[liquid.AvailabilityZone][]StoragePool)
	for volumeType, cfg := range l.VolumeTypes.Get() {
		volumeTypesByBackendName[cfg.VolumeBackendName] = volumeType
		sortedPools[volumeType] = make(map[liquid.AvailabilityZone][]StoragePool)
	}
	for _, pool := range pools {
		volumeType, ok := volumeTypesByBackendName[pool.Capabilities.VolumeBackendName]
		if !ok {
			logg.Info("ScanCapacity: skipping pool %q with unknown volume_backend_name %q", pool.Name, pool.Capabilities.VolumeBackendName)
			continue
		}

		poolAZ := liquid.AvailabilityZoneUnknown
		for az, hosts := range serviceHostsPerAZ {
			for _, v := range hosts {
				// pool.Name has the format backendHostname@backendName#backendPoolName
				if strings.Contains(pool.Name, v) {
					poolAZ = az
					break
				}
			}
		}
		if poolAZ == liquid.AvailabilityZoneUnknown {
			logg.Info("ScanCapacity: pool %q does not match any service host", pool.Name)
		}
		logg.Debug("ScanCapacity: considering pool %q for volume type %q in AZ %q", pool.Name, volumeType, poolAZ)

		sortedPools[volumeType][poolAZ] = append(sortedPools[volumeType][poolAZ], pool)
	}

	// render result
	result := liquid.ServiceCapacityReport{
		InfoVersion: serviceInfo.Version,
		Resources:   make(map[liquid.ResourceName]*liquid.ResourceCapacityReport),
	}
	for volumeType := range l.VolumeTypes.Get() {
		poolsForVolumeType := liquids.RestrictToKnownAZs(sortedPools[volumeType], req.AllAZs)
		result.Resources[volumeType.CapacityResourceName()], err = l.buildResourceCapacityReport(poolsForVolumeType)
		if err != nil {
			return liquid.ServiceCapacityReport{}, err
		}
	}
	return result, nil
}

func (l *Logic) buildResourceCapacityReport(pools map[liquid.AvailabilityZone][]StoragePool) (result *liquid.ResourceCapacityReport, err error) {
	perAZ := make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport, len(pools))
	for az, azPools := range pools {
		perAZ[az], err = l.buildAZResourceCapacityReport(azPools)
		if err != nil {
			return nil, err
		}
	}
	return &liquid.ResourceCapacityReport{PerAZ: perAZ}, nil
}

func (l *Logic) buildAZResourceCapacityReport(pools []StoragePool) (*liquid.AZResourceCapacityReport, error) {
	var subcapacities []liquid.Subcapacity

	// prepare information for each pool
	for _, pool := range pools {
		usage := uint64(pool.Capabilities.AllocatedCapacityGB)
		builder := liquid.SubcapacityBuilder[StoragePoolAttributes]{
			Name:       pool.Name,
			Capacity:   uint64(pool.Capabilities.TotalCapacityGB),
			Usage:      &usage,
			Attributes: StoragePoolAttributes{},
		}

		state := pool.Capabilities.CustomAttributes.CinderState
		if state == "drain" || state == "reserved" {
			logg.Info("ScanCapacity: pool %q with %g GiB capacity has cinder_state %q -- only considering %g GiB used capacity",
				pool.Name, pool.Capabilities.TotalCapacityGB, state, pool.Capabilities.AllocatedCapacityGB)
			builder.Attributes.ExclusionReason = "cinder_state = " + state
			builder.Attributes.RealCapacity = builder.Capacity
			builder.Capacity = usage // this is what counts towards the total capacity down below
		}

		subcapacity, err := builder.Finalize()
		if err != nil {
			return nil, err
		}
		subcapacities = append(subcapacities, subcapacity)
	}

	// compute overall numbers
	result := &liquid.AZResourceCapacityReport{
		Capacity: 0,
		Usage:    liquids.PointerTo(uint64(0)),
	}
	for _, sub := range subcapacities {
		result.Capacity += sub.Capacity
		*result.Usage += *sub.Usage
	}
	if l.WithSubcapacities {
		result.Subcapacities = subcapacities
	}

	return result, nil
}

////////////////////////////////////////////////////////////////////////////////
// internal types for capacity reporting

// StoragePoolAttributes is the Attributes payload type for a Cinder subcapacity.
type StoragePoolAttributes struct {
	ExclusionReason string `json:"exclusion_reason,omitempty"`
	// Only set when ExclusionReason is set.
	RealCapacity uint64 `json:"real_capacity,omitempty"`
}

////////////////////////////////////////////////////////////////////////////////
// custom types for Cinder APIs

// StoragePool is a custom deserialization target type that replaces
// type schedulerstats.StoragePool.
type StoragePool struct {
	Name         string `json:"name"`
	Capabilities struct {
		VolumeBackendName   string                          `json:"volume_backend_name"`
		TotalCapacityGB     liquids.Float64WithStringErrors `json:"total_capacity_gb"`
		AllocatedCapacityGB liquids.Float64WithStringErrors `json:"allocated_capacity_gb"`

		// SAP Converged Cloud extension
		CustomAttributes struct {
			CinderState string `json:"cinder_state"`
		} `json:"custom_attributes"`
	} `json:"capabilities"`
}

func (l *Logic) listStoragePools(ctx context.Context) ([]StoragePool, error) {
	var poolData struct {
		StoragePools []StoragePool `json:"pools"`
	}
	allPages, err := schedulerstats.List(l.CinderV3, schedulerstats.ListOpts{Detail: true}).AllPages(ctx)
	if err != nil {
		return nil, err
	}
	err = allPages.(schedulerstats.StoragePoolPage).ExtractInto(&poolData)
	return poolData.StoragePools, err
}
