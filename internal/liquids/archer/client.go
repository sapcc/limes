// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package archer

import (
	"context"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
)

// Client is a gophercloud.ServiceClient for the Archer API.
type Client struct {
	*gophercloud.ServiceClient
}

func newClient(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (*Client, error) {
	serviceType := "endpoint-services"
	eo.ApplyDefaults(serviceType)

	url, err := provider.EndpointLocator(eo)
	if err != nil {
		return nil, err
	}
	return &Client{
		ServiceClient: &gophercloud.ServiceClient{
			ProviderClient: provider,
			Endpoint:       url,
			Type:           serviceType,
		},
	}, nil
}

type quotaSet struct {
	// for both GET and PUT
	Endpoint int64 `json:"endpoint"`
	Service  int64 `json:"service"`
	// only for GET
	InUseEndpoint uint64 `json:"in_use_endpoint"`
	InUseService  uint64 `json:"in_use_service"`
}

func (c *Client) getQuotaSet(ctx context.Context, projectUUID string) (qs quotaSet, err error) {
	url := c.ServiceURL("quotas", projectUUID)
	var r gophercloud.Result
	_, r.Header, r.Err = gophercloud.ParseResponse(c.Get(ctx, url, &r.Body, nil)) //nolint:bodyclose // already closed by gophercloud
	err = r.ExtractInto(&qs)
	return
}

func (c *Client) putQuotaSet(ctx context.Context, projectUUID string, qs quotaSet) error {
	url := c.ServiceURL("quotas", projectUUID)
	opts := gophercloud.RequestOpts{OkCodes: []int{http.StatusOK}}
	_, _, err := gophercloud.ParseResponse(c.Put(ctx, url, qs, nil, &opts)) //nolint:bodyclose // already closed by gophercloud
	return err
}
