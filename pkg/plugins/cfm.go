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
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/limes/pkg/limes"
)

type cfmPlugin struct {
	cfg limes.ServiceConfiguration

	shareserversCache        []cfmShareserver
	shareserversCacheExpires time.Time
}

func init() {
	limes.RegisterQuotaPlugin(func(c limes.ServiceConfiguration, scrapeSubresources map[string]bool) limes.QuotaPlugin {
		return &cfmPlugin{cfg: c}
	})
}

//Init implements the limes.QuotaPlugin interface.
func (p *cfmPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

//ServiceInfo implements the limes.QuotaPlugin interface.
func (p *cfmPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "database",
		ProductName: "cfm",
		Area:        "storage",
	}
}

//Resources implements the limes.QuotaPlugin interface.
func (p *cfmPlugin) Resources() []limes.ResourceInfo {
	return []limes.ResourceInfo{{
		Name: "cfm_share_capacity",
		Unit: limes.UnitBytes,
		//need explicit permission to set quota for this service
		ExternallyManaged: !p.cfg.CFM.Authoritative,
	}}
}

//Scrape implements the limes.QuotaPlugin interface.
func (p *cfmPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string) (map[string]limes.ResourceData, error) {
	client, err := newCFMClient(provider, eo)
	if err != nil {
		return nil, err
	}

	//prefer the new quota API if it is available
	var data struct {
		StorageQuota struct {
			SizeLimitBytes int64 `json:"size_limit"`
			Usage          struct {
				BytesUsed uint64 `json:"potential_growth_size"`
			} `json:"usage"`
		} `json:"storage_quota"`
	}
	err = client.GetQuotaSet(projectUUID).ExtractInto(&data)
	if err == nil {
		logg.Info("using CFM quota set for project %s", projectUUID)
		return map[string]limes.ResourceData{
			"cfm_share_capacity": {
				Quota: data.StorageQuota.SizeLimitBytes,
				Usage: data.StorageQuota.Usage.BytesUsed,
			},
		}, nil
	}

	//never use the old API when we're instructed to only read quotas
	if p.cfg.CFM.Authoritative {
		if _, ok := err.(cfmNotFoundError); ok {
			return map[string]limes.ResourceData{"cfm_share_capacity": {Quota: 0, Usage: 0}}, nil
		}
		return nil, err
	}

	return p.scrapeOld(client, projectUUID)
}

func (p *cfmPlugin) scrapeOld(client *cfmClient, projectUUID string) (map[string]limes.ResourceData, error) {
	//cache the result of cfmListShareservers(), it's mildly expensive
	now := time.Now()
	if p.shareserversCache == nil || p.shareserversCacheExpires.Before(now) {
		shareservers, err := client.ListShareservers()
		if err != nil {
			return nil, err
		}
		p.shareserversCache = shareservers
		p.shareserversCacheExpires = now.Add(5 * time.Minute)
	}
	shareservers := p.shareserversCache

	result := limes.ResourceData{Quota: 0, Usage: 0}
	for _, shareserver := range shareservers {
		if shareserver.ProjectUUID != projectUUID {
			continue
		}

		shareserverDetailed, err := client.GetShareserver(shareserver.DetailsURL)
		if err != nil {
			return nil, err
		}

		result.Quota += int64(shareserverDetailed.BytesUsed)
		result.Usage += shareserverDetailed.BytesUsed
	}

	return map[string]limes.ResourceData{"cfm_share_capacity": result}, nil
}

//SetQuota implements the limes.QuotaPlugin interface.
func (p *cfmPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	if !p.cfg.CFM.Authoritative {
		return errors.New("the database/cfm_share_capacity resource is externally managed")
	}

	client, err := newCFMClient(provider, eo)
	if err != nil {
		return err
	}
	quotaBytes := quotas["cfm_share_capacity"]
	err = client.UpdateQuotaSet(projectUUID, quotaBytes)
	if _, ok := err.(cfmNotFoundError); ok {
		if quotaBytes == 0 {
			return nil //nothing to do: quota does not exist, but is also not wanted
		}
		err = client.CreateQuotaSet(projectUUID, quotaBytes)
	}
	return err
}
