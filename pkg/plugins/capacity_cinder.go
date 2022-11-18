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
	"errors"
	"strings"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/extensions/schedulerstats"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/extensions/services"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/pkg/core"
)

type capacityCinderPlugin struct {
	VolumeTypes map[string]struct {
		VolumeBackendName string `yaml:"volume_backend_name"`
		IsDefault         bool   `yaml:"default"`
	} `yaml:"volume_types"`
}

func init() {
	core.CapacityPluginRegistry.Add(func() core.CapacityPlugin { return &capacityCinderPlugin{} })
}

// Init implements the core.CapacityPlugin interface.
func (p *capacityCinderPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, scrapeSubcapacities map[string]map[string]bool) error {
	if len(p.VolumeTypes) == 0 {
		//nolint:stylecheck //Cinder is a proper name
		return errors.New("Cinder capacity plugin: missing required configuration field cinder.volume_types")
	}
	return nil
}

// PluginTypeID implements the core.CapacityPlugin interface.
func (p *capacityCinderPlugin) PluginTypeID() string {
	return "cinder"
}

func (p *capacityCinderPlugin) makeResourceName(volumeType string) string {
	//the resources for the volume type marked as default don't get the volume
	//type suffix for backwards compatibility reasons
	if p.VolumeTypes[volumeType].IsDefault {
		return "capacity"
	}
	return "capacity_" + volumeType
	//NOTE: We don't make estimates for no. of snapshots/volumes in this
	//capacitor. These values depend highly on the backend. (On SAP CC, we
	//configure capacity for snapshots/volumes via the "manual" capacitor.)
}

// Scrape implements the core.CapacityPlugin interface.
func (p *capacityCinderPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (result map[string]map[string]core.CapacityData, serializedMetrics string, err error) {
	client, err := openstack.NewBlockStorageV3(provider, eo)
	if err != nil {
		return nil, "", err
	}

	//Get absolute limits for a tenant
	allPages, err := schedulerstats.List(client, schedulerstats.ListOpts{Detail: true}).AllPages()
	if err != nil {
		return nil, "", err
	}
	allStoragePools, err := schedulerstats.ExtractStoragePools(allPages)
	if err != nil {
		return nil, "", err
	}

	allPages, err = services.List(client, nil).AllPages()
	if err != nil {
		return nil, "", err
	}
	allServices, err := services.ExtractServices(allPages)
	if err != nil {
		return nil, "", err
	}

	serviceHostsPerAZ := make(map[string][]string)
	for _, element := range allServices {
		if element.Binary == "cinder-volume" {
			//element.Host has the format backendHostname@backendName
			serviceHostsPerAZ[element.Zone] = append(serviceHostsPerAZ[element.Zone], element.Host)
		}
	}

	capaData := make(map[string]*core.CapacityData)
	volumeTypesByBackendName := make(map[string]string)
	for volumeType, cfg := range p.VolumeTypes {
		volumeTypesByBackendName[cfg.VolumeBackendName] = volumeType
		capaData[p.makeResourceName(volumeType)] = &core.CapacityData{
			Capacity:      0,
			CapacityPerAZ: make(map[string]*core.CapacityDataForAZ),
		}
	}

	//add results from scheduler-stats
	for _, element := range allStoragePools {
		volumeType, ok := volumeTypesByBackendName[element.Capabilities.VolumeBackendName]
		if !ok {
			logg.Info("Cinder capacity plugin: skipping pool %q with unknown volume_backend_name %q", element.Name, element.Capabilities.VolumeBackendName)
			continue
		}

		logg.Debug("Cinder capacity plugin: considering pool %q with volume_backend_name %q for volume type %q", element.Name, element.Capabilities.VolumeBackendName, volumeType)

		resourceName := p.makeResourceName(volumeType)
		capaData[resourceName].Capacity += uint64(element.Capabilities.TotalCapacityGB)

		var poolAZ string
		for az, hosts := range serviceHostsPerAZ {
			for _, v := range hosts {
				//element.Name has the format backendHostname@backendName#backendPoolName
				if strings.Contains(element.Name, v) {
					poolAZ = az
					break
				}
			}
		}
		if poolAZ == "" {
			logg.Info("Cinder storage pool %q does not match any service host", element.Name)
			poolAZ = "unknown"
		}
		if _, ok := capaData[resourceName].CapacityPerAZ[poolAZ]; !ok {
			capaData[resourceName].CapacityPerAZ[poolAZ] = &core.CapacityDataForAZ{}
		}

		azCapaData := capaData[resourceName].CapacityPerAZ[poolAZ]
		azCapaData.Capacity += uint64(element.Capabilities.TotalCapacityGB)
		azCapaData.Usage += uint64(element.Capabilities.AllocatedCapacityGB)
	}

	capaDataFinal := make(map[string]core.CapacityData)
	for k, v := range capaData {
		capaDataFinal[k] = *v
	}
	return map[string]map[string]core.CapacityData{"volumev2": capaDataFinal}, "", nil
}

// DescribeMetrics implements the core.CapacityPlugin interface.
func (p *capacityCinderPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

// CollectMetrics implements the core.CapacityPlugin interface.
func (p *capacityCinderPlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID, serializedMetrics string) error {
	//not used by this plugin
	return nil
}
