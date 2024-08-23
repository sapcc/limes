/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package designate

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
)

type Logic struct {
	// connections
	DesignateV2 *Client `yaml:"-"`
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.DesignateV2, err = NewClient(provider, eo)
	return err
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	return liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"zones": {
				Unit:     limes.UnitNone,
				HasQuota: true,
			},
			"recordsets_per_zone": {
				Unit:     limes.UnitNone,
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
	quotas, err := l.DesignateV2.GetQuota(ctx, projectUUID)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	// to query usage, start by listing all zones
	zoneIDs, err := l.DesignateV2.ListZoneIDs(ctx, projectUUID)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	// query "recordsets per zone" usage by counting recordsets in each zone
	// individually (we could count all recordsets over the all project at once,
	// but that won't help since the quota applies per individual zone)
	maxRecordsetsPerZone := uint64(0)
	for _, zoneID := range zoneIDs {
		count, err := l.DesignateV2.CountZoneRecordsets(ctx, projectUUID, zoneID)
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
				Quota: &quotas.Zones,
				PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{
					Usage: uint64(len(zoneIDs)),
				}),
			},
			"recordsets_per_zone": {
				Quota: &quotas.RecordsetsPerZone,
				PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{
					Usage: maxRecordsetsPerZone,
				}),
			},
		},
	}, nil
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	return l.DesignateV2.SetQuota(ctx, projectUUID, QuotaSet{
		Zones:             int64(req.Resources["zones"].Quota),               //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63
		RecordsetsPerZone: int64(req.Resources["recordsets_per_zone"].Quota), //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63

		// Designate has a records_per_recordset quota of default 20, so if we set
		// ZoneRecords to 20 * ZoneRecordsets, this quota will not disturb us
		RecordsPerZone: int64(req.Resources["recordsets_per_zone"].Quota * 20), //nolint:gosec // uint64 -> int64 would only fail if quota is bigger than 2^63 / 20
	})
}
