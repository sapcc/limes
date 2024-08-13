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

package archer

import (
	"context"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
)

type Logic struct {
	// connections
	Archer *Client `yaml:"-"`
}

// Init implements the liquidapi.Logic interface.
func (l *Logic) Init(ctx context.Context, provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.Archer, err = NewClient(provider, eo)
	return err
}

// BuildServiceInfo implements the liquidapi.Logic interface.
func (l *Logic) BuildServiceInfo(ctx context.Context) (liquid.ServiceInfo, error) {
	return liquid.ServiceInfo{
		Version: 1,
		Resources: map[liquid.ResourceName]liquid.ResourceInfo{
			"endpoints": {
				Unit:     limes.UnitNone,
				HasQuota: true,
			},
			"services": {
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
	quotaSet, err := l.Archer.GetQuotaSet(ctx, projectUUID)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	return liquid.ServiceUsageReport{
		InfoVersion: serviceInfo.Version,
		Resources: map[liquid.ResourceName]*liquid.ResourceUsageReport{
			"endpoints": {
				Quota: &quotaSet.Endpoint,
				PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: quotaSet.InUseEndpoint}),
			},
			"services": {
				Quota: &quotaSet.Service,
				PerAZ: liquid.InAnyAZ(liquid.AZResourceUsageReport{Usage: quotaSet.InUseService}),
			},
		},
	}, nil
}

// SetQuota implements the liquidapi.Logic interface.
func (l *Logic) SetQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest, serviceInfo liquid.ServiceInfo) error {
	return l.Archer.PutQuotaSet(ctx, projectUUID, QuotaSet{
		Endpoint: int64(req.Resources["endpoints"].Quota),
		Service:  int64(req.Resources["services"].Quota),
	})
}
