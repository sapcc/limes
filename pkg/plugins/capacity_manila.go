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
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/core"
)

type capacityManilaPlugin struct {
	cfg core.CapacitorConfiguration
}

func init() {
	core.RegisterCapacityPlugin(func(c core.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) core.CapacityPlugin {
		return &capacityManilaPlugin{c}
	})
}

//Init implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

//ID implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) ID() string {
	return "manila"
}

//Scrape implements the core.CapacityPlugin interface.
func (p *capacityManilaPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID string) (map[string]map[string]core.CapacityData, error) {
	cfg := p.cfg.Manila
	if cfg.ShareNetworks == 0 {
		return nil, errors.New("missing configuration parameter: share_networks")
	}
	if cfg.SharesPerPool == 0 {
		return nil, errors.New("missing configuration parameter: shares_per_pool")
	}
	if cfg.SnapshotsPerShare == 0 {
		return nil, errors.New("missing configuration parameter: snapshots_per_share")
	}

	client, err := openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return nil, err
	}

	//query Manila for known pools and hosts
	//filtered by share-type 'default'
	var poolData struct {
		Pools []struct {
			Name         string `json:"name"`
			Host         string `json:"host"`
			Capabilities struct {
				TotalCapacityGb     float64 `json:"total_capacity_gb"`
				AllocatedCapacityGb float64 `json:"allocated_capacity_gb"`
			} `json:"capabilities"`
		} `json:"pools"`
	}
	err = manilaGetPoolsDetailed(client).ExtractInto(&poolData)
	if err != nil {
		return nil, err
	}

	//TODO: replace "os-services" with services, when API version > 2.6
	var result gophercloud.Result
	url := client.ServiceURL("os-services")
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	var servicesData struct {
		Services []struct {
			ID               int    `json:"id"`
			Binary           string `json:"binary"`
			AvailabilityZone string `json:"zone"`
			Host             string `json:"host"`
		} `json:"services"`
	}
	err = result.ExtractInto(&servicesData)
	if err != nil {
		return nil, err
	}

	serviceHostsPerAZ := make(map[string][]string)
	for _, element := range servicesData.Services {
		if element.Binary == "manila-share" {
			//element.Host has the format backendHostname@backendName
			fields := strings.Split(element.Host, "@")
			if len(fields) != 2 {
				logg.Error("Expected a Manila service host in the format \"backendHostname@backendName\", got %q with ID %d", element.Host, element.ID)
			} else {
				serviceHostsPerAZ[element.AvailabilityZone] = append(serviceHostsPerAZ[element.AvailabilityZone], fields[0])
			}
		}
	}

	//count pools and their capacities
	var (
		totalPoolCount  = uint64(len(poolData.Pools))
		totalCapacityGb = float64(0)

		availabilityZones        = make(map[string]bool)
		poolCountPerAZ           = make(map[string]uint64)
		totalCapacityGbPerAZ     = make(map[string]float64)
		allocatedCapacityGbPerAZ = make(map[string]float64)
	)
	for _, pool := range poolData.Pools {
		totalCapacityGb += pool.Capabilities.TotalCapacityGb

		var poolAZ string
		for az, hosts := range serviceHostsPerAZ {
			for _, v := range hosts {
				if pool.Host == v {
					poolAZ = az
					break
				}
			}
		}
		if poolAZ == "" {
			logg.Info("Manila storage pool %q does not match any service host", pool.Name)
			poolAZ = "unknown"
		}
		availabilityZones[poolAZ] = true
		poolCountPerAZ[poolAZ]++
		totalCapacityGbPerAZ[poolAZ] += pool.Capabilities.TotalCapacityGb
		allocatedCapacityGbPerAZ[poolAZ] += pool.Capabilities.AllocatedCapacityGb
	}

	//NOTE: The value of `cfg.CapacityBalance` is how many capacity we give out
	//to snapshots as a fraction of the capacity given out to shares. For
	//example, with CapacityBalance = 2, we allocate 2/3 of the total capacity to
	//snapshots, and 1/3 to shares.
	capBalance := cfg.CapacityBalance

	//derive availability zone usage and capacities
	var (
		shareCountPerAZ       = make(map[string]*core.CapacityDataForAZ)
		shareSnapshotsPerAZ   = make(map[string]*core.CapacityDataForAZ)
		shareCapacityPerAZ    = make(map[string]*core.CapacityDataForAZ)
		snapshotCapacityPerAZ = make(map[string]*core.CapacityDataForAZ)
	)
	for az := range availabilityZones {
		shareCountPerAZ[az] = &core.CapacityDataForAZ{
			Capacity: getShareCount(poolCountPerAZ[az], cfg.SharesPerPool, (cfg.ShareNetworks / uint64(len(availabilityZones)))),
		}
		shareSnapshotsPerAZ[az] = &core.CapacityDataForAZ{
			Capacity: getShareSnapshots(shareCountPerAZ[az].Capacity, cfg.SnapshotsPerShare),
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
		totalShareCount       = getShareCount(totalPoolCount, cfg.SharesPerPool, cfg.ShareNetworks)
		totalShareSnapshots   = getShareSnapshots(totalShareCount, cfg.SnapshotsPerShare)
		totalShareCapacity    = getShareCapacity(totalCapacityGb, capBalance)
		totalSnapshotCapacity = getSnapshotCapacity(totalCapacityGb, capBalance)
	)

	return map[string]map[string]core.CapacityData{
		"sharev2": {
			"share_networks":    core.CapacityData{Capacity: cfg.ShareNetworks},
			"shares":            core.CapacityData{Capacity: totalShareCount, CapacityPerAZ: shareCountPerAZ},
			"share_snapshots":   core.CapacityData{Capacity: totalShareSnapshots, CapacityPerAZ: shareSnapshotsPerAZ},
			"share_capacity":    core.CapacityData{Capacity: totalShareCapacity, CapacityPerAZ: shareCapacityPerAZ},
			"snapshot_capacity": core.CapacityData{Capacity: totalSnapshotCapacity, CapacityPerAZ: snapshotCapacityPerAZ},
		},
	}, nil
}

func manilaGetPoolsDetailed(client *gophercloud.ServiceClient) (result gophercloud.Result) {
	client.Microversion = "2.23" //required for filtering by share_type
	url := client.ServiceURL("scheduler-stats", "pools", "detail") + "?share_type=default"
	_, result.Err = client.Get(url, &result.Body, nil)
	client.Microversion = ""
	return
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
