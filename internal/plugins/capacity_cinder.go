/*******************************************************************************
*
* Copyright 2017 SAP SE
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
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/schedulerstats"
	"github.com/gophercloud/gophercloud/v2/openstack/blockstorage/v3/services"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
)

type capacityCinderPlugin struct {
	// configuration
	VolumeTypes map[string]struct {
		VolumeBackendName string `yaml:"volume_backend_name"`
		IsDefault         bool   `yaml:"default"`
	} `yaml:"volume_types"`
	WithSubcapacities bool `yaml:"with_subcapacities"`
	// connections
	CinderV3 *gophercloud.ServiceClient `yaml:"-"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityCinderPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityCinderPlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	if len(p.VolumeTypes) == 0 {
		//nolint:stylecheck //Cinder is a proper name
		return errors.New("Cinder capacity plugin: missing required configuration field cinder.volume_types")
	}

	p.CinderV3, err = openstack.NewBlockStorageV3(provider, eo)
	return err
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacityCinderPlugin) PluginTypeID() string {
	return "cinder"
}

func (p *capacityCinderPlugin) makeResourceName(volumeType string) limesresources.ResourceName {
	// the resources for the volume type marked as default don't get the volume
	// type suffix for backwards compatibility reasons
	if p.VolumeTypes[volumeType].IsDefault {
		return "capacity"
	}
	return limesresources.ResourceName("capacity_" + volumeType)
	//NOTE: We don't make estimates for no. of snapshots/volumes in this
	// capacitor. These values depend highly on the backend. (On SAP CC, we
	// configure capacity for snapshots/volumes via the "manual" capacitor.)
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityCinderPlugin) Scrape(ctx context.Context, _ core.CapacityPluginBackchannel, allAZs []limes.AvailabilityZone) (result map[limes.ServiceType]map[limesresources.ResourceName]core.PerAZ[core.CapacityData], serializedMetrics []byte, err error) {
	// list storage pools
	var poolData struct {
		StoragePools []struct {
			Name         string `json:"name"`
			Capabilities struct {
				VolumeBackendName   string          `json:"volume_backend_name"`
				TotalCapacityGB     float64OrString `json:"total_capacity_gb"`
				AllocatedCapacityGB float64OrString `json:"allocated_capacity_gb"`
				// we need a custom type because `schedulerstats.Capabilities` does not expose this custom_attributes field
				CustomAttributes struct {
					CinderState string `json:"cinder_state"`
				} `json:"custom_attributes"`
			} `json:"capabilities"`
		} `json:"pools"`
	}

	allPages, err := schedulerstats.List(p.CinderV3, schedulerstats.ListOpts{Detail: true}).AllPages(ctx)
	if err != nil {
		return nil, nil, err
	}
	err = allPages.(schedulerstats.StoragePoolPage).ExtractInto(&poolData)
	if err != nil {
		return nil, nil, err
	}

	// list service hosts
	allPages, err = services.List(p.CinderV3, nil).AllPages(ctx)
	if err != nil {
		return nil, nil, err
	}
	allServices, err := services.ExtractServices(allPages)
	if err != nil {
		return nil, nil, err
	}

	serviceHostsPerAZ := make(map[string][]string)
	for _, element := range allServices {
		if element.Binary == "cinder-volume" {
			// element.Host has the format backendHostname@backendName
			serviceHostsPerAZ[element.Zone] = append(serviceHostsPerAZ[element.Zone], element.Host)
		}
	}

	capaData := make(map[limesresources.ResourceName]core.PerAZ[core.CapacityData])
	volumeTypesByBackendName := make(map[string]string)
	for volumeType, cfg := range p.VolumeTypes {
		volumeTypesByBackendName[cfg.VolumeBackendName] = volumeType
		capaData[p.makeResourceName(volumeType)] = make(core.PerAZ[core.CapacityData])
	}

	// add results from scheduler-stats
	for _, pool := range poolData.StoragePools {
		// on pools that are slated for decommissioning (state "drain")
		// or reserved for absorbing payloads from draining pools (state "reserved"),
		// no quota should be given out for the free capacity;
		// only actively used capacity is included in the total (for business continuity purposes)
		exclusionReason := ""
		state := pool.Capabilities.CustomAttributes.CinderState
		if state == "drain" || state == "reserved" {
			logg.Info("Cinder capacity plugin: pool %q with %g GiB capacity has cinder_state %q -- only considering %g GiB used capacity",
				pool.Name, pool.Capabilities.TotalCapacityGB, state, pool.Capabilities.AllocatedCapacityGB)
			exclusionReason = "cinder_state = " + state
		}

		volumeType, ok := volumeTypesByBackendName[pool.Capabilities.VolumeBackendName]
		if !ok {
			logg.Info("Cinder capacity plugin: skipping pool %q with unknown volume_backend_name %q", pool.Name, pool.Capabilities.VolumeBackendName)
			continue
		}
		logg.Debug("Cinder capacity plugin: considering pool %q with volume_backend_name %q for volume type %q", pool.Name, pool.Capabilities.VolumeBackendName, volumeType)

		var poolAZ limes.AvailabilityZone
		for az, hosts := range serviceHostsPerAZ {
			for _, v := range hosts {
				// pool.Name has the format backendHostname@backendName#backendPoolName
				if strings.Contains(pool.Name, v) {
					poolAZ = limes.AvailabilityZone(az)
					break
				}
			}
		}
		if poolAZ == "" {
			logg.Info("Cinder storage pool %q does not match any service host", pool.Name)
			poolAZ = limes.AvailabilityZoneUnknown
		}

		resourceName := p.makeResourceName(volumeType)
		capa := capaData[resourceName][poolAZ]
		if capa == nil {
			capa = &core.CapacityData{}
			capaData[resourceName][poolAZ] = capa
		}

		if exclusionReason == "" {
			capa.Capacity += uint64(pool.Capabilities.TotalCapacityGB)
		} else {
			capa.Capacity += uint64(pool.Capabilities.AllocatedCapacityGB)
		}
		if capa.Usage == nil {
			capa.Usage = p2u64(0)
		}
		*capa.Usage += uint64(pool.Capabilities.AllocatedCapacityGB)

		if p.WithSubcapacities {
			capa.Subcapacities = append(capa.Subcapacities, storagePoolSubcapacity{
				PoolName:         pool.Name,
				AvailabilityZone: poolAZ,
				CapacityGiB:      uint64(pool.Capabilities.TotalCapacityGB),
				UsageGiB:         uint64(pool.Capabilities.AllocatedCapacityGB),
				ExclusionReason:  exclusionReason,
			})
		}
	}

	return map[limes.ServiceType]map[limesresources.ResourceName]core.PerAZ[core.CapacityData]{"volumev2": capaData}, nil, nil
}

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityCinderPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	// not used by this plugin
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityCinderPlugin) CollectMetrics(ch chan<- prometheus.Metric, serializedMetrics []byte, capacitorID string) error {
	// not used by this plugin
	return nil
}

////////////////////////////////////////////////////////////////////////////////
// OpenStack is being a mess once again

// Used for the "total_capacity_gb" field in Cinder pools, which may be a string like "infinite" or "" (unknown).
type float64OrString float64

// UnmarshalJSON implements the json.Unmarshaler interface.
func (f *float64OrString) UnmarshalJSON(buf []byte) error {
	//ref: <https://github.com/gophercloud/gophercloud/blob/7137f0845e8cf2210601f867e7ddd9f54bb72859/openstack/blockstorage/extensions/schedulerstats/results.go#L60-L74>

	if buf[0] == '"' {
		var str string
		err := json.Unmarshal(buf, &str)
		if err != nil {
			return err
		}

		if str == "infinite" {
			*f = float64OrString(math.Inf(+1))
		} else {
			*f = 0
		}
		return nil
	}

	var val float64
	err := json.Unmarshal(buf, &val)
	*f = float64OrString(val)
	return err
}
