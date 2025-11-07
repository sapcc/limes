// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package designate

import (
	"context"
	"errors"
	"net/http"

	"github.com/gophercloud/gophercloud/v2"
	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/respondwith"
)

// Logic implements the liquidapi.Logic interface for Designate.
type Logic struct {
	// connections
	DesignateV2 *Client `json:"-"`
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.DesignateV2, err = newClient(provider, eo)
	return err
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	return liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"zones": {
				Unit:     liquid.UnitNone,
				Topology: liquid.FlatTopology,
				HasQuota: true,
			},
			"recordsets_per_zone": {
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
	// query quotas
	quotas, err := l.DesignateV2.getQuota(ctx, projectUUID)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	// to query usage, start by listing all zones
	zoneIDs, err := l.DesignateV2.listZoneIDs(ctx, projectUUID)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	// query "recordsets per zone" usage by counting recordsets in each zone
	// individually (we could count all recordsets over the all project at once,
	// but that won't help since the quota applies per individual zone)
	maxRecordsetsPerZone := uint64(0)
	for _, zoneID := range zoneIDs {
		count, err := l.DesignateV2.countZoneRecordsets(ctx, projectUUID, zoneID)
		if err != nil {
			return liquid.ServiceUsageReport{}, err
		}
		if maxRecordsetsPerZone < count {
			maxRecordsetsPerZone = count
		}
	}

	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources: map[liquid.ResourceName]*liquid.ResourceUsageReport{
			"zones": {
				Quota: Some(quotas.Zones),
				PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{
					Usage: uint64(len(zoneIDs)),
				}),
			},
			"recordsets_per_zone": {
				Quota: Some(quotas.RecordsetsPerZone),
				PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{
					Usage: maxRecordsetsPerZone,
				}),
			},
		},
	}, nil
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	return l.DesignateV2.setQuota(ctx, projectUUID, quotaSet{
		Zones:             int64(req.Resources["zones"].Quota),               //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63
		RecordsetsPerZone: int64(req.Resources["recordsets_per_zone"].Quota), //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63

		// Designate has a records_per_recordset quota of default 20, so if we set
		// ZoneRecords to 20 * ZoneRecordsets, this quota will not disturb us
		RecordsPerZone: int64(req.Resources["recordsets_per_zone"].Quota * 20), //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63 / 20
	})
}

// ReviewCommitmentChange implements the liquidapi.Logic interface.
func (l *Logic) ReviewCommitmentChange(ctx context.Context, req liquid.CommitmentChangeRequest, serviceInfo liquid.ServiceInfo) (liquid.CommitmentChangeResponse, error) {
	err := errors.New("this liquid does not manage commitments")
	return liquid.CommitmentChangeResponse{}, respondwith.CustomStatus(http.StatusBadRequest, err)
}
