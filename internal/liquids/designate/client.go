// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package designate

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
	"github.com/gophercloud/gophercloud/v2/openstack/dns/v2/zones"
	"github.com/gophercloud/gophercloud/v2/pagination"
)

type Client struct {
	*gophercloud.ServiceClient
}

func NewClient(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (*Client, error) {
	sc, err := openstack.NewDNSV2(provider, eo)
	return &Client{ServiceClient: sc}, err
}

type QuotaSet struct {
	Zones             int64 `json:"zones"`
	RecordsetsPerZone int64 `json:"zone_recordsets"`
	RecordsPerZone    int64 `json:"zone_records"`
}

func (c *Client) GetQuota(ctx context.Context, projectUUID string) (qs QuotaSet, err error) {
	url := c.ServiceURL("quotas", projectUUID)
	opts := gophercloud.RequestOpts{
		MoreHeaders: map[string]string{"X-Auth-All-Projects": "true"},
	}

	var r gophercloud.Result
	_, r.Header, r.Err = gophercloud.ParseResponse(c.Get(ctx, url, &r.Body, &opts))
	err = r.ExtractInto(&qs)
	return
}

func (c *Client) SetQuota(ctx context.Context, projectUUID string, qs QuotaSet) error {
	url := c.ServiceURL("quotas", projectUUID)
	opts := gophercloud.RequestOpts{
		MoreHeaders: map[string]string{"X-Auth-All-Projects": "true"},
	}

	_, _, err := gophercloud.ParseResponse(c.Patch(ctx, url, qs, nil, &opts))
	return err
}

func (c *Client) ListZoneIDs(ctx context.Context, projectUUID string) ([]string, error) {
	pager := zones.List(c.ServiceClient, zones.ListOpts{})
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

func (c *Client) CountZoneRecordsets(ctx context.Context, projectUUID, zoneID string) (uint64, error) {
	url := c.ServiceURL("zones", zoneID, "recordsets")
	url += "?limit=1" // do not need all data about all recordsets, just the total count
	opts := gophercloud.RequestOpts{
		MoreHeaders: map[string]string{
			"X-Auth-All-Projects":    "false",
			"X-Auth-Sudo-Project-Id": projectUUID,
		},
	}

	var r gophercloud.Result
	_, r.Header, r.Err = gophercloud.ParseResponse(c.Get(ctx, url, &r.Body, &opts))

	var data struct {
		Metadata struct {
			Count uint64 `json:"total_count"`
		} `json:"metadata"`
	}
	err := r.ExtractInto(&data)
	return data.Metadata.Count, err
}
