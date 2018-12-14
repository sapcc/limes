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
	"github.com/gophercloud/gophercloud/openstack/dns/v2/zones"
	"github.com/gophercloud/gophercloud/pagination"
	"github.com/sapcc/limes/pkg/core"
)

type designatePlugin struct {
	cfg core.ServiceConfiguration
}

var designateResources = []core.ResourceInfo{
	{
		Name: "zones",
		Unit: core.UnitNone,
	},
	{
		//this quota means "recordsets per zone", not "recordsets per project"!
		Name: "recordsets",
		Unit: core.UnitNone,
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
func (p *designatePlugin) ServiceInfo() core.ServiceInfo {
	return core.ServiceInfo{
		Type:        "dns",
		ProductName: "designate",
		Area:        "dns",
	}
}

//Resources implements the core.QuotaPlugin interface.
func (p *designatePlugin) Resources() []core.ResourceInfo {
	return designateResources
}

//Scrape implements the core.QuotaPlugin interface.
func (p *designatePlugin) Scrape(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string) (map[string]core.ResourceData, error) {
	client, err := openstack.NewDNSV2(provider, eo)
	if err != nil {
		return nil, err
	}

	//query quotas
	quotas, err := dnsGetQuota(client, projectUUID)
	if err != nil {
		return nil, err
	}

	//to query usage, start by listing all zones
	zoneIDs, err := dnsListZoneIDs(client, projectUUID)
	if err != nil {
		return nil, err
	}

	//query "recordsets per zone" usage by counting recordsets in each zone
	//individually (we could count all recordsets over the all project at once,
	//but that won't help since the quota applies per individual zone)
	//TODO: this needs a lot of API requests for large projects; see if we can
	//use Ceilometer instead
	maxRecordsetsPerZone := uint64(0)
	for _, zoneID := range zoneIDs {
		count, err := dnsCountZoneRecordsets(client, projectUUID, zoneID)
		if err != nil {
			return nil, err
		}
		if maxRecordsetsPerZone < count {
			maxRecordsetsPerZone = count
		}
	}

	return map[string]core.ResourceData{
		"zones": {
			Quota: quotas.Zones,
			Usage: uint64(len(zoneIDs)),
		},
		"recordsets": {
			Quota: quotas.ZoneRecordsets,
			Usage: maxRecordsetsPerZone,
		},
	}, nil
}

//SetQuota implements the core.QuotaPlugin interface.
func (p *designatePlugin) SetQuota(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, clusterID, domainUUID, projectUUID string, quotas map[string]uint64) error {
	client, err := openstack.NewDNSV2(provider, eo)
	if err != nil {
		return err
	}

	return dnsSetQuota(client, projectUUID, &dnsQuota{
		Zones:          int64(quotas["zones"]),
		ZoneRecordsets: int64(quotas["recordsets"]),
		//set ZoneRecords quota to match ZoneRecordsets
		//(Designate has a records_per_recordset quota of default 20, so if we set
		//ZoneRecords to 20 * ZoneRecordsets, this quota will not disturb us)
		ZoneRecords: int64(quotas["recordsets"] * 20),
	})
}

////////////////////////////////////////////////////////////////////////////////
// API requests to Designate

type dnsQuota struct {
	Zones          int64 `json:"zones"`
	ZoneRecordsets int64 `json:"zone_recordsets"`
	ZoneRecords    int64 `json:"zone_records"`
}

func dnsGetQuota(client *gophercloud.ServiceClient, projectUUID string) (*dnsQuota, error) {
	url := client.ServiceURL("quotas", projectUUID)
	opts := gophercloud.RequestOpts{
		MoreHeaders: map[string]string{"X-Auth-All-Projects": "true"},
	}

	var result gophercloud.Result
	var data dnsQuota
	_, result.Err = client.Get(url, &result.Body, &opts)
	err := result.ExtractInto(&data)
	return &data, err
}

func dnsSetQuota(client *gophercloud.ServiceClient, projectUUID string, quota *dnsQuota) error {
	url := client.ServiceURL("quotas", projectUUID)
	opts := gophercloud.RequestOpts{
		MoreHeaders: map[string]string{"X-Auth-All-Projects": "true"},
	}

	_, err := client.Patch(url, quota, nil, &opts)
	return err
}

func dnsListZoneIDs(client *gophercloud.ServiceClient, projectUUID string) ([]string, error) {
	pager := zones.List(client, zones.ListOpts{})
	pager.Headers = map[string]string{
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

func dnsCountZoneRecordsets(client *gophercloud.ServiceClient, projectUUID, zoneID string) (uint64, error) {
	url := client.ServiceURL("zones", zoneID, "recordsets")
	opts := gophercloud.RequestOpts{
		MoreHeaders: map[string]string{
			"X-Auth-All-Projects":    "false",
			"X-Auth-Sudo-Project-Id": projectUUID,
		},
	}

	//do not need all data about all recordsets, just the total count
	url += "?limit=1"

	var result gophercloud.Result
	var data struct {
		Metadata struct {
			Count uint64 `json:"total_count"`
		} `json:"metadata"`
	}
	_, result.Err = client.Get(url, &result.Body, &opts)
	err := result.ExtractInto(&data)
	return data.Metadata.Count, err
}
