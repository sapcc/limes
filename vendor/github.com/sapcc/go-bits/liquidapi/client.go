// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package liquidapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/sapcc/go-api-declarations/liquid"
)

// Client provides structured access to a LIQUID API.
type Client struct {
	gophercloud.ServiceClient
}

// ClientOpts contains additional options for NewClient().
type ClientOpts struct {
	// The service type of the liquid in the Keystone catalog.
	// Required if EndpointOverride is not given.
	ServiceType string

	// Skips inspecting the Keystone catalog and assumes that the liquid's API is
	// located at this base URL. Required if ServiceType is not given.
	EndpointOverride string
}

// NewClient creates a Client for interacting with a liquid.
func NewClient(client *gophercloud.ProviderClient, endpointOpts gophercloud.EndpointOpts, opts ClientOpts) (*Client, error) {
	if opts.ServiceType == "" && opts.EndpointOverride == "" {
		return nil, errors.New("either ServiceType or EndpointOverride needs to be given in liquidapi.NewClient()")
	}

	endpoint := opts.EndpointOverride
	if endpoint == "" {
		endpointOpts.ApplyDefaults(opts.ServiceType)
		var err error
		endpoint, err = client.EndpointLocator(endpointOpts)
		if err != nil {
			return nil, err
		}
	}

	if opts.ServiceType == "" {
		opts.ServiceType = "liquid"
	}
	return &Client{
		ServiceClient: gophercloud.ServiceClient{
			ProviderClient: client,
			Endpoint:       endpoint,
			Type:           opts.ServiceType,
		},
	}, nil
}

// GetInfo executes GET /v1/info.
func (c *Client) GetInfo(ctx context.Context) (result liquid.ServiceInfo, err error) {
	url := c.ServiceURL("v1", "info")
	opts := gophercloud.RequestOpts{KeepResponseBody: true}
	resp, err := c.Get(ctx, url, nil, &opts)
	if err == nil {
		err = parseLiquidResponse(resp, &result)
	}
	return
}

// GetCapacityReport executes POST /v1/report-capacity.
func (c *Client) GetCapacityReport(ctx context.Context, req liquid.ServiceCapacityRequest) (result liquid.ServiceCapacityReport, err error) {
	url := c.ServiceURL("v1", "report-capacity")
	opts := gophercloud.RequestOpts{KeepResponseBody: true, OkCodes: []int{http.StatusOK}}
	resp, err := c.Post(ctx, url, req, nil, &opts)
	if err == nil {
		err = parseLiquidResponse(resp, &result)
	}
	return
}

// GetUsageReport executes POST /v1/projects/:uuid/report-usage.
func (c *Client) GetUsageReport(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest) (result liquid.ServiceUsageReport, err error) {
	url := c.ServiceURL("v1", "projects", projectUUID, "report-usage")
	opts := gophercloud.RequestOpts{KeepResponseBody: true, OkCodes: []int{http.StatusOK}}
	resp, err := c.Post(ctx, url, req, nil, &opts)
	if err == nil {
		err = parseLiquidResponse(resp, &result)
	}
	return
}

// PutQuota executes PUT /v1/projects/:uuid/quota.
func (c *Client) PutQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest) (err error) {
	url := c.ServiceURL("v1", "projects", projectUUID, "quota")
	opts := gophercloud.RequestOpts{KeepResponseBody: true, OkCodes: []int{http.StatusNoContent}}
	_, err = c.Put(ctx, url, req, nil, &opts) //nolint:bodyclose // either the response is 204 and does not have a body, or it's an error and Gophercloud does a ReadAll() internally
	return
}

// We do not use the standard response body parsing from Gophercloud
// because we want to be strict and DisallowUnknownFields().
func parseLiquidResponse(resp *http.Response, result any) error {
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	dec.DisallowUnknownFields()
	err := dec.Decode(&result)
	if err != nil {
		return fmt.Errorf("could not parse response body from %s %s: %w",
			resp.Request.Method, resp.Request.URL.String(), err)
	}
	return nil
}
