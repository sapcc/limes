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

// MockLiquidClient implements the LiquidClient interface.
type MockLiquidClient struct {
	ServiceInfo                 MockLiquidSlot[liquid.ServiceInfo]
	CapacityReport              MockLiquidSlot[liquid.ServiceCapacityReport]
	UsageReport                 MockLiquidSlot[liquid.ServiceUsageReport]
	CommitmentChangeResponse    MockLiquidSlot[liquid.CommitmentChangeResponse]
	quotaError                  error
	LastCommitmentChangeRequest liquid.CommitmentChangeRequest
}

// GetInfo implements the core.LiquidClient interface.
func (l *MockLiquidClient) GetInfo(ctx context.Context) (result liquid.ServiceInfo, err error) {
	return l.ServiceInfo.get()
}

// IncrementServiceInfoVersion increments the ServiceInfo version number by 1.
func (l *MockLiquidClient) IncrementServiceInfoVersion() {
	l.ServiceInfo.Modify(func(info *liquid.ServiceInfo) { info.Version++ })
}

// GetCapacityReport implements the core.LiquidClient interface.
func (l *MockLiquidClient) GetCapacityReport(ctx context.Context, req liquid.ServiceCapacityRequest) (result liquid.ServiceCapacityReport, err error) {
	return l.CapacityReport.get()
}

// IncrementCapacityReportInfoVersion increments the CapacityReport InfoVersion by 1.
func (l *MockLiquidClient) IncrementCapacityReportInfoVersion() {
	l.CapacityReport.Modify(func(report *liquid.ServiceCapacityReport) { report.InfoVersion++ })
}

// GetUsageReport implements the core.LiquidClient interface.
func (l *MockLiquidClient) GetUsageReport(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest) (result liquid.ServiceUsageReport, err error) {
	return l.UsageReport.get()
}

// IncrementUsageReportInfoVersion increments the UsageReport InfoVersion by 1.
func (l *MockLiquidClient) IncrementUsageReportInfoVersion() {
	l.UsageReport.Modify(func(report *liquid.ServiceUsageReport) { report.InfoVersion++ })
}

// SetQuotaError sets an error to be returned by PutQuota, or clears it if nil is given.
func (l *MockLiquidClient) SetQuotaError(err error) {
	l.quotaError = err
}

// PutQuota implements the core.LiquidClient interface.
func (l *MockLiquidClient) PutQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest) (err error) {
	return l.quotaError
}

// ChangeCommitments implements the core.LiquidClient interface.
func (l *MockLiquidClient) ChangeCommitments(ctx context.Context, req liquid.CommitmentChangeRequest) (result liquid.CommitmentChangeResponse, err error) {
	l.LastCommitmentChangeRequest = req
	return l.CommitmentChangeResponse.get()
}

type cloneableLiquidData[Self any] interface {
	Clone() Self
}

// MockLiquidSlot contains a prepared response (and/or error) to a LIQUID function call.
type MockLiquidSlot[T cloneableLiquidData[T]] struct {
	data T
	err  error
}

func (s MockLiquidSlot[T]) get() (T, error) {
	if s.err != nil {
		var zero T
		return zero, s.err
	}
	// This Clone() ensures that the receiver of the data does not mess with
	// data structures that are also held by us (thus messing up the next get() call).
	return s.data.Clone(), nil
}

// Modify changes the held data in-place by executing the provided callback.
// The data instance will be cloned after the callback to ensure that the test
// function cannot smuggle live references to the data.
func (s *MockLiquidSlot[T]) Modify(action func(*T)) {
	data := s.data
	action(&data)
	s.data = data.Clone()
}

// Set replaces the held data.
func (s *MockLiquidSlot[T]) Set(data T) {
	s.data = data.Clone()
}

// SetError sets an error to return instead of the data, or clears it if nil is given.
func (s *MockLiquidSlot[T]) SetError(err error) {
	s.err = err
}
