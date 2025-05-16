// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package octavia

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/gophercloud/gophercloud/v2/openstack/loadbalancer/v2/quotas"
)

func getQuota(ctx context.Context, client *gophercloud.ServiceClient, projectUUID string) (map[string]int64, error) {
	var data struct {
		Quota map[string]int64 `json:"quota"`
	}
	err := quotas.Get(ctx, client, projectUUID).ExtractInto(&data)
	return data.Quota, err
}

func getUsage(ctx context.Context, client *gophercloud.ServiceClient, projectUUID string) (map[string]uint64, error) {
	// NOTE: This API endpoint is a custom extension in SAP Converged Cloud.
	var r gophercloud.Result
	url := client.ServiceURL("quota_usage", projectUUID)
	_, r.Header, r.Err = gophercloud.ParseResponse(client.Get(ctx, url, &r.Body, nil))

	var data struct {
		Usage map[string]uint64 `json:"quota_usage"`
	}
	err := r.ExtractInto(&data)
	return data.Usage, err
}

type quotaSet map[string]uint64

// ToQuotaUpdateMap implements the quotas.UpdateOpts interfaces.
func (q quotaSet) ToQuotaUpdateMap() (map[string]any, error) {
	return map[string]any{"quota": map[string]uint64(q)}, nil
}
