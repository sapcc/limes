/*******************************************************************************
*
* Copyright 2025 SAP SE
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

package test

import (
	"context"

	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/core"
)

// MockLiquidClient implements the LiquidClient interface
type MockLiquidClient struct {
	serviceInfo           liquid.ServiceInfo
	serviceCapacityReport liquid.ServiceCapacityReport
	serviceUsageReport    liquid.ServiceUsageReport
	usageReportError      error
	capacityReportError   error
	quotaError            error
}

func init() {
	core.NewMockLiquidClient = func() core.LiquidClient {
		return &MockLiquidClient{
			serviceInfo: liquid.ServiceInfo{
				Version: 1,
				Resources: map[liquid.ResourceName]liquid.ResourceInfo{
					"capacity": {
						Unit:                liquid.UnitBytes,
						Topology:            liquid.AZAwareTopology,
						HasCapacity:         true,
						HasQuota:            true,
						NeedsResourceDemand: true,
					},
					"things": {
						Unit:        liquid.UnitNone,
						Topology:    liquid.FlatTopology,
						HasCapacity: false,
						HasQuota:    true,
					},
				},
			},
			usageReportError:    nil,
			capacityReportError: nil,
			quotaError:          nil,
		}
	}
}

func (l *MockLiquidClient) GetInfo(ctx context.Context) (result liquid.ServiceInfo, err error) {
	return l.serviceInfo, nil
}

func (l *MockLiquidClient) SetServiceInfo(info liquid.ServiceInfo) {
	l.serviceInfo = info
}

func (l *MockLiquidClient) SetCapacityReportError(err error) {
	l.capacityReportError = err
}

func (l *MockLiquidClient) GetCapacityReport(ctx context.Context, req liquid.ServiceCapacityRequest) (result liquid.ServiceCapacityReport, err error) {
	if l.capacityReportError != nil {
		return liquid.ServiceCapacityReport{}, l.capacityReportError
	}
	return l.serviceCapacityReport, nil
}

func (l *MockLiquidClient) SetCapacityReport(capacityReport liquid.ServiceCapacityReport) {
	l.serviceCapacityReport = capacityReport
}

func (l *MockLiquidClient) SetUsageReportError(err error) {
	l.usageReportError = err
}

func (l *MockLiquidClient) GetUsageReport(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest) (result liquid.ServiceUsageReport, err error) {
	if l.usageReportError != nil {
		return liquid.ServiceUsageReport{}, l.usageReportError
	}
	return l.serviceUsageReport, nil
}

func (l *MockLiquidClient) SetUsageReport(usageReport liquid.ServiceUsageReport) {
	l.serviceUsageReport = usageReport
}

func (l *MockLiquidClient) SetQuotaError(err error) {
	l.quotaError = err
}

func (l *MockLiquidClient) PutQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest) (err error) {
	return l.quotaError
}
