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
	"math/big"

	"github.com/gophercloud/gophercloud"
	"github.com/gophercloud/gophercloud/openstack"
	"github.com/gophercloud/gophercloud/openstack/dns/v2/quotas"
	"github.com/gophercloud/gophercloud/openstack/dns/v2/recordsets"
	"github.com/gophercloud/gophercloud/openstack/dns/v2/zones"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"

	"github.com/sapcc/limes/pkg/core"
)

type designatePlugin struct {
	cfg core.ServiceConfiguration
}

var designateResources = []limes.ResourceInfo{
	{
		Name: "zones",
		Unit: limes.UnitNone,
	},
	{
		//this quota means "recordsets per zone", not "recordsets per project"!
		Name: "recordsets",
		Unit: limes.UnitNone,
	},
}

func init() {
	core.RegisterQuotaPlugin(func(c core.ServiceConfiguration, scrapeSubresources map[string]bool) core.QuotaPlugin {
		return &designatePlugin{c}
	})
}

//Init implements the core.QuotaPlugin interface.
func (p *designatePlugin) Init(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) error {
	return nil
}

//ServiceInfo implements the core.QuotaPlugin interface.
func (p *designatePlugin) ServiceInfo() limes.ServiceInfo {
	return limes.ServiceInfo{
		Type:        "dns",
		ProductName: "designate",
		Area:        "dns",
	}
}

//Resources implements the core.QuotaPlugin interface.
func (p *designatePlugin) Resources() []limes.ResourceInfo {
	return designateResources
}

//Rates implements the core.QuotaPlugin interface.
func (p *designatePlugin) Rates() []limes.RateInfo {
	return nil
}

//ScrapeRates implements the core.QuotaPlugin interface.
func (p *designatePlugin) ScrapeRates(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, prevSerializedState string) (result map[string]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

//Scrape implements the core.QuotaPlugin interface.
func (p *designatePlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject) (map[string]core.ResourceData, string, error) {
	client, err := openstack.NewDNSV2(provider, eo)
	if err != nil {
		return nil, "", err
	}

	//query quotas
	client.MoreHeaders = map[string]string{"X-Auth-All-Projects": "true"}
	quotas, err := quotas.Get(client, project.UUID).Extract()
	if err != nil {
		return nil, "", err
	}

	//to query usage, start by listing all zones
	zoneIDs, err := dnsListZoneIDs(client, project.UUID)
	if err != nil {
		return nil, "", err
	}

	//query "recordsets per zone" usage by counting recordsets in each zone
	//individually (we could count all recordsets over the all project at once,
	//but that won't help since the quota applies per individual zone)
	maxRecordsetsPerZone := 0
	for _, zoneID := range zoneIDs {
		client.MoreHeaders = map[string]string{
			"X-Auth-All-Projects":    "false",
			"X-Auth-Sudo-Project-Id": project.UUID,
		}
		allZones, err := recordsets.ListByZone(client, zoneID, recordsets.ListOpts{Limit: 1}).AllPages()
		if err != nil {
			return nil, "", err
		}
		allRecordSets, err := recordsets.ExtractRecordSets(allZones)
		if err != nil {
			return nil, "", err
		}
		if maxRecordsetsPerZone < allRecordSets[0].Metadata.TotalCount {
			maxRecordsetsPerZone = allRecordSets[0].Metadata.TotalCount
		}
	}

	return map[string]core.ResourceData{
		"zones": {
			Quota: int64(quotas.Zones),
			Usage: uint64(len(zoneIDs)),
		},
		"recordsets": {
			Quota: int64(quotas.ZoneRecordsets),
			Usage: uint64(maxRecordsetsPerZone),
		},
	}, "", nil
}

//IsQuotaAcceptableForProject implements the core.QuotaPlugin interface.
func (p *designatePlugin) IsQuotaAcceptableForProject(client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quotas map[string]uint64) error {
	//not required for this plugin
	return nil
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *designatePlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, project core.KeystoneProject, quota map[string]uint64) error {
	client, err := openstack.NewDNSV2(provider, eo)
	if err != nil {
		return err
	}

	uint2p := func(val uint64) *int {
		ret := int(val)
		return &ret
	}

	client.MoreHeaders = map[string]string{"X-Auth-All-Projects": "true"}
	_, err = quotas.Update(client, project.UUID, quotas.UpdateOpts{
		Zones:          uint2p(quota["zones"]),
		ZoneRecordsets: uint2p(quota["recordsets"]),
		//set ZoneRecords quota to match ZoneRecordsets
		//(Designate has a records_per_recordset quota of default 20, so if we set
		//ZoneRecords to 20 * ZoneRecordsets, this quota will not disturb us)
		ZoneRecords: uint2p(quota["recordsets"] * 20),
	}).Extract()
	return err
}

//DescribeMetrics implements the core.QuotaPlugin interface.
func (p *designatePlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	//not used by this plugin
}

//CollectMetrics implements the core.QuotaPlugin interface.
func (p *designatePlugin) CollectMetrics(ch chan<- prometheus.Metric, clusterID string, project core.KeystoneProject, serializedMetrics string) error {
	//not used by this plugin
	return nil
}

////////////////////////////////////////////////////////////////////////////////
// API requests to Designate

func dnsListZoneIDs(client *gophercloud.ServiceClient, projectUUID string) ([]string, error) {
	pager := zones.List(client, zones.ListOpts{})
	client.MoreHeaders = map[string]string{
		"X-Auth-All-Projects":    "false",
		"X-Auth-Sudo-Project-Id": projectUUID,
	}

	var ids []string
	err := pager.EachPage(func(page pagination.Page) (bool, error) {
		zones, err := zones.ExtractZones(page)
		if err != nil {
			return false, err
		}
		for _, zone := range zones {
			ids = append(ids, zone.ID)
		}
		return true, nil
	})
	return ids, err
}
