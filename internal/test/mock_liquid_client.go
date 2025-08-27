// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"context"

	"github.com/sapcc/go-api-declarations/liquid"
)

// DefaultLiquidServiceInfo builds the default ServiceInfo that most mock liquids use.
// It defines two resources:
//   - "capacity" is measured in bytes, AZ-aware and reports capacity.
//   - "things" is counted, not AZ-aware and does not report capacity.
func DefaultLiquidServiceInfo() liquid.ServiceInfo {
	return liquid.ServiceInfo{
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
	}
}

// MockLiquidClient implements the LiquidClient interface
type MockLiquidClient struct {
	serviceInfo                 liquid.ServiceInfo
	serviceCapacityReport       liquid.ServiceCapacityReport
	serviceUsageReport          liquid.ServiceUsageReport
	commitmentChangeResponse    liquid.CommitmentChangeResponse
	usageReportError            error
	capacityReportError         error
	quotaError                  error
	serviceInfoError            error
	commitmentChangeError       error
	LastCommitmentChangeRequest liquid.CommitmentChangeRequest
}

// GetInfo implements the core.LiquidClient interface.
func (l *MockLiquidClient) GetInfo(ctx context.Context) (result liquid.ServiceInfo, err error) {
	if l.serviceInfoError != nil {
		return liquid.ServiceInfo{}, l.serviceInfoError
	}
	return l.serviceInfo, nil
}

func (l *MockLiquidClient) SetServiceInfoError(err error) {
	l.serviceInfoError = err
}

func (l *MockLiquidClient) SetServiceInfo(info liquid.ServiceInfo) {
	l.serviceInfo = info
}

func (l *MockLiquidClient) IncrementServiceInfoVersion() {
	l.serviceInfo.Version++
}

func (l *MockLiquidClient) SetCapacityReportError(err error) {
	l.capacityReportError = err
}

func (l *MockLiquidClient) IncrementCapacityReportInfoVersion() {
	l.serviceCapacityReport.InfoVersion++
}

func (l *MockLiquidClient) SetCapacityReport(capacityReport liquid.ServiceCapacityReport) {
	l.serviceCapacityReport = capacityReport
}

// GetCapacityReport implements the core.LiquidClient interface.
func (l *MockLiquidClient) GetCapacityReport(ctx context.Context, req liquid.ServiceCapacityRequest) (result liquid.ServiceCapacityReport, err error) {
	if l.capacityReportError != nil {
		return liquid.ServiceCapacityReport{}, l.capacityReportError
	}
	return l.serviceCapacityReport.Clone(), nil
}

func (l *MockLiquidClient) SetUsageReportError(err error) {
	l.usageReportError = err
}

func (l *MockLiquidClient) IncrementUsageReportInfoVersion() {
	l.serviceUsageReport.InfoVersion++
}

func (l *MockLiquidClient) SetUsageReport(usageReport liquid.ServiceUsageReport) {
	l.serviceUsageReport = usageReport
}

// GetUsageReport implements the core.LiquidClient interface.
func (l *MockLiquidClient) GetUsageReport(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest) (result liquid.ServiceUsageReport, err error) {
	if l.usageReportError != nil {
		return liquid.ServiceUsageReport{}, l.usageReportError
	}
	return l.serviceUsageReport.Clone(), nil
}

func (l *MockLiquidClient) SetQuotaError(err error) {
	l.quotaError = err
}

// PutQuota implements the core.LiquidClient interface.
func (l *MockLiquidClient) PutQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest) (err error) {
	return l.quotaError
}

func (l *MockLiquidClient) SetCommitmentChangeError(err error) {
	l.commitmentChangeError = err
}

func (l *MockLiquidClient) SetCommitmentChangeResponse(response liquid.CommitmentChangeResponse) {
	l.commitmentChangeResponse = response
}

// ChangeCommitments implements the core.LiquidClient interface.
func (l *MockLiquidClient) ChangeCommitments(ctx context.Context, req liquid.CommitmentChangeRequest) (result liquid.CommitmentChangeResponse, err error) {
	l.LastCommitmentChangeRequest = req
	if l.commitmentChangeError != nil {
		return liquid.CommitmentChangeResponse{}, l.commitmentChangeError
	}
	return l.commitmentChangeResponse, nil
}
