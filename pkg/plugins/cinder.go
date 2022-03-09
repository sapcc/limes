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
	"encoding/json"
	"errors"
	"math/big"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/extensions/quotasets"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v2/volumes"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
)

type cinderPlugin struct {
	cfg           core.ServiceConfiguration
	scrapeVolumes bool
}

func init() {
	core.RegisterQuotaPlugin(func(c core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		return &cinderPlugin{
			cfg:           c,
			scrapeVolumes: scrapeSubresources["volumes"],
		}
	})
}

//Init implements the core.QuotaPlugin interface.
func (p *cinderPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	if len(p.cfg.VolumeV2.VolumeTypes) == 0 {
		return errors.New("quota plugin volumev2: missing required configuration field volumev2.volume_types")
	}
	return nil
}

//ServiceInfo implements the core.QuotaPlugin interface.
func (p *cinderPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "volumev2",
		ProductName: "cinder",
		Area:        "storage",
	}
}

//Resources implements the core.QuotaPlugin interface.
func (p *cinderPlugin) Resources() []limes.ResourceInfo {
	result := make([]limes.ResourceInfo, 0, 3*len(p.cfg.VolumeV2.VolumeTypes))
	for _, volumeType := range p.cfg.VolumeV2.VolumeTypes {
		category := p.makeResourceName("volumev2", volumeType)
		result = append(result,
			limes.ResourceInfo{
				Name:     p.makeResourceName("capacity", volumeType),
				Unit:     limes.UnitGibibytes,
				Category: category,
			},
			limes.ResourceInfo{
				Name:     p.makeResourceName("snapshots", volumeType),
				Unit:     limes.UnitNone,
				Category: category,
			},
			limes.ResourceInfo{
				Name:     p.makeResourceName("volumes", volumeType),
				Unit:     limes.UnitNone,
				Category: category,
			},
		)
	}
	return result
}

//Rates implements the core.QuotaPlugin interface.
func (p *cinderPlugin) Rates() []limes.RateInfo {
	return nil
}

func (p *cinderPlugin) makeResourceName(kind, volumeType string) string {
	if p.cfg.VolumeV2.VolumeTypes[0] == volumeType {
		//the resources for the first volume type don't get the volume type suffix
		//for backwards compatibility reasons
		return kind
	}
	return kind + "_" + volumeType
}

type quotaSetField core.ResourceData

func (f *quotaSetField) UnmarshalJSON(buf []byte) error {
	//The `quota_set` field in the os-quota-sets response is mostly
	//map[string]quotaSetField, but there is also an "id" key containing a
	//string. Skip deserialization of that value.
	if buf[0] == '"' {
		return nil
	}

	var data struct {
		Quota int64  `json:"limit"`
		Usage uint64 `json:"in_use"`
	}
	err := json.Unmarshal(buf, &data)
	if err == nil {
		f.Quota = data.Quota
		f.Usage = data.Usage
	}
	return err
}

func (f quotaSetField) ToResourceData(subresources []interface{}) core.ResourceData {
	return core.ResourceData{
		Quota:        f.Quota,
		Usage:        f.Usage,
		Subresources: subresources,
	}
}

//ScrapeRates implements the core.QuotaPlugin interface.
func (p *cinderPlugin) ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

//Scrape implements the core.QuotaPlugin interface.
func (p *cinderPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject) (map[string]core.ResourceData, string, error) {
	client, err := openstack.NewBlockStorageV2(provider, eo)
	if err != nil {
		return nil, "", err
	}
	var data struct {
		QuotaSet map[string]quotaSetField `json:"quota_set"`
	}
	err = quotasets.GetUsage(client, project.UUID).ExtractInto(&data)
	if err != nil {
		return nil, "", err
	}

	volumeData := make(map[string][]interface{})
	if p.scrapeVolumes {
		isVolumeType := make(map[string]bool)
		for _, volumeType := range p.cfg.VolumeV2.VolumeTypes {
			isVolumeType[volumeType] = true
		}

		listOpts := volumes.ListOpts{
			AllTenants: true,
			TenantID:   project.UUID,
		}

		err := volumes.List(client, listOpts).EachPage(func(page pagination.Page) (bool, error) {
			vols, err := volumes.ExtractVolumes(page)
			if err != nil {
				return false, err
			}

			for _, volume := range vols {
				volumeType := volume.VolumeType
				//group subresources with unknown volume types under the default volume type
				if !isVolumeType[volumeType] {
					volumeType = p.cfg.VolumeV2.VolumeTypes[0]
				}

				volumeData[volumeType] = append(volumeData[volumeType], map[string]interface{}{
					"id":     volume.ID,
					"name":   volume.Name,
					"status": volume.Status,
					"size": limes.ValueWithUnit{
						Value: uint64(volume.Size),
						Unit:  limes.UnitGibibytes,
					},
					"availability_zone": volume.AvailabilityZone,
				})
			}
			return true, nil
		})
		if err != nil {
			return nil, "", err
		}
	}

	rd := make(map[string]core.ResourceData)
	for _, volumeType := range p.cfg.VolumeV2.VolumeTypes {
		rd[p.makeResourceName("capacity", volumeType)] = data.QuotaSet["gigabytes_"+volumeType].ToResourceData(nil)
		rd[p.makeResourceName("snapshots", volumeType)] = data.QuotaSet["snapshots_"+volumeType].ToResourceData(nil)
		rd[p.makeResourceName("volumes", volumeType)] = data.QuotaSet["volumes_"+volumeType].ToResourceData(
			volumeData[volumeType],
		)
	}
	return rd, "", nil
}

//IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *cinderPlugin) IsQuotaAcceptableForProject(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	//not required for this plugin
	return nil
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *cinderPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	var requestData struct {
		QuotaSet map[string]uint64 `json:"quota_set"`
	}
	requestData.QuotaSet = make(map[string]uint64)

	for _, volumeType := range p.cfg.VolumeV2.VolumeTypes {
		quotaCapacity := quotas[p.makeResourceName("capacity", volumeType)]
		requestData.QuotaSet["gigabytes_"+volumeType] = quotaCapacity
		requestData.QuotaSet["gigabytes"] += quotaCapacity

		quotaSnapshots := quotas[p.makeResourceName("snapshots", volumeType)]
		requestData.QuotaSet["snapshots_"+volumeType] = quotaSnapshots
		requestData.QuotaSet["snapshots"] += quotaSnapshots

		quotaVolumes := quotas[p.makeResourceName("volumes", volumeType)]
		requestData.QuotaSet["volumes_"+volumeType] = quotaVolumes
		requestData.QuotaSet["volumes"] += quotaVolumes
	}

	client, err := openstack.NewBlockStorageV2(provider, eo)
	if err != nil {
		return err
	}

	url := client.ServiceURL("os-quota-sets", project.UUID)
	_, err = client.Put(url, requestData, nil, &gophercloud.RequestOpts{OkCodes: []int{200}})
	return err
}

//DescribeMetrics implements the core.QuotaPlugin interface.
func (p *cinderPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

//CollectMetrics implements the core.QuotaPlugin interface.
func (p *cinderPlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID string, project core.KeystoneProject, serializedMetrics string) error {
	//not used by this plugin
	return nil
}
