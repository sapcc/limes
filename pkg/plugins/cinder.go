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
	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/blockstorage/v2/volumes"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/sapcc/limes/pkg/limes"
)

type cinderPlugin struct {
	cfg           limes.ServiceConfiguration
	scrapeVolumes bool
}

var cinderResources = []limes.ResourceInfo{
	{
		Name: "capacity",
		Unit: limes.UnitGibibytes,
	},
	{
		Name: "snapshots",
		Unit: limes.UnitNone,
	},
	{
		Name: "volumes",
		Unit: limes.UnitNone,
	},
}

func init() {
	limes.RegisterQuotaPlugin(func(c limes.ServiceConfiguration, scrapeSubresources map[string]bool) limes.QuotaPlugin {
		return &cinderPlugin{
			cfg:           c,
			scrapeVolumes: scrapeSubresources["volumes"],
		}
	})
}

//ServiceInfo implements the limes.QuotaPlugin interface.
func (p *cinderPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "volumev2",
		ProductName: "cinder",
		Area:        "storage",
	}
}

//Resources implements the limes.QuotaPlugin interface.
func (p *cinderPlugin) Resources() []limes.ResourceInfo {
	return cinderResources
}

func (p *cinderPlugin) Client(provider *gophercloud.ProviderClient) (*gophercloud.ServiceClient, error) {
	return openstack.NewBlockStorageV2(provider,
		gophercloud.EndpointOpts{Availability: gophercloud.AvailabilityPublic},
	)
}

//Scrape implements the limes.QuotaPlugin interface.
func (p *cinderPlugin) Scrape(provider *gophercloud.ProviderClient, domainUUID, projectUUID string) (map[string]limes.ResourceData, error) {
	client, err := p.Client(provider)
	if err != nil {
		return nil, err
	}

	var result gophercloud.Result
	url := client.ServiceURL("os-quota-sets", projectUUID) + "?usage=True"
	_, err = client.Get(url, &result.Body, nil)
	if err != nil {
		return nil, err
	}

	type field struct {
		Quota int64  `json:"limit"`
		Usage uint64 `json:"in_use"`
	}
	var data struct {
		QuotaSet struct {
			Capacity  field `json:"gigabytes"`
			Snapshots field `json:"snapshots"`
			Volumes   field `json:"volumes"`
		} `json:"quota_set"`
	}
	err = result.ExtractInto(&data)
	if err != nil {
		return nil, err
	}

	var volumeData []interface{}
	if p.scrapeVolumes {
		listOpts := cinderVolumeListOpts{
			AllTenants: true,
			ProjectID:  projectUUID,
		}

		err := volumes.List(client, listOpts).EachPage(func(page pagination.Page) (bool, error) {
			vols, err := volumes.ExtractVolumes(page)
			if err != nil {
				return false, err
			}

			for _, volume := range vols {
				volumeData = append(volumeData, map[string]interface{}{
					"id":     volume.ID,
					"name":   volume.Name,
					"status": volume.Status,
					"size": limes.ValueWithUnit{
						Value: uint64(volume.Size),
						Unit:  limes.UnitGibibytes,
					},
				})
			}
			return true, nil
		})
		if err != nil {
			return nil, err
		}
	}

	return map[string]limes.ResourceData{
		"capacity": {
			Quota: data.QuotaSet.Capacity.Quota,
			Usage: data.QuotaSet.Capacity.Usage,
		},
		"snapshots": {
			Quota: data.QuotaSet.Snapshots.Quota,
			Usage: data.QuotaSet.Snapshots.Usage,
		},
		"volumes": {
			Quota:        data.QuotaSet.Volumes.Quota,
			Usage:        data.QuotaSet.Volumes.Usage,
			Subresources: volumeData,
		},
	}, nil
}

//SetQuota implements the limes.QuotaPlugin interface.
func (p *cinderPlugin) SetQuota(provider *gophercloud.ProviderClient, domainUUID, projectUUID string, quotas map[string]uint64) error {
	requestData := map[string]map[string]uint64{
		"quota_set": {
			"gigabytes": quotas["capacity"],
			"snapshots": quotas["snapshots"],
			"volumes":   quotas["volumes"],
		},
	}

	client, err := p.Client(provider)
	if err != nil {
		return err
	}

	url := client.ServiceURL("os-quota-sets", projectUUID)
	_, err = client.Put(url, requestData, nil, &gophercloud.RequestOpts{OkCodes: []int{200}})
	return err
}

type cinderVolumeListOpts struct {
	AllTenants bool   `q:"all_tenants"`
	ProjectID  string `q:"project_id"`
}

func (opts cinderVolumeListOpts) ToVolumeListQuery() (string, error) {
	q, err := gophercloud.BuildQueryString(opts)
	return q.String(), err
}
