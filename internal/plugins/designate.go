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
	"math/big"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/zones"
	"github.com/gophercloud/gophercloud/v2/pagination"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

type designatePlugin struct {
	// connections
	DesignateV2 *gophercloud.ServiceClient `yaml:"-"`
}

var designateResources = map[liquid.ResourceName]liquid.ResourceInfo{
	"zones": {
		Unit:     limes.UnitNone,
		HasQuota: true,
	},
	"recordsets": {
		// this quota means "recordsets per zone", not "recordsets per project"!
		Unit:     limes.UnitNone,
		HasQuota: true,
	},
}

func init() {
	core.QuotaPluginRegistry.Add(func() core.QuotaPlugin { return &designatePlugin{} })
}

// Init implements the core.QuotaPlugin interface.
func (p *designatePlugin) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, serviceType db.ServiceType) (err error) {
	p.DesignateV2, err = openstack.NewDNSV2(provider, eo)
	return err
}

// PluginTypeID implements the core.QuotaPlugin interface.
func (p *designatePlugin) PluginTypeID() string {
	return "dns"
}

// ServiceInfo implements the core.QuotaPlugin interface.
func (p *designatePlugin) ServiceInfo() core.ServiceInfo {
	return core.ServiceInfo{
		ProductName: "designate",
		Area:        "dns",
	}
}

// Resources implements the core.QuotaPlugin interface.
func (p *designatePlugin) Resources() map[liquid.ResourceName]liquid.ResourceInfo {
	return designateResources
}

// Rates implements the core.QuotaPlugin interface.
func (p *designatePlugin) Rates() map[db.RateName]core.RateInfo {
	return nil
}

// ScrapeRates implements the core.QuotaPlugin interface.
func (p *designatePlugin) ScrapeRates(ctx context.Context, project core.KeystoneProject, prevSerializedState string) (result map[db.RateName]*big.Int, serializedState string, err error) {
	return nil, "", nil
}

// Scrape implements the core.QuotaPlugin interface.
func (p *designatePlugin) Scrape(ctx context.Context, project core.KeystoneProject, allAZs []limes.AvailabilityZone) (result map[liquid.ResourceName]core.ResourceData, serializedMetrics []byte, err error) {
	// query quotas
	quotas, err := dnsGetQuota(ctx, p.DesignateV2, project.UUID)
	if err != nil {
		return nil, nil, err
	}

	// to query usage, start by listing all zones
	zoneIDs, err := dnsListZoneIDs(ctx, p.DesignateV2, project.UUID)
	if err != nil {
		return nil, nil, err
	}

	// query "recordsets per zone" usage by counting recordsets in each zone
	// individually (we could count all recordsets over the all project at once,
	// but that won't help since the quota applies per individual zone)
	maxRecordsetsPerZone := uint64(0)
	for _, zoneID := range zoneIDs {
		count, err := dnsCountZoneRecordsets(ctx, p.DesignateV2, project.UUID, zoneID)
		if err != nil {
			return nil, nil, err
		}
		if maxRecordsetsPerZone < count {
			maxRecordsetsPerZone = count
		}
	}

	return map[liquid.ResourceName]core.ResourceData{
		"zones": {
			Quota: quotas.Zones,
			UsageData: core.InAnyAZ(core.UsageData{
				Usage: uint64(len(zoneIDs)),
			}),
		},
		"recordsets": {
			Quota: quotas.ZoneRecordsets,
			UsageData: core.InAnyAZ(core.UsageData{
				Usage: maxRecordsetsPerZone,
			}),
		},
	}, nil, nil
}

// SetQuota implements the core.QuotaPlugin interface.
func (p *designatePlugin) SetQuota(ctx context.Context, project core.KeystoneProject, quotas map[liquid.ResourceName]uint64) error {
	return dnsSetQuota(ctx, p.DesignateV2, project.UUID, &dnsQuota{
		Zones:          int64(quotas["zones"]),      //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63
		ZoneRecordsets: int64(quotas["recordsets"]), //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63
		// set ZoneRecords quota to match ZoneRecordsets
		// (Designate has a records_per_recordset quota of default 20, so if we set
		// ZoneRecords to 20 * ZoneRecordsets, this quota will not disturb us)
		ZoneRecords: int64(quotas["recordsets"] * 20), //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63 / 20
	})
}

// DescribeMetrics implements the core.QuotaPlugin interface.
func (p *designatePlugin) DescribeMetrics(ch chan<- *prometheus.Desc) {
	// not used by this plugin
}

// CollectMetrics implements the core.QuotaPlugin interface.
func (p *designatePlugin) CollectMetrics(ch chan<- prometheus.Metric, project core.KeystoneProject, serializedMetrics []byte) error {
	// not used by this plugin
	return nil
}

////////////////////////////////////////////////////////////////////////////////
// API requests to Designate

type dnsQuota struct {
	Zones          int64 `json:"zones"`
	ZoneRecordsets int64 `json:"zone_recordsets"`
	ZoneRecords    int64 `json:"zone_records"`
}

func dnsGetQuota(ctx context.Context, client *gophercloud.ServiceClient, projectUUID string) (*dnsQuota, error) {
	url := client.ServiceURL("quotas", projectUUID)
	opts := gophercloud.RequestOpts{
		MoreHeaders: map[string]string{"X-Auth-All-Projects": "true"},
	}

	var result gophercloud.Result
	var data dnsQuota
	_, result.Err = client.Get(ctx, url, &result.Body, &opts) //nolint:bodyclose // already closed by gophercloud
	err := result.ExtractInto(&data)
	return &data, err
}

func dnsSetQuota(ctx context.Context, client *gophercloud.ServiceClient, projectUUID string, quota *dnsQuota) error {
	url := client.ServiceURL("quotas", projectUUID)
	opts := gophercloud.RequestOpts{
		MoreHeaders: map[string]string{"X-Auth-All-Projects": "true"},
	}

	_, err := client.Patch(ctx, url, quota, nil, &opts) //nolint:bodyclose // already closed by gophercloud
	return err
}

func dnsListZoneIDs(ctx context.Context, client *gophercloud.ServiceClient, projectUUID string) ([]string, error) {
	pager := zones.List(client, zones.ListOpts{})
	pager.Headers = map[string]string{
		"X-Auth-All-Projects":    "false",
		"X-Auth-Sudo-Project-Id": projectUUID,
	}

	var ids []string
	err := pager.EachPage(ctx, func(ctx context.Context, page pagination.Page) (bool, error) {
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

func dnsCountZoneRecordsets(ctx context.Context, client *gophercloud.ServiceClient, projectUUID, zoneID string) (uint64, error) {
	url := client.ServiceURL("zones", zoneID, "recordsets")
	opts := gophercloud.RequestOpts{
		MoreHeaders: map[string]string{
			"X-Auth-All-Projects":    "false",
			"X-Auth-Sudo-Project-Id": projectUUID,
		},
	}

	// do not need all data about all recordsets, just the total count
	url += "?limit=1"

	var result gophercloud.Result
	var data struct {
		Metadata struct {
			Count uint64 `json:"total_count"`
		} `json:"metadata"`
	}
	_, result.Err = client.Get(ctx, url, &result.Body, &opts) //nolint:bodyclose // already closed by gophercloud
	err := result.ExtractInto(&data)
	return data.Metadata.Count, err
}
