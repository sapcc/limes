// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package archer

import (
	"context"
	"errors"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/respondwith"
)

// Logic implements the liquidapi.Logic interface for Archer.
type Logic struct {
	// connections
	Archer *Client `json:"-"`
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.Archer, err = newClient(provider, eo)
	return err
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	return liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"endpoints": {
				Unit:     liquid.UnitNone,
				Topology: liquid.FlatTopology,
				HasQuota: true,
			},
			"services": {
				Unit:     liquid.UnitNone,
				Topology: liquid.FlatTopology,
				HasQuota: true,
			},
		},
	}, nil
}

// ScanCapacity implements the liquidapi.Logic interface.
func (l *Logic) ScanCapacity(ctx context.Context, req liquid.ServiceCapacityRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceCapacityReport, error) {
	// no resources report capacity
	return liquid.ServiceCapacityReport{InfoVersion: serviceInfo.Version}, nil
}

// ScanUsage implements the liquidapi.Logic interface.
func (l *Logic) ScanUsage(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest, serviceInfo liquid.ServiceInfo) (liquid.ServiceUsageReport, error) {
	qs, err := l.Archer.getQuotaSet(ctx, projectUUID)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources: map[liquid.ResourceName]*liquid.ResourceUsageReport{
			"endpoints": {
				Quota: Some(qs.Endpoint),
				PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: qs.InUseEndpoint}),
			},
			"services": {
				Quota: Some(qs.Service),
				PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: qs.InUseService}),
			},
		},
	}, nil
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	return l.Archer.putQuotaSet(ctx, projectUUID, quotaSet{
		Endpoint: int64(req.Resources["endpoints"].Quota),
		Service:  int64(req.Resources["services"].Quota),
	})
}

// ReviewCommitmentChange implements the liquidapi.Logic interface.
func (l *Logic) ReviewCommitmentChange(ctx context.Context, req liquid.CommitmentChangeRequest, serviceInfo liquid.ServiceInfo) (liquid.CommitmentChangeResponse, error) {
	err := errors.New("this liquid does not manage commitments")
	return liquid.CommitmentChangeResponse{}, respondwith.CustomStatus(http.StatusBadRequest, err)
}
