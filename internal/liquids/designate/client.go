// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package designate

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack"
)

// Client is a gophercloud.ServiceClient for the Designate API.
type Client struct {
	*gophercloud.ServiceClient
}

func newClient(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (*Client, error) {
	sc, err := openstack.NewDNSV2(provider, eo)
	return &Client{ServiceClient: sc}, err
}

type quotaSet struct {
	Zones             int64 `json:"zones"`
	RecordsetsPerZone int64 `json:"zone_recordsets"`
	RecordsPerZone    int64 `json:"zone_records"`
}

func (c *Client) getQuota(ctx context.Context, projectUUID string) (qs quotaSet, err error) {
	url := c.ServiceURL("quotas", projectUUID)
	opts := gophercloud.RequestOpts{
		MoreHeaders: map[string]string{"X-Auth-All-Projects": "true"},
	}

	var r gophercloud.Result
	_, r.Header, r.Err = gophercloud.ParseResponse(c.Get(ctx, url, &r.Body, &opts)) //nolint:bodyclose
	err = r.ExtractInto(&qs)
	return
}

func (c *Client) setQuota(ctx context.Context, projectUUID string, qs quotaSet) error {
	url := c.ServiceURL("quotas", projectUUID)
	opts := gophercloud.RequestOpts{
		MoreHeaders: map[string]string{"X-Auth-All-Projects": "true"},
	}

	_, _, err := gophercloud.ParseResponse(c.Patch(ctx, url, qs, nil, &opts)) //nolint:bodyclose
	return err
}
