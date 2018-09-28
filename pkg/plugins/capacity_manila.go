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

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/limes"
)

type capacityManilaPlugin struct {
	cfg limes.CapacitorConfiguration
}

func init() {
	limes.RegisterCapacityPlugin(func(c limes.CapacitorConfiguration, scrapeSubcapacities map[string]map[string]bool) limes.CapacityPlugin {
		return &capacityManilaPlugin{c}
	})
}

//ID implements the limes.CapacityPlugin interface.
func (p *capacityManilaPlugin) ID() string {
	return "manila"
}

//Scrape implements the limes.CapacityPlugin interface.
func (p *capacityManilaPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID string) (map[string]map[string]limes.CapacityData, error) {
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
	if cfg.CapacityOvercommitFactor == 0 {
		cfg.CapacityOvercommitFactor = 1 //default is no overcommit
	}

	client, err := openstack.NewSharedFileSystemV2(provider, eo)
	if err != nil {
		return nil, err
	}

	//query Manila for known pools and hosts
	//filtered by share-type 'default'
	var data struct {
		Pools []struct {
			Host         string `json:"host"`
			Capabilities struct {
				TotalCapacityGB float64 `json:"total_capacity_gb"`
			} `json:"capabilities"`
		} `json:"pools"`
	}
	err = manilaGetPoolsDetailed(client).ExtractInto(&data)
	if err != nil {
		return nil, err
	}

	//count hosts and pools, find total capacity
	hosts := make(map[string]bool)
	totalCapacityGB := float64(0)
	for _, pool := range data.Pools {
		hosts[pool.Host] = true
		totalCapacityGB += pool.Capabilities.TotalCapacityGB
	}
	poolCount := uint64(len(data.Pools))
	totalCapacityGB *= cfg.CapacityOvercommitFactor

	//derive capacities
	shareCount := cfg.SharesPerPool*poolCount - cfg.ShareNetworks
	logg.Debug("sc = sp * pc - sn = %d * %d - %d = %d", cfg.SharesPerPool, poolCount, cfg.ShareNetworks, shareCount)
	if cfg.SharesPerPool*poolCount < cfg.ShareNetworks { //detect unsigned int underflow
		shareCount = 0
	}

	//NOTE: The value of `cfg.CapacityBalance` is how many capacity we give out
	//to snapshots as a fraction of the capacity given out to shares. For
	//example, with CapacityBalance = 2, we allocate 2/3 of the total capacity to
	//snapshots, and 1/3 to shares.
	b := cfg.CapacityBalance
	return map[string]map[string]limes.CapacityData{
		"sharev2": {
			"share_networks":    limes.CapacityData{Capacity: cfg.ShareNetworks},
			"shares":            limes.CapacityData{Capacity: shareCount},
			"share_snapshots":   limes.CapacityData{Capacity: cfg.SnapshotsPerShare * shareCount},
			"share_capacity":    limes.CapacityData{Capacity: uint64(1 / (b + 1) * totalCapacityGB)},
			"snapshot_capacity": limes.CapacityData{Capacity: uint64(b / (b + 1) * totalCapacityGB)},
		},
	}, nil
}

func manilaGetPoolsDetailed(client *gophercloud.ServiceClient) (result gophercloud.Result) {
	client.Microversion = "2.23" //required for filtering by share_type
	url := client.ServiceURL("scheduler-stats", "pools", "detail") + "?share_type=default"
	_, result.Err = client.Get(url, &result.Body, nil)
	return
}
