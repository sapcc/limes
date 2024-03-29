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
	"errors"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/sharedfilesystems/v2/schedulerstats"
	"github.com/gophercloud/gophercloud/openstack/sharedfilesystems/v2/services"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
)

type capacityManilaPlugin struct {
	// configuration
	ShareTypes        []ManilaShareTypeSpec `yaml:"share_types"`
	ShareNetworks     uint64                `yaml:"share_networks"`
	SharesPerPool     uint64                `yaml:"shares_per_pool"`
	SnapshotsPerShare uint64                `yaml:"snapshots_per_share"`
	CapacityBalance   float64               `yaml:"capacity_balance"`
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

func (p *capacityManilaPlugin) makeResourceName(kind string, shareType ManilaShareTypeSpec) string {
	if p.ShareTypes[0].Name == shareType.Name {
		// the resources for the first share type don't get the share type suffix
		// for backwards compatibility reasons
		return kind
	}
	return kind + "_" + shareType.Name
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) Scrape(_ core.CapacityPluginBackchannel) (result map[string]map[string]core.PerAZ[core.CapacityData], _ []byte, err error) {
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

	caps := map[string]core.PerAZ[core.CapacityData]{
		"share_networks": core.InAnyAZ(core.CapacityData{Capacity: p.ShareNetworks}),
	}
	for _, shareType := range p.ShareTypes {
		capForType, err := p.scrapeForShareType(shareType, azForServiceHost)
		if err != nil {
			return nil, nil, err
		}
		caps[p.makeResourceName("shares", shareType)] = capForType.Shares
		caps[p.makeResourceName("share_snapshots", shareType)] = capForType.Snapshots
		caps[p.makeResourceName("share_capacity", shareType)] = capForType.ShareGigabytes
		caps[p.makeResourceName("snapshot_capacity", shareType)] = capForType.SnapshotGigabytes
	}
	return map[string]map[string]core.PerAZ[core.CapacityData]{"sharev2": caps}, nil, nil
}

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	// not used by this plugin
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte) error {
	// not used by this plugin
	return nil
}

type capacityForShareType struct {
	Shares            core.PerAZ[core.CapacityData]
	Snapshots         core.PerAZ[core.CapacityData]
	ShareGigabytes    core.PerAZ[core.CapacityData]
	SnapshotGigabytes core.PerAZ[core.CapacityData]
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

func (p *capacityManilaPlugin) scrapeForShareType(shareType ManilaShareTypeSpec, azForServiceHost map[string]limes.AvailabilityZone) (capacityForShareType, error) {
	// list all pools for the Manila share types corresponding to this virtual share type
	var allPools []manilaPool
	for _, stName := range getAllManilaShareTypes(shareType) {
		allPages, err := schedulerstats.ListDetail(p.ManilaV2, poolsListDetailOpts{ShareType: stName}).AllPages()
		if err != nil {
			return capacityForShareType{}, err
		}
		pools, err := manilaExtractPools(allPages)
		if err != nil {
			return capacityForShareType{}, err
		}
		allPools = append(allPools, pools...)
	}

	//NOTE: The value of `p.CapacityBalance` is how many capacity we give out
	// to snapshots as a fraction of the capacity given out to shares. For
	// example, with CapacityBalance = 2, we allocate 2/3 of the total capacity to
	// snapshots, and 1/3 to shares.
	capBalance := p.CapacityBalance

	// count pools and their capacities
	var (
		availabilityZones          = make(map[limes.AvailabilityZone]bool)
		poolCountPerAZ             = make(map[limes.AvailabilityZone]uint64)
		totalCapacityGbPerAZ       = make(map[limes.AvailabilityZone]float64)
		allocatedCapacityGbPerAZ   = make(map[limes.AvailabilityZone]float64)
		shareSubcapacitiesPerAZ    = make(map[limes.AvailabilityZone][]any)
		snapshotSubcapacitiesPerAZ = make(map[limes.AvailabilityZone][]any)
	)
	for _, pool := range allPools {
		isIncluded := true
		if pool.Capabilities.HardwareState != "" {
			var ok bool
			isIncluded, ok = manilaIncludeByHardwareState[pool.Capabilities.HardwareState]
			if !ok {
				logg.Error("Manila storage pool %q (share type %q) has unknown hardware_state value: %q",
					pool.Name, shareType.Name, pool.Capabilities.HardwareState)
				continue
			}
			if !isIncluded {
				logg.Info("ignoring Manila storage pool %q (share type %q) because of hardware_state value: %q",
					pool.Name, shareType.Name, pool.Capabilities.HardwareState)
			}
		}

		poolAZ := azForServiceHost[pool.Host]
		if poolAZ == "" {
			logg.Info("Manila storage pool %q (share type %q) does not match any service host", pool.Name, shareType.Name)
			poolAZ = limes.AvailabilityZoneUnknown
		}

		availabilityZones[poolAZ] = true
		if isIncluded {
			poolCountPerAZ[poolAZ]++
			totalCapacityGbPerAZ[poolAZ] += pool.Capabilities.TotalCapacityGB
			allocatedCapacityGbPerAZ[poolAZ] += pool.Capabilities.AllocatedCapacityGB
		}

		if p.WithSubcapacities {
			shareSubcapa := storagePoolSubcapacity{
				PoolName:         pool.Name,
				AvailabilityZone: poolAZ,
				CapacityGiB:      getShareCapacity(pool.Capabilities.TotalCapacityGB, capBalance),
				UsageGiB:         getShareCapacity(pool.Capabilities.AllocatedCapacityGB, capBalance),
			}
			snapshotSubcapa := storagePoolSubcapacity{
				PoolName:         pool.Name,
				AvailabilityZone: poolAZ,
				CapacityGiB:      getSnapshotCapacity(pool.Capabilities.TotalCapacityGB, capBalance),
				UsageGiB:         getSnapshotCapacity(pool.Capabilities.AllocatedCapacityGB, capBalance),
			}

			if !isIncluded {
				shareSubcapa.ExclusionReason = "hardware_state = " + pool.Capabilities.HardwareState
				snapshotSubcapa.ExclusionReason = "hardware_state = " + pool.Capabilities.HardwareState
			}
			shareSubcapacitiesPerAZ[poolAZ] = append(shareSubcapacitiesPerAZ[poolAZ], shareSubcapa)
			snapshotSubcapacitiesPerAZ[poolAZ] = append(snapshotSubcapacitiesPerAZ[poolAZ], snapshotSubcapa)
		}
	}

	// derive availability zone usage and capacities
	result := capacityForShareType{
		Shares:            make(core.PerAZ[core.CapacityData]),
		Snapshots:         make(core.PerAZ[core.CapacityData]),
		ShareGigabytes:    make(core.PerAZ[core.CapacityData]),
		SnapshotGigabytes: make(core.PerAZ[core.CapacityData]),
	}
	for az := range availabilityZones {
		result.Shares[az] = &core.CapacityData{
			Capacity: getShareCount(poolCountPerAZ[az], p.SharesPerPool, (p.ShareNetworks / uint64(len(availabilityZones)))),
		}
		result.Snapshots[az] = &core.CapacityData{
			Capacity: getShareSnapshots(result.Shares[az].Capacity, p.SnapshotsPerShare),
		}
		result.ShareGigabytes[az] = &core.CapacityData{
			Capacity:      getShareCapacity(totalCapacityGbPerAZ[az], capBalance),
			Usage:         p2u64(getShareCapacity(allocatedCapacityGbPerAZ[az], capBalance)),
			Subcapacities: shareSubcapacitiesPerAZ[az],
		}
		result.SnapshotGigabytes[az] = &core.CapacityData{
			Capacity:      getSnapshotCapacity(totalCapacityGbPerAZ[az], capBalance),
			Usage:         p2u64(getSnapshotCapacity(allocatedCapacityGbPerAZ[az], capBalance)),
			Subcapacities: snapshotSubcapacitiesPerAZ[az],
		}
	}

	return result, nil
}

func getShareCount(poolCount, sharesPerPool, shareNetworks uint64) uint64 {
	shareCount := (sharesPerPool * poolCount) - shareNetworks
	logg.Debug("sc = sp * pc - sn: %d * %d - %d = %d", sharesPerPool, poolCount, shareNetworks, shareCount)
	if (sharesPerPool * poolCount) < shareNetworks { // detect unsigned int underflow
		shareCount = 0
	}
	return shareCount
}

func getShareSnapshots(shareCount, snapshotsPerShare uint64) uint64 {
	return (snapshotsPerShare * shareCount)
}

func getShareCapacity(capGB, capBalance float64) uint64 {
	return uint64(1 / (capBalance + 1) * capGB)
}

func getSnapshotCapacity(capGB, capBalance float64) uint64 {
	return uint64(capBalance / (capBalance + 1) * capGB)
}

////////////////////////////////////////////////////////////////////////////////
// We need a custom type for Pool.Capabilities to support CCloud-specific fields.

// key = value for hardware_state capability
// value = whether pools with this state count towards the reported capacity
var manilaIncludeByHardwareState = map[string]bool{
	// Default value.
	"live": true,
	// Pool is in buildup. At that phase, Manila already knows this storage
	// backend, but it is not handed out to customers. Shares are only deployable
	// with custom share type `integration` for integration testing. Capacity
	// should not yet be handed out.
	"in_build": false,
	// Pool will be decommissioned soon. Still serving customer shares, but drain
	// is ongoing. Capacity should no longer be handed out.
	"in_decom": false,
	// Pool is meant as replacement for another pool in_decom. Capacity should
	// not be handed out. Will only be used in tight situations to ensure there
	// is enough capacity to drain backend in decommissioning.
	"replacing_decom": false,
}

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

// manilaExtractPools is `schedulerstats.ExtractPools()`, but using our custom pool type.
func manilaExtractPools(p pagination.Page) ([]manilaPool, error) {
	var s struct {
		Pools []manilaPool `json:"pools"`
	}
	err := (p.(schedulerstats.PoolPage)).ExtractInto(&s)
	return s.Pools, err
}
