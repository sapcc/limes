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
	"fmt"
	"math/big"

	"github.com/gophercloud/gophercloud"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/pkg/core"
)

type cfmPlugin struct {
	cfg       core.ServiceConfiguration
	projectID string
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &cfmPlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *cfmPlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, c core.ServiceConfiguration, scrapeSubresources map[string]bool) (err error) {
	p.cfg = c
	if !p.cfg.CFM.Authoritative {
		return fmt.Errorf(`quota plugin "database" (for CFM service) does not support "authoritative = false" mode anymore`)
	}
	p.projectID, err = getProjectIDForToken(provider, eo)
	return err
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *cfmPlugin) PluginTypeID() string {
	return "database"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *cfmPlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "database",
		ProductName: "cfm",
		Area:        "storage",
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *cfmPlugin) Resources() []limesresources.ResourceInfo {
	return []limesresources.ResourceInfo{{
		Name: "cfm_share_capacity",
		Unit: limes.UnitBytes,
	}}
}

// Rates implements the core.QuotaPlugin interface.
func (p *cfmPlugin) Rates() []limesrates.RateInfo {
	return nil
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *cfmPlugin) ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *cfmPlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject) (result map[string]core.ResourceData, _ string, err error) {
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

	if _, ok := err.(cfmNotFoundError); ok {
		return map[string]core.ResourceData{"cfm_share_capacity": {Quota: 0, Usage: 0}}, "", nil
	}
	return nil, "", err
}

// IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *cfmPlugin) IsQuotaAcceptableForProject(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	//not required for this plugin
	return nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *cfmPlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
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

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *cfmPlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *cfmPlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID string, project core.KeystoneProject, serializedMetrics string) error {
	//not used by this plugin
	return nil
}
