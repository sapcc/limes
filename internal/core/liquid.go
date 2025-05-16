// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"

	"github.com/gophercloud/gophercloud/v2"
	. "github.com/majewsky/gg/option"
	"github.com/prometheus/common/model"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/promquery"

	"github.com/sapcc/limes/internal/db"
)

// LiquidConnection holds all the necessary information which is necessary to interact with the LiquidClient.
// In future, the state information of the LiquidConnection should be persisted in the database to avoid
// reloading in case of configuration changes.
type LiquidConnection struct {
	// configuration
	LiquidServiceType               string
	ServiceType                     db.ServiceType
	FixedCapacityConfiguration      Option[map[liquid.ResourceName]uint64]
	PrometheusCapacityConfiguration Option[PrometheusCapacityConfiguration]

	// state
	LiquidServiceInfo liquid.ServiceInfo
	LiquidClient      LiquidClient
}

// MakeLiquidConnection is a factory to fill all necessary configuration fields
func MakeLiquidConnection(lc LiquidConfiguration, serviceType db.ServiceType) LiquidConnection {
	if lc.LiquidServiceType == "" {
		lc.LiquidServiceType = "liquid-" + string(serviceType)
	}
	return LiquidConnection{
		LiquidServiceType:               lc.LiquidServiceType,
		ServiceType:                     serviceType,
		FixedCapacityConfiguration:      lc.FixedCapacityConfiguration,
		PrometheusCapacityConfiguration: lc.PrometheusCapacityConfiguration,
	}
}

// Init is called before any other interface methods, and allows the LiquidConnection to
// perform first-time initialization.
func (l *LiquidConnection) Init(ctx context.Context, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.LiquidClient, err = NewLiquidClient(client, eo, liquidapi.ClientOpts{ServiceType: l.LiquidServiceType})
	if err != nil {
		return err
	}

	l.LiquidServiceInfo, err = l.LiquidClient.GetInfo(ctx)
	if err != nil {
		return err
	}
	err = liquid.ValidateServiceInfo(l.LiquidServiceInfo)
	return err
}

// ServiceInfo returns metadata for this liquid.
// This includes metadata for all the resources and rates that this liquid scrapes.
func (l *LiquidConnection) ServiceInfo() liquid.ServiceInfo {
	return l.LiquidServiceInfo
}

// Scrape queries the backend service for the quota and usage data of all
// the resources for the given project in the given domain.
//
// The `allAZs` list comes from the Limes config and should be used when
// building AZ-aware usage data, to ensure that each AZ-aware resource reports
// usage in all available AZs, even when the project in question does not have
// usage in every AZ.
func (l *LiquidConnection) Scrape(ctx context.Context, project KeystoneProject, allAZs []limes.AvailabilityZone) (result liquid.ServiceUsageReport, err error) {
	// shortcut for liquids that only have rates and no resources
	if len(l.LiquidServiceInfo.Resources) == 0 && len(l.LiquidServiceInfo.UsageMetricFamilies) == 0 {
		return liquid.ServiceUsageReport{}, nil
	}

	req, err := l.BuildServiceUsageRequest(project, allAZs)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	result, err = l.LiquidClient.GetUsageReport(ctx, project.UUID, req)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}
	if result.InfoVersion != l.LiquidServiceInfo.Version {
		logg.Fatal("ServiceInfo version for %s changed from %d to %d; restarting now to reload ServiceInfo...",
			l.LiquidServiceType, l.LiquidServiceInfo.Version, result.InfoVersion)
	}
	err = liquid.ValidateServiceInfo(l.LiquidServiceInfo)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}
	err = liquid.ValidateUsageReport(result, req, l.LiquidServiceInfo)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	return result, nil
}

// ScrapeCapacity queries the backend service(s) for the capacities of the resources
// that this LiquidConnection is concerned with. The result is a two-dimensional map,
// with the first key being the service type, and the second key being the
// resource name.
func (l *LiquidConnection) ScrapeCapacity(ctx context.Context, backchannel CapacityScrapeBackchannel, allAZs []limes.AvailabilityZone) (result liquid.ServiceCapacityReport, err error) {
	req, err := l.BuildServiceCapacityRequest(backchannel, allAZs)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}

	result, err = l.LiquidClient.GetCapacityReport(ctx, req)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}
	if result.InfoVersion != l.LiquidServiceInfo.Version {
		logg.Fatal("ServiceInfo version for %s changed from %d to %d; restarting now to reload ServiceInfo...",
			l.LiquidServiceType, l.LiquidServiceInfo.Version, result.InfoVersion)
	}
	err = liquid.ValidateServiceInfo(l.LiquidServiceInfo)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}
	err = liquid.ValidateCapacityReport(result, req, l.LiquidServiceInfo)
	if err != nil {
		return liquid.ServiceCapacityReport{}, err
	}

	// manual capacity collection
	fixedCapaConfig, exists := l.FixedCapacityConfiguration.Unpack()
	if exists {
		if result.Resources == nil {
			result.Resources = make(map[liquid.ResourceName]*liquid.ResourceCapacityReport)
		}
		for resName, capacity := range fixedCapaConfig {
			result.Resources[resName] = &liquid.ResourceCapacityReport{
				PerAZ: liquid.InAnyAZ(liquid.AZResourceCapacityReport{Capacity: capacity}),
			}
		}
	}

	// prometheus capacity collection
	prometheusCapaConfig, exists := l.PrometheusCapacityConfiguration.Unpack()
	if exists {
		if result.Resources == nil {
			result.Resources = make(map[liquid.ResourceName]*liquid.ResourceCapacityReport)
		}
		client, err := prometheusCapaConfig.APIConfig.Connect()
		if err != nil {
			return liquid.ServiceCapacityReport{}, err
		}
		for resName, query := range prometheusCapaConfig.Queries {
			azReports, err := prometheusScrapeOneResource(prometheusCapaConfig, ctx, client, query, allAZs)
			if err != nil {
				return liquid.ServiceCapacityReport{}, fmt.Errorf("while scraping prometheus capacity %q/%q: %w", l.ServiceType, resName, err)
			}
			result.Resources[resName] = &liquid.ResourceCapacityReport{
				PerAZ: azReports,
			}
		}
	}

	return result, nil
}

// prometheusScrapeOneResource retrieves capacity for one resource via a prometheus client.
func prometheusScrapeOneResource(p PrometheusCapacityConfiguration, ctx context.Context, client promquery.Client, query string, allAZs []limes.AvailabilityZone) (map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport, error) {
	vector, err := client.GetVector(ctx, query)
	if err != nil {
		return nil, err
	}

	// for known AZs, we expect exactly one result;
	// all unknown AZs get lumped into AvailabilityZoneUnknown
	matchedSamples := make(map[limes.AvailabilityZone]*model.Sample)
	var unmatchedSamples []*model.Sample
	for _, sample := range vector {
		az := limes.AvailabilityZone(sample.Metric["az"])
		switch {
		case az == "":
			return nil, fmt.Errorf(`missing label "az" on metric %v = %g`, sample.Metric, sample.Value)
		case slices.Contains(allAZs, az) || az == limes.AvailabilityZoneAny:
			if matchedSamples[az] != nil {
				other := matchedSamples[az]
				return nil, fmt.Errorf(`multiple samples for az=%q: found %v = %g and %v = %g`, az, sample.Metric, sample.Value, other.Metric, other.Value)
			}
			matchedSamples[az] = sample
		default:
			unmatchedSamples = append(unmatchedSamples, sample)
		}
	}

	// build result
	result := make(map[liquid.AvailabilityZone]*liquid.AZResourceCapacityReport)
	for az, sample := range matchedSamples {
		result[az] = &liquid.AZResourceCapacityReport{
			Capacity: uint64(sample.Value),
		}
	}
	if len(result) == 0 || len(unmatchedSamples) > 0 {
		unmatchedCapacity := float64(0.0)
		for _, sample := range unmatchedSamples {
			unmatchedCapacity += float64(sample.Value)
		}
		result[limes.AvailabilityZoneUnknown] = &liquid.AZResourceCapacityReport{
			Capacity: uint64(unmatchedCapacity),
		}
	}

	// validate result
	if !p.AllowZeroCapacity {
		totalCapacity := uint64(0)
		for _, azData := range result {
			totalCapacity += azData.Capacity
		}
		if totalCapacity == 0 {
			return nil, errors.New("got 0 total capacity, but allow_zero_capacity = false")
		}
	}

	return result, nil
}

// BuildServiceCapacityRequest generates the request body payload for querying
// the LIQUID API endpoint /v1/report-capacity
func (l *LiquidConnection) BuildServiceCapacityRequest(backchannel CapacityScrapeBackchannel, allAZs []limes.AvailabilityZone) (liquid.ServiceCapacityRequest, error) {
	req := liquid.ServiceCapacityRequest{
		AllAZs:           allAZs,
		DemandByResource: make(map[liquid.ResourceName]liquid.ResourceDemand, len(l.LiquidServiceInfo.Resources)),
	}

	var err error
	for resName, resInfo := range l.LiquidServiceInfo.Resources {
		if !resInfo.HasCapacity {
			continue
		}
		if !resInfo.NeedsResourceDemand {
			continue
		}
		req.DemandByResource[resName], err = backchannel.GetResourceDemand(l.ServiceType, resName)
		if err != nil {
			return liquid.ServiceCapacityRequest{}, fmt.Errorf("while getting resource demand for %s/%s: %w", l.ServiceType, resName, err)
		}
	}
	return req, nil
}

// BuildServiceUsageRequest generates the request body payload for querying
// the LIQUID API endpoint /v1/projects/:uuid/report-usage
func (l *LiquidConnection) BuildServiceUsageRequest(project KeystoneProject, allAZs []limes.AvailabilityZone) (liquid.ServiceUsageRequest, error) {
	req := liquid.ServiceUsageRequest{AllAZs: allAZs}
	if l.LiquidServiceInfo.UsageReportNeedsProjectMetadata {
		req.ProjectMetadata = Some(project.ForLiquid())
	}
	return req, nil
}

// SetQuota updates the backend service's quotas for the given project in the
// given domain to the values specified here.
func (l *LiquidConnection) SetQuota(ctx context.Context, project KeystoneProject, quotaReq map[liquid.ResourceName]liquid.ResourceQuotaRequest) error {
	req := liquid.ServiceQuotaRequest{Resources: quotaReq}
	if l.LiquidServiceInfo.QuotaUpdateNeedsProjectMetadata {
		req.ProjectMetadata = Some(project.ForLiquid())
	}

	return l.LiquidClient.PutQuota(ctx, project.UUID, req)
}

// ScrapeRates queries the backend service for the usage data of all rates.
//
// The `allAZs` list comes from the Limes config and should be used when
// building AZ-aware usage data, to ensure that each AZ-aware resource reports
// usage in all available AZs, even when the project in question does not have
// usage in every AZ.
//
// The serializedState return value is persisted in the Limes DB and returned
// back to the next ScrapeRates() call for the same project in the
// prevSerializedState argument. Besides that, this field is not interpreted
// by the core application in any way. The LiquidConnection can use this
// field to carry state between ScrapeRates() calls, esp. to detect and handle
// counter resets in the backend.
func (l *LiquidConnection) ScrapeRates(ctx context.Context, project KeystoneProject, allAZs []limes.AvailabilityZone, prevSerializedState string) (result map[liquid.RateName]*big.Int, serializedState string, err error) {
	// shortcut for liquids that do not have rates
	if len(l.LiquidServiceInfo.Rates) == 0 {
		return nil, "", nil
	}

	req := liquid.ServiceUsageRequest{
		AllAZs:          allAZs,
		SerializedState: json.RawMessage(prevSerializedState),
	}
	if l.LiquidServiceInfo.UsageReportNeedsProjectMetadata {
		req.ProjectMetadata = Some(project.ForLiquid())
	}

	resp, err := l.LiquidClient.GetUsageReport(ctx, project.UUID, req)
	if err != nil {
		return nil, "", err
	}
	if resp.InfoVersion != l.LiquidServiceInfo.Version {
		logg.Fatal("ServiceInfo version for %s changed from %d to %d; restarting now to reload ServiceInfo...",
			l.LiquidServiceType, l.LiquidServiceInfo.Version, resp.InfoVersion)
	}

	result = make(map[liquid.RateName]*big.Int)
	for rateName := range l.LiquidServiceInfo.Rates {
		rateReport := resp.Rates[rateName]
		if rateReport == nil {
			return nil, "", fmt.Errorf("missing report for rate %q", rateName)
		}

		// TODO: add AZ-awareness for rate usage in Limes
		// (until this is done, we take the sum over all AZs here)
		result[rateName] = &big.Int{}
		for _, azReport := range rateReport.PerAZ {
			if usage, ok := azReport.Usage.Unpack(); ok {
				var x big.Int
				result[rateName] = x.Add(result[rateName], usage)
			}
		}
	}

	return result, string(resp.SerializedState), nil
}

// CapacityScrapeBackchannel is a callback interface that is provided to
// LiquidConnection.Scrape(). Most capacity scrape implementations will not need
// this, but some esoteric usecases use this information to distribute
// available capacity among resources in accordance with customer demand.
//
// Note that ResourceDemand is measured against effective capacity, which
// differs from the raw capacity collect from a liquid or another source by
// the OvercommitFactor.
type CapacityScrapeBackchannel interface {
	GetResourceDemand(serviceType db.ServiceType, resourceName liquid.ResourceName) (liquid.ResourceDemand, error)
}

// BuildAPIRateInfo converts a RateInfo from LIQUID into the API format.
func BuildAPIRateInfo(rateName limesrates.RateName, rateInfo liquid.RateInfo) limesrates.RateInfo {
	return limesrates.RateInfo{
		Name: rateName,
		Unit: rateInfo.Unit,
	}
}
