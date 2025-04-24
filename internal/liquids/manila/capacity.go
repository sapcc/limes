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
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/sharedfilesystems/v2/schedulerstats"
	"github.com/gophercloud/gophercloud/v2/openstack/sharedfilesystems/v2/services"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/liquids"
)

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	allPages, err := services.List(l.ManilaV2, nil).AllPages(ctx)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}
	allServices, err := services.ExtractServices(allPages)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}

	azForServiceHost := make(map[string]liquid.AvailabilityZone)
	for _, element := range allServices {
		if element.Binary == "manila-share" {
			// element.Host has the format backendHostname@backendName
			fields := strings.Split(element.Host, "@")
			if len(fields) != 2 {
				logg.Error("Expected a Manila service host in the format \"backendHostname@backendName\", got %q with ID %d", element.Host, element.ID)
			} else {
				azForServiceHost[fields[0]] = liquid.AvailabilityZone(element.Zone)
			}
		}
	}

	resources := map[liquid.ResourceName]*liquid.ResourceCapacityReport{
		"share_networks": {
			PerAZ: liquid.InAnyAZ(liquid.AZResourceCapacityReport{Capacity: l.CapacityCalculation.ShareNetworks}),
		},
	}
	for _, vst := range l.VirtualShareTypes {
		shareCapacityDemand := convertToRawDemand(req.DemandByResource[vst.ShareCapacityResourceName()])
		snapshotCapacityDemand := convertToRawDemand(req.DemandByResource[vst.SnapshotCapacityResourceName()])
		var snapmirrorCapacityDemand map[liquid.AvailabilityZone]liquid.ResourceDemandInAZ
		if l.NetappMetrics != nil {
			snapmirrorCapacityDemand = convertToRawDemand(req.DemandByResource[vst.SnapmirrorCapacityResourceName()])
		}
		// ^ NOTE: If l.NetappMetrics == nil, `snapmirrorCapacityDemand[az]` is always zero-valued
		// and thus no capacity will be allocated to the snapmirror_capacity resource.

		subresult, err := l.scanCapacityForShareType(ctx, vst, azForServiceHost, req.AllAZs, shareCapacityDemand, snapshotCapacityDemand, snapmirrorCapacityDemand)
		if err != nil {
			return liquid.ServiceCapacityReport{}, err
		}

		resources[vst.SharesResourceName()] = &subresult.Shares
		resources[vst.SnapshotsResourceName()] = &subresult.Snapshots
		resources[vst.ShareCapacityResourceName()] = &subresult.ShareCapacity
		resources[vst.SnapshotCapacityResourceName()] = &subresult.SnapshotCapacity
		if l.NetappMetrics != nil {
			resources[vst.SnapmirrorCapacityResourceName()] = &subresult.SnapmirrorCapacity
		}
	}

	return liquid.ServiceCapacityReport{
		InfoVersion: serviceInfo.Version,
		Resources:   resources,
	}, nil
}

func convertToRawDemand(demand liquid.ResourceDemand) map[liquid.AvailabilityZone]liquid.ResourceDemandInAZ {
	rawDemand := make(map[liquid.AvailabilityZone]liquid.ResourceDemandInAZ, len(demand.PerAZ))
	for az, effectiveDemand := range demand.PerAZ {
		rawDemand[az] = demand.OvercommitFactor.ApplyInReverseToDemand(effectiveDemand)
	}
	return rawDemand
}

type capacityForShareType struct {
	Shares             liquid.ResourceCapacityReport
	Snapshots          liquid.ResourceCapacityReport
	ShareCapacity      liquid.ResourceCapacityReport
	SnapshotCapacity   liquid.ResourceCapacityReport
	SnapmirrorCapacity liquid.ResourceCapacityReport
}

type azCapacityForShareType struct {
	Shares             liquid.AZResourceCapacityReport
	Snapshots          liquid.AZResourceCapacityReport
	ShareCapacity      liquid.AZResourceCapacityReport
	SnapshotCapacity   liquid.AZResourceCapacityReport
	SnapmirrorCapacity liquid.AZResourceCapacityReport
}

func (l *Logic) scanCapacityForShareType(ctx context.Context, vst VirtualShareType, azForServiceHost map[string]liquid.AvailabilityZone, allAZs []liquid.AvailabilityZone, shareCapacityDemand, snapshotCapacityDemand, snapmirrorCapacityDemand map[liquid.AvailabilityZone]liquid.ResourceDemandInAZ) (capacityForShareType, error) {
	// list all pools for the Manila share types corresponding to this virtual share type
	allPoolsByAZ := make(map[liquid.AvailabilityZone][]*Pool)
	for _, rst := range vst.AllRealShareTypes() {
		pools, err := l.getPools(ctx, rst)
		if err != nil {
			return capacityForShareType{}, err
		}

		// sort pools by AZ
		for _, pool := range pools {
			poolAZ := azForServiceHost[pool.Host]
			if poolAZ == "" {
				logg.Info("storage pool %q (share type %q) does not match any service host", pool.Name, rst)
				poolAZ = liquid.AvailabilityZoneUnknown
			}
			if !slices.Contains(allAZs, poolAZ) {
				logg.Info("storage pool %q (share type %q) belongs to unknown AZ %q", pool.Name, rst, poolAZ)
				poolAZ = liquid.AvailabilityZoneUnknown
			}
			allPoolsByAZ[poolAZ] = append(allPoolsByAZ[poolAZ], &pool)
		}
	}

	// the following computations are performed for each AZ separately
	result := capacityForShareType{
		Shares:             liquid.ResourceCapacityReport{PerAZ: make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport)},
		Snapshots:          liquid.ResourceCapacityReport{PerAZ: make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport)},
		ShareCapacity:      liquid.ResourceCapacityReport{PerAZ: make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport)},
		SnapshotCapacity:   liquid.ResourceCapacityReport{PerAZ: make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport)},
		SnapmirrorCapacity: liquid.ResourceCapacityReport{PerAZ: make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport)},
	}
	allAZsWithUnknown := append(slices.Clone(allAZs), liquid.AvailabilityZoneUnknown)
	for _, az := range allAZsWithUnknown {
		azPools, exists := allPoolsByAZ[az]
		if !exists {
			result.Shares.PerAZ[az] = &liquid.AZResourceCapacityReport{}
			result.Snapshots.PerAZ[az] = &liquid.AZResourceCapacityReport{}
			result.ShareCapacity.PerAZ[az] = &liquid.AZResourceCapacityReport{}
			result.SnapshotCapacity.PerAZ[az] = &liquid.AZResourceCapacityReport{}
			result.SnapmirrorCapacity.PerAZ[az] = &liquid.AZResourceCapacityReport{}
			continue
		}
		azResult, err := l.scanCapacityForShareTypeAndAZ(vst, uint64(len(allAZs)), az, azPools, shareCapacityDemand[az], snapshotCapacityDemand[az], snapmirrorCapacityDemand[az])
		if err != nil {
			return capacityForShareType{}, err
		}
		result.Shares.PerAZ[az] = &azResult.Shares
		result.Snapshots.PerAZ[az] = &azResult.Snapshots
		result.ShareCapacity.PerAZ[az] = &azResult.ShareCapacity
		result.SnapshotCapacity.PerAZ[az] = &azResult.SnapshotCapacity
		result.SnapmirrorCapacity.PerAZ[az] = &azResult.SnapmirrorCapacity
	}

	return result, nil
}

func (l *Logic) scanCapacityForShareTypeAndAZ(vst VirtualShareType, azCount uint64, az liquid.AvailabilityZone, pools []*Pool, shareCapacityDemand, snapshotCapacityDemand, snapmirrorCapacityDemand liquid.ResourceDemandInAZ) (azCapacityForShareType, error) {
	// count pools and sum their capacities if they are included
	var (
		poolCount           uint64
		totalCapacityGB     float64
		allocatedCapacityGB float64
	)
	for _, pool := range pools {
		poolCount++
		allocatedCapacityGB += float64(pool.Capabilities.AllocatedCapacityGB)

		if pool.CountsUnusedCapacity() {
			totalCapacityGB += float64(pool.Capabilities.TotalCapacityGB)
		} else {
			totalCapacityGB += float64(pool.Capabilities.AllocatedCapacityGB)
			logg.Info("ignoring unused capacity in Manila storage pool %q (share type %q) in AZ %q because of hardware_state value: %q",
				pool.Name, vst.Name, az, pool.Capabilities.HardwareState)
		}
	}

	// distribute capacity and usage between the various resource types
	balance := map[string]float64{
		"shares":      1,
		"snapshots":   l.CapacityCalculation.CapacityBalance,
		"snapmirrors": 0,
	}
	logg.Debug("distributing capacity for share_type %q, AZ %q", vst.Name, az)
	distributedCapacityGiB := liquidapi.DistributeDemandFairly(uint64(totalCapacityGB), map[string]liquid.ResourceDemandInAZ{
		"shares":      shareCapacityDemand,
		"snapshots":   snapshotCapacityDemand,
		"snapmirrors": snapmirrorCapacityDemand,
	}, balance)
	logg.Debug("distributing usage for share_type %q, AZ %q", vst.Name, az)
	distributedUsageGiB := liquidapi.DistributeDemandFairly(uint64(allocatedCapacityGB), map[string]liquid.ResourceDemandInAZ{
		"shares":      {Usage: shareCapacityDemand.Usage},
		"snapshots":   {Usage: snapshotCapacityDemand.Usage},
		"snapmirrors": {Usage: snapmirrorCapacityDemand.Usage},
	}, balance)

	// build overall result
	params := l.CapacityCalculation
	var result azCapacityForShareType
	result.Shares = liquid.AZResourceCapacityReport{
		Capacity: liquidapi.SaturatingSub(params.SharesPerPool*poolCount, params.ShareNetworks/azCount),
	}
	result.Snapshots = liquid.AZResourceCapacityReport{
		Capacity: result.Shares.Capacity * params.SnapshotsPerShare,
	}
	result.ShareCapacity = liquid.AZResourceCapacityReport{
		Capacity: distributedCapacityGiB["shares"],
		Usage:    Some(distributedUsageGiB["shares"]),
	}
	result.SnapshotCapacity = liquid.AZResourceCapacityReport{
		Capacity: distributedCapacityGiB["snapshots"],
		Usage:    Some(distributedUsageGiB["snapshots"]),
	}
	result.SnapmirrorCapacity = liquid.AZResourceCapacityReport{
		Capacity: distributedCapacityGiB["snapmirrors"],
		Usage:    Some(distributedUsageGiB["snapmirrors"]),
	}

	// render subcapacities (these are not split between share_capacity and
	// snapshot_capacity because that quickly turns into an algorithmic
	// nightmare, and we have no demand (pun intended) for that right now)
	if params.WithSubcapacities {
		slices.SortFunc(pools, func(lhs, rhs *Pool) int {
			return strings.Compare(lhs.Name, rhs.Name)
		})
		for _, pool := range pools {
			usage := uint64(pool.Capabilities.AllocatedCapacityGB)
			builder := liquid.SubcapacityBuilder[StoragePoolAttributes]{
				Name:       pool.Name,
				Capacity:   uint64(pool.Capabilities.TotalCapacityGB),
				Usage:      Some(usage),
				Attributes: StoragePoolAttributes{},
			}

			if !pool.CountsUnusedCapacity() {
				builder.Attributes.ExclusionReason = "hardware_state = " + pool.Capabilities.HardwareState
				builder.Attributes.RealCapacity = builder.Capacity
				builder.Capacity = usage
			}

			subcapacity, err := builder.Finalize()
			if err != nil {
				return azCapacityForShareType{}, err
			}
			result.ShareCapacity.Subcapacities = append(result.ShareCapacity.Subcapacities, subcapacity)
		}
	}

	return result, nil
}

////////////////////////////////////////////////////////////////////////////////
// internal types for capacity reporting

// StoragePoolAttributes is the Attributes payload type for a Manila subcapacity.
type StoragePoolAttributes struct {
	ExclusionReason string `json:"exclusion_reason,omitempty"`
	// Only set when ExclusionReason is set.
	RealCapacity uint64 `json:"real_capacity,omitempty"`
}

////////////////////////////////////////////////////////////////////////////////
// custom types for Manila APIs (to work around Gophercloud limitations)

// PoolsListDetailOpts fixes the broken structure of schedulerstats.ListDetailOpts.
type poolsListDetailOpts struct {
	// TODO: remove when https://github.com/gophercloud/gophercloud/pull/3167 was merged
	ShareType string `q:"share_type,omitempty"`
}

// ToPoolsListQuery implements the schedulerstats.ListDetailOptsBuilder interface.
func (opts poolsListDetailOpts) ToPoolsListQuery() (string, error) {
	q, err := gophercloud.BuildQueryString(opts)
	return q.String(), err
}

// Pool is a custom extension of the type `schedulerstats.Pool`.
type Pool struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Capabilities struct {
		// standard fields
		TotalCapacityGB     liquids.Float64WithStringErrors `json:"total_capacity_gb"`
		AllocatedCapacityGB liquids.Float64WithStringErrors `json:"allocated_capacity_gb"`
		// CCloud extension fields
		HardwareState string `json:"hardware_state"`
	} `json:"capabilities,omitempty"`
}

// CountsUnusedCapacity returns whether this pool has all its capacity count
// towards the total (instead of just the used capacity).
func (p Pool) CountsUnusedCapacity() bool {
	switch p.Capabilities.HardwareState {
	// Pool is in buildup. At that phase, Manila already knows this storage
	// backend, but it is not handed out to customers. Shares are only deployable
	// with custom share type `integration` for integration testing. Capacity
	// should not yet be handed out.
	case "in_build":
		return false
	// Pool will be decommissioned soon. Still serving customer shares, but drain
	// is ongoing. Unused capacity should no longer be handed out.
	case "in_decom":
		return false
	// Pool is meant as replacement for another pool in_decom. Capacity should
	// not be handed out. Will only be used in tight situations to ensure there
	// is enough capacity to drain backend in decommissioning.
	case "replacing_decom":
		return false
	// Default value on deployments using the HardwareState field.
	case "live":
		return true
	// Default value on deployments not using the HardwareState field.
	case "":
		return true
	default:
		logg.Info("Manila storage pool %q has unknown hardware_state value: %q",
			p.Name, p.Capabilities.HardwareState)
		return false
	}
}

// Lists all pools for the given real share type.
func (l *Logic) getPools(ctx context.Context, rst RealShareType) ([]Pool, error) {
	allPages, err := schedulerstats.ListDetail(l.ManilaV2, poolsListDetailOpts{ShareType: string(rst)}).AllPages(ctx)
	if err != nil {
		return nil, err
	}

	var s struct {
		Pools []Pool `json:"pools"`
	}
	err = (allPages.(schedulerstats.PoolPage)).ExtractInto(&s)
	return s.Pools, err
}
