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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
)

type capacityManilaPlugin struct {
	ShareTypes        []ManilaShareTypeSpec `yaml:"share_types"`
	ShareNetworks     uint64                `yaml:"share_networks"`
	SharesPerPool     uint64                `yaml:"shares_per_pool"`
	SnapshotsPerShare uint64                `yaml:"snapshots_per_share"`
	CapacityBalance   float64               `yaml:"capacity_balance"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityManilaPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubcapacities map[string]map[string]bool) error {
	if len(p.ShareTypes) == 0 {
		return errors.New("capacity plugin manila: missing required configuration field manila.share_types")
	}
	err := compileManilaShareTypeSpecs(p.ShareTypes)
	if err != nil {
		return err
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
	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) PluginTypeID() string {
	return "manila"
}

func (p *capacityManilaPlugin) makeResourceName(kind string, shareType ManilaShareTypeSpec) string {
	if p.ShareTypes[0].Name == shareType.Name {
		//the resources for the first share type don't get the share type suffix
		//for backwards compatibility reasons
		return kind
	}
	return kind + "_" + shareType.Name
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (result map[string]map[string]core.CapacityData, _ string, err error) {
	client, err := openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return nil, "", err
	}
	client.Microversion = "2.23" //required for filtering pools by share_type

	allPages, err := services.List(client, nil).AllPages()
	if err != nil {
		return nil, "", err
	}
	allServices, err := services.ExtractServices(allPages)
	if err != nil {
		return nil, "", err
	}

	azForServiceHost := make(map[string]string)
	for _, element := range allServices {
		if element.Binary == "manila-share" {
			//element.Host has the format backendHostname@backendName
			fields := strings.Split(element.Host, "@")
			if len(fields) != 2 {
				logg.Error("Expected a Manila service host in the format \"backendHostname@backendName\", got %q with ID %d", element.Host, element.ID)
			} else {
				azForServiceHost[fields[0]] = element.Zone
			}
		}
	}

	caps := map[string]core.CapacityData{
		"share_networks": {Capacity: p.ShareNetworks},
	}
	for _, shareType := range p.ShareTypes {
		capForType, err := p.scrapeForShareType(shareType, client, azForServiceHost)
		if err != nil {
			return nil, "", err
		}
		caps[p.makeResourceName("shares", shareType)] = capForType.Shares
		caps[p.makeResourceName("share_snapshots", shareType)] = capForType.Snapshots
		caps[p.makeResourceName("share_capacity", shareType)] = capForType.ShareGigabytes
		caps[p.makeResourceName("snapshot_capacity", shareType)] = capForType.SnapshotGigabytes
	}
	return map[string]map[string]core.CapacityData{"sharev2": caps}, "", nil
}

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics string) error {
	//not used by this plugin
	return nil
}

type capacityForShareType struct {
	Shares            core.CapacityData
	Snapshots         core.CapacityData
	ShareGigabytes    core.CapacityData
	SnapshotGigabytes core.CapacityData
}

func (p *capacityManilaPlugin) scrapeForShareType(shareType ManilaShareTypeSpec, client *gophercloud.ServiceClient, azForServiceHost map[string]string) (capacityForShareType, error) {
	//list all pools for the Manila share types corresponding to this virtual share type
	var allPools []schedulerstats.Pool
	for _, stName := range getAllManilaShareTypes(shareType) {
		allPages, err := schedulerstats.ListDetail(client, schedulerstats.ListDetailOpts{ShareType: stName}).AllPages()
		if err != nil {
			return capacityForShareType{}, err
		}
		pools, err := schedulerstats.ExtractPools(allPages)
		if err != nil {
			return capacityForShareType{}, err
		}
		allPools = append(allPools, pools...)
	}

	//count pools and their capacities
	var (
		totalPoolCount  = uint64(len(allPools))
		totalCapacityGb = float64(0)

		availabilityZones        = make(map[string]bool)
		poolCountPerAZ           = make(map[string]uint64)
		totalCapacityGbPerAZ     = make(map[string]float64)
		allocatedCapacityGbPerAZ = make(map[string]float64)
	)
	for _, pool := range allPools {
		totalCapacityGb += pool.Capabilities.TotalCapacityGB

		poolAZ := azForServiceHost[pool.Host]
		if poolAZ == "" {
			logg.Info("Manila storage pool %q (share type %q) does not match any service host", pool.Name, shareType.Name)
			poolAZ = "unknown"
		}
		availabilityZones[poolAZ] = true
		poolCountPerAZ[poolAZ]++
		totalCapacityGbPerAZ[poolAZ] += pool.Capabilities.TotalCapacityGB
		allocatedCapacityGbPerAZ[poolAZ] += pool.Capabilities.AllocatedCapacityGB
	}

	//NOTE: The value of `p.CapacityBalance` is how many capacity we give out
	//to snapshots as a fraction of the capacity given out to shares. For
	//example, with CapacityBalance = 2, we allocate 2/3 of the total capacity to
	//snapshots, and 1/3 to shares.
	capBalance := p.CapacityBalance

	//derive availability zone usage and capacities
	var (
		shareCountPerAZ       = make(map[string]*core.CapacityDataForAZ)
		shareSnapshotsPerAZ   = make(map[string]*core.CapacityDataForAZ)
		shareCapacityPerAZ    = make(map[string]*core.CapacityDataForAZ)
		snapshotCapacityPerAZ = make(map[string]*core.CapacityDataForAZ)
	)
	for az := range availabilityZones {
		shareCountPerAZ[az] = &core.CapacityDataForAZ{
			Capacity: getShareCount(poolCountPerAZ[az], p.SharesPerPool, (p.ShareNetworks / uint64(len(availabilityZones)))),
		}
		shareSnapshotsPerAZ[az] = &core.CapacityDataForAZ{
			Capacity: getShareSnapshots(shareCountPerAZ[az].Capacity, p.SnapshotsPerShare),
		}
		shareCapacityPerAZ[az] = &core.CapacityDataForAZ{
			Capacity: getShareCapacity(totalCapacityGbPerAZ[az], capBalance),
			Usage:    getShareCapacity(allocatedCapacityGbPerAZ[az], capBalance),
		}
		snapshotCapacityPerAZ[az] = &core.CapacityDataForAZ{
			Capacity: getSnapshotCapacity(totalCapacityGbPerAZ[az], capBalance),
			Usage:    getSnapshotCapacity(allocatedCapacityGbPerAZ[az], capBalance),
		}
	}

	//derive cluster level capacities
	var (
		totalShareCount       = getShareCount(totalPoolCount, p.SharesPerPool, p.ShareNetworks)
		totalShareSnapshots   = getShareSnapshots(totalShareCount, p.SnapshotsPerShare)
		totalShareCapacity    = getShareCapacity(totalCapacityGb, capBalance)
		totalSnapshotCapacity = getSnapshotCapacity(totalCapacityGb, capBalance)
	)

	return capacityForShareType{
		Shares:            core.CapacityData{Capacity: totalShareCount, CapacityPerAZ: shareCountPerAZ},
		Snapshots:         core.CapacityData{Capacity: totalShareSnapshots, CapacityPerAZ: shareSnapshotsPerAZ},
		ShareGigabytes:    core.CapacityData{Capacity: totalShareCapacity, CapacityPerAZ: shareCapacityPerAZ},
		SnapshotGigabytes: core.CapacityData{Capacity: totalSnapshotCapacity, CapacityPerAZ: snapshotCapacityPerAZ},
	}, nil
}

func getShareCount(poolCount, sharesPerPool, shareNetworks uint64) uint64 {
	shareCount := (sharesPerPool * poolCount) - shareNetworks
	logg.Debug("sc = sp * pc - sn: %d * %d - %d = %d", sharesPerPool, poolCount, shareNetworks, shareCount)
	if (sharesPerPool * poolCount) < shareNetworks { //detect unsigned int underflow
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
