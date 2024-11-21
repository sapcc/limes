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
	"cmp"
	"context"
	"strings"

	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/schedulerstats"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/services"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/liquids"
	"github.com/sapcc/limes/internal/util"
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

	// sort volume types by VolumeTypeInfo (if multiple volume types have the same VolumeTypeInfo, they need to share the same pools)
	volumeTypesByInfo := make(map[VolumeTypeInfo][]VolumeType)
	for volumeType, info := range l.VolumeTypes.Get() {
		volumeTypesByInfo[info] = append(volumeTypesByInfo[info], volumeType)
	}

	// sort pools by volume backend name and AZ
	sortedPools := make(map[VolumeTypeInfo]map[liquid.AvailabilityZone][]StoragePool, len(volumeTypesByInfo))
	for info := range volumeTypesByInfo {
		sortedPools[info] = make(map[liquid.AvailabilityZone][]StoragePool)
	}
	for _, pool := range pools {
		info := VolumeTypeInfo{
			VolumeBackendName: pool.Capabilities.VolumeBackendName,
		}
		_, exists := sortedPools[info]
		if !exists {
			logg.Info("ScanCapacity: skipping pool %q: no volume type uses pools with %s", pool.Name, info)
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
			logg.Info("ScanCapacity: pool %q with %s does not match any service host", pool.Name, info)
		}
		logg.Debug("ScanCapacity: considering pool %q with %s in AZ %q", pool.Name, info, poolAZ)

		sortedPools[info][poolAZ] = append(sortedPools[info][poolAZ], pool)
	}

	// calculate result
	result := liquid.ServiceCapacityReport{
		InfoVersion: serviceInfo.Version,
		Resources:   make(map[liquid.ResourceName]*liquid.ResourceCapacityReport),
	}
	for info, volumeTypes := range volumeTypesByInfo {
		relevantPools := liquids.RestrictToKnownAZs(sortedPools[info], req.AllAZs)
		relevantDemands := make(map[VolumeType]liquid.ResourceDemand)
		for _, volumeType := range volumeTypes {
			relevantDemands[volumeType] = req.DemandByResource[volumeType.CapacityResourceName()]
		}

		reportsByVolumeType, err := l.buildCapacityReportForPoolSet(relevantPools, relevantDemands)
		if err != nil {
			return liquid.ServiceCapacityReport{}, err
		}
		for volumeType, resReport := range reportsByVolumeType {
			result.Resources[volumeType.CapacityResourceName()] = resReport
		}
	}
	return result, nil
}

func (l *Logic) buildCapacityReportForPoolSet(pools map[liquid.AvailabilityZone][]StoragePool, demands map[VolumeType]liquid.ResourceDemand) (map[VolumeType]*liquid.ResourceCapacityReport, error) {
	// prepare output structure
	result := make(map[VolumeType]*liquid.ResourceCapacityReport, len(demands))
	for volumeType := range demands {
		result[volumeType] = &liquid.ResourceCapacityReport{
			PerAZ: make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport, len(pools)),
		}
	}

	// this is used to decide where to report subcapacities (see below)
	mostCommonVolumeType := argmax(demands, func(demand liquid.ResourceDemand) (result uint64) {
		for _, d := range demand.PerAZ {
			result += d.Usage + d.UnusedCommitments + d.PendingCommitments
		}
		return result
	})

	// fill report, one AZ at a time
	for az, azPools := range pools {
		azRawDemands := make(map[VolumeType]liquid.ResourceDemandInAZ)
		for volumeType, demand := range demands {
			azRawDemands[volumeType] = demand.OvercommitFactor.ApplyInReverseToDemand(demand.PerAZ[az])
		}
		azReports, err := l.buildAZCapacityReportForPoolSet(azPools, azRawDemands, az, mostCommonVolumeType)
		if err != nil {
			return nil, err
		}
		for volumeType, azResReport := range azReports {
			result[volumeType].PerAZ[az] = azResReport
		}
	}
	return result, nil
}

func (l *Logic) buildAZCapacityReportForPoolSet(pools []StoragePool, rawDemands map[VolumeType]liquid.ResourceDemandInAZ, az liquid.AvailabilityZone, mostCommonVolumeType VolumeType) (map[VolumeType]*liquid.AZResourceCapacityReport, error) {
	var (
		totalCapacityGiB = uint64(0)
		totalUsageGiB    = uint64(0)
		subcapacities    []liquid.Subcapacity
	)

	// prepare information for each pool, and also compute running totals
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

		if l.WithSubcapacities {
			subcapacity, err := builder.Finalize()
			if err != nil {
				return nil, err
			}
			subcapacities = append(subcapacities, subcapacity)
		}

		totalCapacityGiB += builder.Capacity
		totalUsageGiB += *builder.Usage
	}

	// distribute capacity and usage between the relevant volume types
	balance := make(map[VolumeType]float64, len(rawDemands))
	for volumeType := range rawDemands {
		balance[volumeType] = 1.0
	}
	logg.Debug("distributing for AZ %q: capacity = %d between volume types %v", az, totalCapacityGiB, balance)
	distributedCapacityGiB := util.DistributeDemandFairly(totalCapacityGiB, rawDemands, balance)
	logg.Debug("distributing for AZ %q: usage = %d between volume types %v", az, totalUsageGiB, balance)
	distributedUsageGiB := util.DistributeDemandFairly(totalUsageGiB, rawDemands, balance)

	result := make(map[VolumeType]*liquid.AZResourceCapacityReport, len(rawDemands))
	for volumeType := range rawDemands {
		result[volumeType] = &liquid.AZResourceCapacityReport{
			Capacity: distributedCapacityGiB[volumeType],
			Usage:    liquids.PointerTo(distributedUsageGiB[volumeType]),
		}
	}

	// splitting the subcapacities between resources would quickly turn into a mess;
	// since we don't have a need for that, we just report subcapacities on the most commonly used volume type
	if l.WithSubcapacities {
		result[mostCommonVolumeType].Subcapacities = subcapacities
	}
	return result, nil
}

func argmax[K comparable, V any, N cmp.Ordered](set map[K]V, predicate func(V) N) K {
	var (
		bestKey   K
		bestScore N
		first     = true
	)
	for key, value := range set {
		score := predicate(value)
		if first || score > bestScore {
			bestKey = key
			bestScore = score
		}
	}
	return bestKey
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
