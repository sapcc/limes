// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package test

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/gofrs/uuid/v5"
	"github.com/gophercloud/gophercloud/v2"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/core"
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
	serviceInfo           liquid.ServiceInfo
	serviceCapacityReport liquid.ServiceCapacityReport
	serviceUsageReport    liquid.ServiceUsageReport
	usageReportError      error
	capacityReportError   error
	quotaError            error
}

var mockLiquidClients = make(map[string]core.LiquidClient)

// NewMockLiquidClient creates a new MockLiquidClient instance.
//
// As a caller, you receive the actual MockLiquidClient instance that you can
// manipulate throughout the tests to setup the specific scenarios that you
// want to test.
//
// Additionally, the client is put into an internal registry under the returned
// service type string. This value shall be put into the cluster configuration
// to allow the core.Cluster object to find your mock client.
func NewMockLiquidClient(serviceInfo liquid.ServiceInfo) (client *MockLiquidClient, liquidServiceType string) {
	// We use a randomly-generated service type here, in order to allow for
	// multiple tests to proceed in parallel without interfering with each other
	// (once we deem this actually safe to do).
	liquidServiceType = must.Return(uuid.NewV4()).String()

	client = &MockLiquidClient{serviceInfo: serviceInfo}
	mockLiquidClients[liquidServiceType] = client
	return
}

func init() {
	core.NewLiquidClient = func(_ *gophercloud.ProviderClient, _ gophercloud.EndpointOpts, opts liquidapi.ClientOpts) (core.LiquidClient, error) {
		client, ok := mockLiquidClients[opts.ServiceType]
		if !ok {
			return nil, fmt.Errorf("no MockLiquidClient registered for service type %q", opts.ServiceType)
		}
		return client, nil
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
	return cloneServiceCapacityReport(l.serviceCapacityReport), nil
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
	return cloneServiceUsageReport(l.serviceUsageReport), nil
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

func cloneServiceUsageReport(report liquid.ServiceUsageReport) liquid.ServiceUsageReport {
	result := report
	resources := maps.Clone(report.Resources)
	for resName, resReport := range report.Resources {
		resReportClone := liquid.ResourceUsageReport{Forbidden: resReport.Forbidden, Quota: resReport.Quota}
		resReportClone.PerAZ = maps.Clone(resReport.PerAZ)
		for az, azReport := range resReport.PerAZ {
			azReportClone := liquid.AZResourceUsageReport{Usage: azReport.Usage, PhysicalUsage: azReport.PhysicalUsage, Quota: azReport.Quota}
			azReportClone.Subresources = slices.Clone(azReport.Subresources)
			for i, subres := range azReport.Subresources {
				subres.Attributes = slices.Clone(subres.Attributes)
				azReport.Subresources[i] = subres
			}
			resReportClone.PerAZ[az] = &azReportClone
		}
		resources[resName] = &resReportClone
	}
	result.Resources = resources

	rates := maps.Clone(report.Rates)
	for rateName, rateReport := range rates {
		rateReportClone := liquid.RateUsageReport{}
		rateReportClone.PerAZ = maps.Clone(rateReport.PerAZ)
		rates[rateName] = &rateReportClone
	}
	result.Rates = rates

	result.Metrics = cloneMetrics(report.Metrics)

	result.SerializedState = slices.Clone(report.SerializedState)
	return result
}

func cloneServiceCapacityReport(report liquid.ServiceCapacityReport) liquid.ServiceCapacityReport {
	result := report

	resources := maps.Clone(report.Resources)
	for resName, resReport := range report.Resources {
		resReportClone := liquid.ResourceCapacityReport{}
		resReportClone.PerAZ = maps.Clone(resReport.PerAZ)
		for az, azReport := range resReport.PerAZ {
			azReportClone := liquid.AZResourceCapacityReport{Capacity: azReport.Capacity, Usage: azReport.Usage}
			azReportClone.Subcapacities = slices.Clone(azReport.Subcapacities)
			for i, subcap := range azReport.Subcapacities {
				subcap.Attributes = slices.Clone(subcap.Attributes)
				azReport.Subcapacities[i] = subcap
			}
			resReportClone.PerAZ[az] = &azReportClone
		}
		resources[resName] = &resReportClone
	}
	result.Resources = resources

	result.Metrics = cloneMetrics(report.Metrics)
	return result
}

func cloneMetrics(metrics map[liquid.MetricName][]liquid.Metric) map[liquid.MetricName][]liquid.Metric {
	metricsClone := maps.Clone(metrics)
	for familyName, familyMetrics := range metricsClone {
		familyMetrics = slices.Clone(familyMetrics)
		for i, familyMetric := range familyMetrics {
			familyMetric.LabelValues = slices.Clone(familyMetric.LabelValues)
			familyMetrics[i] = familyMetric
		}
		metricsClone[familyName] = familyMetrics
	}
	return metricsClone
}
