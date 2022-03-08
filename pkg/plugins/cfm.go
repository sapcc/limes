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
	"math/big"
	"time"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes"
	"github.com/sapcc/limes/pkg/core"
)

type cfmPlugin struct {
	cfg       core.ServiceConfiguration
	projectID string

	shareserversCache        []cfmShareserver
	shareserversCacheExpires time.Time
}

func init() {
	core.RegisterQuotaPlugin(func(c core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		return &cfmPlugin{cfg: c}
	})
}

//Init implements the core.QuotaPlugin interface.
func (p *cfmPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	p.projectID, err = getProjectIDForToken(provider, eo)
	return err
}

//ServiceInfo implements the core.QuotaPlugin interface.
func (p *cfmPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "database",
		ProductName: "cfm",
		Area:        "storage",
	}
}

//Resources implements the core.QuotaPlugin interface.
func (p *cfmPlugin) Resources() []limes.ResourceInfo {
	return []limes.ResourceInfo{{
		Name: "cfm_share_capacity",
		Unit: limes.UnitBytes,
		//need explicit permission to set quota for this service
		ExternallyManaged: !p.cfg.CFM.Authoritative,
	}}
}

//Rates implements the core.QuotaPlugin interface.
func (p *cfmPlugin) Rates() []limes.RateInfo {
	return nil
}

//ScrapeRates implements the core.QuotaPlugin interface.
func (p *cfmPlugin) ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

//Scrape implements the core.QuotaPlugin interface.
func (p *cfmPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject) (map[string]core.ResourceData, string, error) {
	client, err := newCFMClient(provider, eo, p.projectID)
	if err != nil {
		return nil, "", err
	}

	//prefer the new quota API if it is available
	var data struct {
		StorageQuota struct {
			SizeLimitBytes int64 `json:"size_limit"`
			Usage          struct {
				Size      uint64 `json:"size"`
				BytesUsed uint64 `json:"size_used"`
			} `json:"usage"`
		} `json:"storage_quota"`
	}
	err = client.GetQuotaSet(project.UUID).ExtractInto(&data)
	if err == nil {
		logg.Info("using CFM quota set for project %s", project.UUID)
		if !p.cfg.CFM.ReportPhysicalUsage {
			return map[string]core.ResourceData{
				"cfm_share_capacity": {
					Quota: data.StorageQuota.SizeLimitBytes,
					Usage: data.StorageQuota.Usage.BytesUsed,
				},
			}, "", nil
		}
		physicalUsage := data.StorageQuota.Usage.BytesUsed
		return map[string]core.ResourceData{
			"cfm_share_capacity": {
				Quota:         data.StorageQuota.SizeLimitBytes,
				Usage:         data.StorageQuota.Usage.Size,
				PhysicalUsage: &physicalUsage,
			},
		}, "", nil
	}

	//never use the old API when we're instructed to only read quotas
	if p.cfg.CFM.Authoritative {
		if _, ok := err.(cfmNotFoundError); ok {
			return map[string]core.ResourceData{"cfm_share_capacity": {Quota: 0, Usage: 0}}, "", nil
		}
		return nil, "", err
	}

	return p.scrapeOld(client, project.UUID)
}

func (p *cfmPlugin) scrapeOld(client *cfmClient, projectUUID string) (map[string]core.ResourceData, string, error) {
	//cache the result of cfmListShareservers(), it's mildly expensive
	now := time.Now()
	if p.shareserversCache == nil || p.shareserversCacheExpires.Before(now) {
		shareservers, err := client.ListShareservers()
		if err != nil {
			return nil, "", err
		}
		p.shareserversCache = shareservers
		p.shareserversCacheExpires = now.Add(5 * time.Minute)
	}
	shareservers := p.shareserversCache

	result := core.ResourceData{Quota: 0, Usage: 0}
	for _, shareserver := range shareservers {
		if shareserver.ProjectUUID != projectUUID {
			continue
		}

		shareserverDetailed, err := client.GetShareserver(shareserver.DetailsURL)
		if err != nil {
			return nil, "", err
		}

		result.Quota += int64(shareserverDetailed.BytesUsed)
		result.Usage += shareserverDetailed.BytesUsed
	}

	return map[string]core.ResourceData{"cfm_share_capacity": result}, "", nil
}

//IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *cfmPlugin) IsQuotaAcceptableForProject(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	//not required for this plugin
	return nil
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *cfmPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	if !p.cfg.CFM.Authoritative {
		return errors.New("the database/cfm_share_capacity resource is externally managed")
	}

	client, err := newCFMClient(provider, eo, p.projectID)
	if err != nil {
		return err
	}
	quotaBytes := quotas["cfm_share_capacity"]
	err = client.UpdateQuotaSet(project.UUID, quotaBytes)
	if _, ok := err.(cfmNotFoundError); ok {
		if quotaBytes == 0 {
			return nil //nothing to do: quota does not exist, but is also not wanted
		}
		err = client.CreateQuotaSet(project.UUID, quotaBytes)
	}
	return err
}

//DescribeMetrics implements the core.QuotaPlugin interface.
func (p *cfmPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

//CollectMetrics implements the core.QuotaPlugin interface.
func (p *cfmPlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID string, project core.KeystoneProject, serializedMetrics string) error {
	//not used by this plugin
	return nil
}
