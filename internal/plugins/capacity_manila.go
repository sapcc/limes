/*******************************************************************************
*
* Copyright 2018 SAP SE
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

package plugins

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/sharedfilesystems/v2/schedulerstats"
	"github.com/gophercloud/gophercloud/openstack/sharedfilesystems/v2/services"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/util"
)

type capacityManilaPlugin struct {
	// configuration
	ShareTypes        []ManilaShareTypeSpec `yaml:"share_types"`
	ShareNetworks     uint64                `yaml:"share_networks"`
	SharesPerPool     uint64                `yaml:"shares_per_pool"`
	SnapshotsPerShare uint64                `yaml:"snapshots_per_share"`
	CapacityBalance   float64               `yaml:"capacity_balance"`
	WithSnapmirror    bool                  `yaml:"with_snapmirror"`
	WithSubcapacities bool                  `yaml:"with_subcapacities"`
	// connections
	ManilaV2 *gophercloud.ServiceClient `yaml:"-"`
}

// This type is shared with the Cinder capacitor.
type storagePoolSubcapacity struct {
	PoolName         string                 `json:"pool_name"`
	AvailabilityZone limes.AvailabilityZone `json:"az"`
	CapacityGiB      uint64                 `json:"capacity_gib"`
	UsageGiB         uint64                 `json:"usage_gib"`
	// Manila only (SAP-specific extension)
	ExclusionReason string `json:"exclusion_reason"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityManilaPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	if len(p.ShareTypes) == 0 {
		return errors.New("capacity plugin manila: missing required configuration field manila.share_types")
	}
	if p.ShareNetworks == 0 {
		return errors.New("capacity plugin manila: missing required configuration field manila.share_networks")
	}
	if p.SharesPerPool == 0 {
		return errors.New("capacity plugin manila: missing required configuration field manila.shares_per_pool")
	}
	if p.SnapshotsPerShare == 0 {
		return errors.New("capacity plugin manila: missing required configuration field manila.snapshots_per_share")
	}

	p.ManilaV2, err = openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return err
	}
	p.ManilaV2.Microversion = "2.23" // required for filtering pools by share_type
	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) PluginTypeID() string {
	return "manila"
}

func (p *capacityManilaPlugin) makeResourceName(kind string, shareType ManilaShareTypeSpec) limesresources.ResourceName {
	if p.ShareTypes[0].Name == shareType.Name {
		// the resources for the first share type don't get the share type suffix
		// for backwards compatibility reasons
		return limesresources.ResourceName(kind)
	}
	return limesresources.ResourceName(kind + "_" + shareType.Name)
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) Scrape(backchannel core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (result map[limes.ServiceType]map[limesresources.ResourceName]core.PerAZ[core.CapacityData], _ []byte, err error) {
	allPages, err := services.List(p.ManilaV2, nil).AllPages()
	if err != nil {
		return nil, nil, err
	}
	allServices, err := services.ExtractServices(allPages)
	if err != nil {
		return nil, nil, err
	}

	azForServiceHost := make(map[string]limes.AvailabilityZone)
	for _, element := range allServices {
		if element.Binary == "manila-share" {
			// element.Host has the format backendHostname@backendName
			fields := strings.Split(element.Host, "@")
			if len(fields) != 2 {
				logg.Error("Expected a Manila service host in the format \"backendHostname@backendName\", got %q with ID %d", element.Host, element.ID)
			} else {
				azForServiceHost[fields[0]] = limes.AvailabilityZone(element.Zone)
			}
		}
	}

	caps := map[limesresources.ResourceName]core.PerAZ[core.CapacityData]{
		"share_networks": core.InAnyAZ(core.CapacityData{Capacity: p.ShareNetworks}),
	}
	for _, shareType := range p.ShareTypes {
		shareCapacityDemand, err := backchannel.GetGlobalResourceDemand("sharev2", p.makeResourceName("share_capacity", shareType))
		if err != nil {
			return nil, nil, err
		}
		shareOvercommitFactor, err := backchannel.GetOvercommitFactor("sharev2", p.makeResourceName("share_capacity", shareType))
		if err != nil {
			return nil, nil, err
		}
		for az, demand := range shareCapacityDemand {
			shareCapacityDemand[az] = shareOvercommitFactor.ApplyInReverseToDemand(demand)
		}

		snapshotCapacityDemand, err := backchannel.GetGlobalResourceDemand("sharev2", p.makeResourceName("snapshot_capacity", shareType))
		if err != nil {
			return nil, nil, err
		}
		snapshotOvercommitFactor, err := backchannel.GetOvercommitFactor("sharev2", p.makeResourceName("snapshot_capacity", shareType))
		if err != nil {
			return nil, nil, err
		}
		for az, demand := range snapshotCapacityDemand {
			snapshotCapacityDemand[az] = snapshotOvercommitFactor.ApplyInReverseToDemand(demand)
		}

		var snapmirrorCapacityDemand map[limes.AvailabilityZone]core.ResourceDemand
		if p.WithSnapmirror {
			snapmirrorCapacityDemand, err = backchannel.GetGlobalResourceDemand("sharev2", p.makeResourceName("snapmirror_capacity", shareType))
			if err != nil {
				return nil, nil, err
			}
			snapmirrorOvercommitFactor, err := backchannel.GetOvercommitFactor("sharev2", p.makeResourceName("snapmirror_capacity", shareType))
			if err != nil {
				return nil, nil, err
			}
			for az, demand := range snapmirrorCapacityDemand {
				snapmirrorCapacityDemand[az] = snapmirrorOvercommitFactor.ApplyInReverseToDemand(demand)
			}
		}
		// ^ NOTE: If p.WithSnapmirror is false, `snapmirrorCapacityDemand[az]` is always zero-valued
		// and thus no capacity will be allocated to the snapmirror_capacity resource.

		capForType, err := p.scrapeForShareType(shareType, azForServiceHost, allAZs, shareCapacityDemand, snapshotCapacityDemand, snapmirrorCapacityDemand)
		if err != nil {
			return nil, nil, err
		}
		caps[p.makeResourceName("shares", shareType)] = capForType.Shares
		caps[p.makeResourceName("share_snapshots", shareType)] = capForType.Snapshots
		caps[p.makeResourceName("share_capacity", shareType)] = capForType.ShareGigabytes
		caps[p.makeResourceName("snapshot_capacity", shareType)] = capForType.SnapshotGigabytes
		if p.WithSnapmirror {
			caps[p.makeResourceName("snapmirror_capacity", shareType)] = capForType.SnapmirrorGigabytes
		}
	}
	return map[limes.ServiceType]map[limesresources.ResourceName]core.PerAZ[core.CapacityData]{"sharev2": caps}, nil, nil
}

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	// not used by this plugin
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte, capacitorID string) error {
	// not used by this plugin
	return nil
}

type capacityForShareType struct {
	Shares              core.PerAZ[core.CapacityData]
	Snapshots           core.PerAZ[core.CapacityData]
	ShareGigabytes      core.PerAZ[core.CapacityData]
	SnapshotGigabytes   core.PerAZ[core.CapacityData]
	SnapmirrorGigabytes core.PerAZ[core.CapacityData]
}

type azCapacityForShareType struct {
	Shares              core.CapacityData
	Snapshots           core.CapacityData
	ShareGigabytes      core.CapacityData
	SnapshotGigabytes   core.CapacityData
	SnapmirrorGigabytes core.CapacityData
}

type poolsListDetailOpts struct {
	// upstream type (schedulerstats.ListDetailOpts) does not work because of wrong field tags (`json:"..."` instead of `q:"..."`)
	//TODO: fix upstream; I'm doing this quick fix now because I don't have the time to submit an upstream PR and figure out how to write testcases for them
	ShareType string `q:"share_type,omitempty"`
}

// ToPoolsListQuery implements the schedulerstats.ListDetailOptsBuilder interface.
func (opts poolsListDetailOpts) ToPoolsListQuery() (string, error) {
	q, err := gophercloud.BuildQueryString(opts)
	return q.String(), err
}

func (p *capacityManilaPlugin) scrapeForShareType(shareType ManilaShareTypeSpec, azForServiceHost map[string]limes.AvailabilityZone, allAZs []limes.AvailabilityZone, shareCapacityDemand, snapshotCapacityDemand, snapmirrorCapacityDemand map[limes.AvailabilityZone]core.ResourceDemand) (capacityForShareType, error) {
	// list all pools for the Manila share types corresponding to this virtual share type
	allPoolsByAZ := make(map[limes.AvailabilityZone][]*manilaPool)
	for _, stName := range getAllManilaShareTypes(shareType) {
		allPages, err := schedulerstats.ListDetail(p.ManilaV2, poolsListDetailOpts{ShareType: stName}).AllPages()
		if err != nil {
			return capacityForShareType{}, err
		}
		pools, err := manilaExtractPools(allPages)
		if err != nil {
			return capacityForShareType{}, err
		}

		// sort pools by AZ
		for _, pool := range pools {
			poolAZ := azForServiceHost[pool.Host]
			if poolAZ == "" {
				logg.Info("Manila storage pool %q (share type %q) does not match any service host", pool.Name, shareType.Name)
				poolAZ = limes.AvailabilityZoneUnknown
			}
			if !slices.Contains(allAZs, poolAZ) {
				logg.Info("Manila storage pool %q (share type %q) belongs to unknown AZ %q", pool.Name, shareType.Name, poolAZ)
				poolAZ = limes.AvailabilityZoneUnknown
			}
			allPoolsByAZ[poolAZ] = append(allPoolsByAZ[poolAZ], &pool)
		}
	}

	// the following computations are performed for each AZ separately
	result := capacityForShareType{
		Shares:              make(core.PerAZ[core.CapacityData]),
		Snapshots:           make(core.PerAZ[core.CapacityData]),
		ShareGigabytes:      make(core.PerAZ[core.CapacityData]),
		SnapshotGigabytes:   make(core.PerAZ[core.CapacityData]),
		SnapmirrorGigabytes: make(core.PerAZ[core.CapacityData]),
	}
	for az, azPools := range allPoolsByAZ {
		azResult := p.scrapeForShareTypeAndAZ(shareType, uint64(len(allAZs)), az, azPools, shareCapacityDemand[az], snapshotCapacityDemand[az], snapmirrorCapacityDemand[az])
		result.Shares[az] = &azResult.Shares
		result.Snapshots[az] = &azResult.Snapshots
		result.ShareGigabytes[az] = &azResult.ShareGigabytes
		result.SnapshotGigabytes[az] = &azResult.SnapshotGigabytes
		result.SnapmirrorGigabytes[az] = &azResult.SnapmirrorGigabytes
	}

	return result, nil
}

func (p *capacityManilaPlugin) scrapeForShareTypeAndAZ(shareType ManilaShareTypeSpec, azCount uint64, az limes.AvailabilityZone, pools []*manilaPool, shareCapacityDemand, snapshotCapacityDemand, snapmirrorCapacityDemand core.ResourceDemand) azCapacityForShareType {
	// count pools and sum their capacities if they are included
	var (
		poolCount           uint64
		totalCapacityGB     float64
		allocatedCapacityGB float64
	)
	for _, pool := range pools {
		poolCount++
		allocatedCapacityGB += pool.Capabilities.AllocatedCapacityGB

		if pool.CountsUnusedCapacity() {
			totalCapacityGB += pool.Capabilities.TotalCapacityGB
		} else {
			totalCapacityGB += pool.Capabilities.AllocatedCapacityGB
			logg.Info("ignoring unused capacity in Manila storage pool %q (share type %q) in AZ %q because of hardware_state value: %q",
				pool.Name, shareType.Name, az, pool.Capabilities.HardwareState)
		}
	}

	// distribute capacity and usage between the various resource types
	logg.Debug("distributing capacity for share_type %q, AZ %q", shareType.Name, az)
	distributedCapacityGiB := p.distributeByDemand(uint64(totalCapacityGB), map[string]core.ResourceDemand{
		"shares":      shareCapacityDemand,
		"snapshots":   snapshotCapacityDemand,
		"snapmirrors": snapmirrorCapacityDemand,
	})
	logg.Debug("distributing usage for share_type %q, AZ %q", shareType.Name, az)
	distributedUsageGiB := p.distributeByDemand(uint64(allocatedCapacityGB), map[string]core.ResourceDemand{
		"shares":      {Usage: shareCapacityDemand.Usage},
		"snapshots":   {Usage: snapshotCapacityDemand.Usage},
		"snapmirrors": {Usage: snapmirrorCapacityDemand.Usage},
	})

	// build overall result
	var result azCapacityForShareType
	result.Shares = core.CapacityData{
		Capacity: saturatingSub(p.SharesPerPool*poolCount, p.ShareNetworks/azCount),
	}
	result.Snapshots = core.CapacityData{
		Capacity: result.Shares.Capacity * p.SnapshotsPerShare,
	}
	result.ShareGigabytes = core.CapacityData{
		Capacity: distributedCapacityGiB["shares"],
		Usage:    p2u64(distributedUsageGiB["shares"]),
	}
	result.SnapshotGigabytes = core.CapacityData{
		Capacity: distributedCapacityGiB["snapshots"],
		Usage:    p2u64(distributedUsageGiB["snapshots"]),
	}
	result.SnapmirrorGigabytes = core.CapacityData{
		Capacity: distributedCapacityGiB["snapmirrors"],
		Usage:    p2u64(distributedUsageGiB["snapmirrors"]),
	}

	// render subcapacities (these are not split between share_capacity and
	// snapshot_capacity because that quickly turns into an algorithmic
	// nightmare, and we have no demand (pun intended) for that right now)
	if p.WithSubcapacities {
		slices.SortFunc(pools, func(lhs, rhs *manilaPool) int {
			return strings.Compare(lhs.Name, rhs.Name)
		})
		for _, pool := range pools {
			subcapacity := storagePoolSubcapacity{
				PoolName:         pool.Name,
				AvailabilityZone: az,
				CapacityGiB:      uint64(pool.Capabilities.TotalCapacityGB),
				UsageGiB:         uint64(pool.Capabilities.AllocatedCapacityGB),
			}

			if !pool.CountsUnusedCapacity() {
				subcapacity.ExclusionReason = "hardware_state = " + pool.Capabilities.HardwareState
			}
			result.ShareGigabytes.Subcapacities = append(result.ShareGigabytes.Subcapacities, subcapacity)
		}
	}

	return result
}

// This implements the method we use to distribute capacity and usage between shares and snapshots:
// Each tier of demand is distributed fairly (while supplies last).
// Then anything that is not covered by demand is distributed according to the configured CapacityBalance.
//
// For capacity, each tier of demand is considered.
// For usage, the caller will set all demand fields except for Usage to 0.
func (p *capacityManilaPlugin) distributeByDemand(totalAmount uint64, demands map[string]core.ResourceDemand) map[string]uint64 {
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
		cb := p.CapacityBalance
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
// We need a custom type for Pool.Capabilities to support CCloud-specific fields.

// manilaPool is a custom extension of the type `schedulerstats.Pool`.
type manilaPool struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Capabilities struct {
		// standard fields
		TotalCapacityGB     float64 `json:"total_capacity_gb"`
		AllocatedCapacityGB float64 `json:"allocated_capacity_gb"`
		// CCloud extension fields
		HardwareState string `json:"hardware_state"`
	} `json:"capabilities,omitempty"`
}

// Returns whether this pool has all its capacity count towards the total
// (instead of just the used capacity).
func (p manilaPool) CountsUnusedCapacity() bool {
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
	// Default value.
	case "live":
		return true
	default:
		logg.Info("Manila storage pool %q has unknown hardware_state value: %q",
			p.Name, p.Capabilities.HardwareState)
		return true
	}
}

// manilaExtractPools is `schedulerstats.ExtractPools()`, but using our custom pool type.
func manilaExtractPools(p pagination.Page) ([]manilaPool, error) {
	var s struct {
		Pools []manilaPool `json:"pools"`
	}
	err := (p.(schedulerstats.PoolPage)).ExtractInto(&s)
	return s.Pools, err
}
