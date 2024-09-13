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
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/sharedfilesystems/v2/schedulerstats"
	"github.com/gophercloud/gophercloud/v2/openstack/sharedfilesystems/v2/services"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/liquids"
	"github.com/sapcc/limes/internal/util"
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
	for az, azPools := range allPoolsByAZ {
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
	logg.Debug("distributing capacity for share_type %q, AZ %q", vst.Name, az)
	distributedCapacityGiB := l.distributeByDemand(uint64(totalCapacityGB), map[string]liquid.ResourceDemandInAZ{
		"shares":      shareCapacityDemand,
		"snapshots":   snapshotCapacityDemand,
		"snapmirrors": snapmirrorCapacityDemand,
	})
	logg.Debug("distributing usage for share_type %q, AZ %q", vst.Name, az)
	distributedUsageGiB := l.distributeByDemand(uint64(allocatedCapacityGB), map[string]liquid.ResourceDemandInAZ{
		"shares":      {Usage: shareCapacityDemand.Usage},
		"snapshots":   {Usage: snapshotCapacityDemand.Usage},
		"snapmirrors": {Usage: snapmirrorCapacityDemand.Usage},
	})

	// build overall result
	params := l.CapacityCalculation
	var result azCapacityForShareType
	result.Shares = liquid.AZResourceCapacityReport{
		Capacity: liquids.SaturatingSub(params.SharesPerPool*poolCount, params.ShareNetworks/azCount),
	}
	result.Snapshots = liquid.AZResourceCapacityReport{
		Capacity: result.Shares.Capacity * params.SnapshotsPerShare,
	}
	result.ShareCapacity = liquid.AZResourceCapacityReport{
		Capacity: distributedCapacityGiB["shares"],
		Usage:    liquids.PointerTo(distributedUsageGiB["shares"]),
	}
	result.SnapshotCapacity = liquid.AZResourceCapacityReport{
		Capacity: distributedCapacityGiB["snapshots"],
		Usage:    liquids.PointerTo(distributedUsageGiB["snapshots"]),
	}
	result.SnapmirrorCapacity = liquid.AZResourceCapacityReport{
		Capacity: distributedCapacityGiB["snapmirrors"],
		Usage:    liquids.PointerTo(distributedUsageGiB["snapmirrors"]),
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
				Usage:      liquids.PointerTo(usage),
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

// This implements the method we use to distribute capacity and usage between shares and snapshots:
// Each tier of demand is distributed fairly (while supplies last).
// Then anything that is not covered by demand is distributed according to the configured CapacityBalance.
//
// For capacity, each tier of demand is considered.
// For usage, the caller will set all demand fields except for Usage to 0.
func (l *Logic) distributeByDemand(totalAmount uint64, demands map[string]liquid.ResourceDemandInAZ) map[string]uint64 {
	// setup phase to make each of the paragraphs below as identical as possible (for clarity)
	requests := make(map[string]uint64)
	result := make(map[string]uint64)
	remaining := totalAmount

	// tier 1: usage
	for k, demand := range demands {
		requests[k] = demand.Usage
	}
	grantedAmount := util.DistributeFairly(remaining, requests)
	for k := range demands {
		remaining -= grantedAmount[k]
		result[k] += grantedAmount[k]
	}
	if logg.ShowDebug {
		resultJSON, _ := json.Marshal(result) //nolint:errcheck // no reasonable way for this to fail, also only debug log
		logg.Debug("distributeByDemand after phase 1: " + string(resultJSON))
	}

	// tier 2: unused commitments
	for k, demand := range demands {
		requests[k] = demand.UnusedCommitments
	}
	grantedAmount = util.DistributeFairly(remaining, requests)
	for k := range demands {
		remaining -= grantedAmount[k]
		result[k] += grantedAmount[k]
	}
	if logg.ShowDebug {
		resultJSON, _ := json.Marshal(result) //nolint:errcheck // no reasonable way for this to fail, also only debug log
		logg.Debug("distributeByDemand after phase 2: " + string(resultJSON))
	}

	// tier 3: pending commitments
	for k, demand := range demands {
		requests[k] = demand.PendingCommitments
	}
	grantedAmount = util.DistributeFairly(remaining, requests)
	for k := range demands {
		remaining -= grantedAmount[k]
		result[k] += grantedAmount[k]
	}
	if logg.ShowDebug {
		resultJSON, _ := json.Marshal(result) //nolint:errcheck // no reasonable way for this to fail, also only debug log
		logg.Debug("distributeByDemand after phase 2: " + string(resultJSON))
	}

	// final phase: distribute all remaining capacity according to the configured CapacityBalance
	//
	// NOTE: The CapacityBalance value says how much capacity we give out
	// to snapshots as a fraction of the capacity given out to shares. For
	// example, with CapacityBalance = 2, we allocate 2/3 of the total capacity to
	// snapshots, and 1/3 to shares.
	if remaining > 0 {
		cb := l.CapacityCalculation.CapacityBalance
		portionForSnapshots := uint64(cb / (cb + 1) * float64(remaining))
		portionForShares := remaining - portionForSnapshots

		result["snapshots"] += portionForSnapshots
		result["shares"] += portionForShares
	}
	if logg.ShowDebug {
		resultJSON, _ := json.Marshal(result) //nolint:errcheck // no reasonable way for this to fail, also only debug log
		logg.Debug("distributeByDemand after CapacityBalance: " + string(resultJSON))
	}

	return result
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