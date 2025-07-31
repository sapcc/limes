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
	"sync"
	"time"

	"github.com/go-gorp/gorp/v3"
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

// LiquidConnection holds all the information which is necessary to interact with the LiquidClient.
// The state information of the LiquidConnection is persisted in the database to avoid reloading
// in case of configuration changes.
type LiquidConnection struct {
	// configuration
	LiquidServiceType               string
	ServiceType                     db.ServiceType
	FixedCapacityConfiguration      Option[map[liquid.ResourceName]uint64]
	PrometheusCapacityConfiguration Option[PrometheusCapacityConfiguration]
	AvailabilityZones               []limes.AvailabilityZone
	RateLimits                      ServiceRateLimitConfiguration

	// state
	liquidServiceInfo      liquid.ServiceInfo
	liquidServiceInfoMutex sync.RWMutex
	LiquidClient           LiquidClient
	DB                     *gorp.DbMap

	// slots for test doubles
	timeNow func() time.Time
}

// MakeLiquidConnection is a factory to fill all necessary configuration fields
func MakeLiquidConnection(lc LiquidConfiguration, serviceType db.ServiceType, availabilityZones []limes.AvailabilityZone, rateLimits ServiceRateLimitConfiguration, timeNow func() time.Time, dbm *gorp.DbMap) LiquidConnection {
	if lc.LiquidServiceType == "" {
		lc.LiquidServiceType = "liquid-" + string(serviceType)
	}
	return LiquidConnection{
		LiquidServiceType:               lc.LiquidServiceType,
		ServiceType:                     serviceType,
		FixedCapacityConfiguration:      lc.FixedCapacityConfiguration,
		PrometheusCapacityConfiguration: lc.PrometheusCapacityConfiguration,
		AvailabilityZones:               availabilityZones,
		RateLimits:                      rateLimits,
		timeNow:                         timeNow,
		DB:                              dbm,
	}
}

// Init is called before any other interface methods, and allows the LiquidConnection to
// perform first-time initialization.
func (l *LiquidConnection) Init(ctx context.Context, client *gophercloud.ProviderClient, eo gophercloud.EndpointOpts) (err error) {
	l.LiquidClient, err = NewLiquidClient(client, eo, liquidapi.ClientOpts{ServiceType: l.LiquidServiceType})
	if err != nil {
		return err
	}
	serviceInfo, apiSuccess, err := l.retrieveServiceInfo(ctx, true)
	if err != nil {
		return fmt.Errorf("getting ServiceInfo: %w", err)
	}

	if !apiSuccess {
		l.liquidServiceInfoMutex.Lock()
		defer l.liquidServiceInfoMutex.Unlock()
		l.liquidServiceInfo = serviceInfo
		return nil
	}

	_, err = SaveServiceInfoToDB(l.ServiceType, serviceInfo, l.AvailabilityZones, l.RateLimits, l.timeNow(), l.DB)
	if err != nil {
		return fmt.Errorf("saving ServiceInfo: %w", err)
	}

	l.liquidServiceInfoMutex.Lock()
	defer l.liquidServiceInfoMutex.Unlock()
	l.liquidServiceInfo = serviceInfo
	return nil
}

// compareServiceInfoVersions compares a report version of the ServiceInfo with the saved version
// and triggers the update and persisting if necessary.
func (l *LiquidConnection) compareServiceInfoVersions(ctx context.Context, infoVersion int64) (srv db.Service, err error) {
	currentVersion := l.ServiceInfo().Version
	if infoVersion == currentVersion {
		return srv, nil
	}

	logg.Info("ServiceInfo version for %s changed from %d to %d; reloading and persisting ServiceInfo.", l.LiquidServiceType, currentVersion, infoVersion)
	serviceInfo, _, err := l.retrieveServiceInfo(ctx, false)
	if err != nil {
		return srv, err
	}
	// recheck to be sure, that there was no update between pulling the report and getting the ServiceInfo
	newVersion := serviceInfo.Version
	if infoVersion != newVersion {
		return srv, fmt.Errorf("ServiceInfo version mismatch for %s after update: GetInfo %d, report %d", l.LiquidServiceType, newVersion, infoVersion)
	}
	srv, err = SaveServiceInfoToDB(l.ServiceType, serviceInfo, l.AvailabilityZones, l.RateLimits, l.timeNow(), l.DB)
	if err != nil {
		return srv, err
	}

	l.liquidServiceInfoMutex.Lock()
	defer l.liquidServiceInfoMutex.Unlock()
	l.liquidServiceInfo = serviceInfo
	return srv, nil
}

// retrieveServiceInfo queries the backend service for the latest ServiceInfo and validates it.
// If the liquid is not reachable it can fall back to reading the ServiceInfo from the database
// - if the dbFallback parameter is set. It is only called on init and when the InfoVersion changes.
func (l *LiquidConnection) retrieveServiceInfo(ctx context.Context, dbFallback bool) (result liquid.ServiceInfo, apiSuccess bool, err error) {
	apiSuccess = true
	result, err = l.LiquidClient.GetInfo(ctx)
	// result, err := liquid.ServiceInfo{}, errors.New("some error")
	if err != nil && dbFallback {
		apiSuccess = false
		logg.Info("request to Liquid failed for %s, falling back to DB: %w", l.LiquidServiceType, err)
		var serviceInfos map[db.ServiceType]liquid.ServiceInfo
		serviceInfos, err = readServiceInfoFromDB(l.DB, Some(l.ServiceType))
		result = serviceInfos[l.ServiceType]
	}
	if err != nil {
		return result, false, err
	}
	err = liquid.ValidateServiceInfo(result)
	if err != nil {
		return result, false, err
	}
	return result, apiSuccess, nil
}

// ServiceInfo returns metadata for this liquid.
// This includes metadata for all the resources and rates that this liquid scrapes.
func (l *LiquidConnection) ServiceInfo() liquid.ServiceInfo {
	l.liquidServiceInfoMutex.RLock()
	defer l.liquidServiceInfoMutex.RUnlock()
	return l.liquidServiceInfo
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
	lsi := l.ServiceInfo()
	if len(lsi.Resources) == 0 && len(lsi.UsageMetricFamilies) == 0 {
		return liquid.ServiceUsageReport{}, nil
	}

	req, err := BuildServiceUsageRequest(project, allAZs, l.ServiceInfo().UsageReportNeedsProjectMetadata)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	result, err = l.LiquidClient.GetUsageReport(ctx, string(project.UUID), req)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	_, err = l.compareServiceInfoVersions(ctx, result.InfoVersion)
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	err = liquid.ValidateUsageReport(result, req, l.ServiceInfo())
	if err != nil {
		return liquid.ServiceUsageReport{}, err
	}

	return result, nil
}

// ScrapeCapacity queries the backend service(s) for the capacities of the resources
// that this LiquidConnection is concerned with. The result is a two-dimensional map,
// with the first key being the service type, and the second key being the
// resource name.
func (l *LiquidConnection) ScrapeCapacity(ctx context.Context, backchannel CapacityScrapeBackchannel, allAZs []limes.AvailabilityZone) (result liquid.ServiceCapacityReport, srv db.Service, err error) {
	req, err := BuildServiceCapacityRequest(backchannel, allAZs, l.ServiceType, l.ServiceInfo().Resources)
	if err != nil {
		return result, srv, err
	}

	result, err = l.LiquidClient.GetCapacityReport(ctx, req)
	if err != nil {
		return result, srv, err
	}

	srv, err = l.compareServiceInfoVersions(ctx, result.InfoVersion)
	if err != nil {
		return result, srv, err
	}

	err = liquid.ValidateCapacityReport(result, req, l.ServiceInfo())
	if err != nil {
		return result, srv, err
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
			return result, srv, err
		}
		for resName, query := range prometheusCapaConfig.Queries {
			azReports, err := prometheusScrapeOneResource(prometheusCapaConfig, ctx, client, query, allAZs)
			if err != nil {
				return result, srv, fmt.Errorf("while scraping prometheus capacity %q/%q: %w", l.ServiceType, resName, err)
			}
			result.Resources[resName] = &liquid.ResourceCapacityReport{
				PerAZ: azReports,
			}
		}
	}

	return result, srv, nil
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

// BuildServiceCapacityRequest generates the request body payload for querying the LIQUID API
// endpoint /v1/report-capacity. In order to be reusable for exposing an API which prints the
// request for admin purposes, it does not use the LiquidConnection as receiver type.
func BuildServiceCapacityRequest(backchannel CapacityScrapeBackchannel, allAZs []limes.AvailabilityZone, serviceType db.ServiceType, resources map[liquid.ResourceName]liquid.ResourceInfo) (liquid.ServiceCapacityRequest, error) {
	req := liquid.ServiceCapacityRequest{
		AllAZs:           allAZs,
		DemandByResource: make(map[liquid.ResourceName]liquid.ResourceDemand, len(resources)),
	}

	var err error
	for resName, resInfo := range resources {
		if !resInfo.HasCapacity {
			continue
		}
		if !resInfo.NeedsResourceDemand {
			continue
		}
		req.DemandByResource[resName], err = backchannel.GetResourceDemand(serviceType, resName)
		if err != nil {
			return liquid.ServiceCapacityRequest{}, fmt.Errorf("while getting resource demand for %s/%s: %w", serviceType, resName, err)
		}
	}
	return req, nil
}

// BuildServiceUsageRequest generates the request body payload for querying the LIQUID API
// endpoint /v1/projects/:uuid/report-usage. In order to be reusable for exposing an API
// which prints the request for admin purposes, it does not use the LiquidConnection as receiver type.
func BuildServiceUsageRequest(project KeystoneProject, allAZs []limes.AvailabilityZone, usageReportNeedsProjectMetadata bool) (liquid.ServiceUsageRequest, error) {
	req := liquid.ServiceUsageRequest{AllAZs: allAZs}
	if usageReportNeedsProjectMetadata {
		req.ProjectMetadata = Some(project.ForLiquid())
	}
	return req, nil
}

// SetQuota updates the backend service's quotas for the given project in the
// given domain to the values specified here.
func (l *LiquidConnection) SetQuota(ctx context.Context, project KeystoneProject, quotaReq map[liquid.ResourceName]liquid.ResourceQuotaRequest) error {
	req := liquid.ServiceQuotaRequest{Resources: quotaReq}
	if l.ServiceInfo().QuotaUpdateNeedsProjectMetadata {
		req.ProjectMetadata = Some(project.ForLiquid())
	}

	return l.LiquidClient.PutQuota(ctx, string(project.UUID), req)
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
	lsi := l.ServiceInfo()
	if len(lsi.Rates) == 0 {
		return nil, "", nil
	}

	req := liquid.ServiceUsageRequest{
		AllAZs:          allAZs,
		SerializedState: json.RawMessage(prevSerializedState),
	}
	if lsi.UsageReportNeedsProjectMetadata {
		req.ProjectMetadata = Some(project.ForLiquid())
	}

	resp, err := l.LiquidClient.GetUsageReport(ctx, string(project.UUID), req)
	if err != nil {
		return nil, "", err
	}

	_, err = l.compareServiceInfoVersions(ctx, resp.InfoVersion)
	if err != nil {
		return nil, "", err
	}

	result = make(map[liquid.RateName]*big.Int)
	for rateName := range lsi.Rates {
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

// LiquidClient is a wrapper for liquidapi.Client
// Allows for the implementation of a mock client that is used in unit tests
type LiquidClient interface {
	GetInfo(ctx context.Context) (result liquid.ServiceInfo, err error)
	GetCapacityReport(ctx context.Context, req liquid.ServiceCapacityRequest) (result liquid.ServiceCapacityReport, err error)
	GetUsageReport(ctx context.Context, projectUUID string, req liquid.ServiceUsageRequest) (result liquid.ServiceUsageReport, err error)
	PutQuota(ctx context.Context, projectUUID string, req liquid.ServiceQuotaRequest) (err error)
	ChangeCommitments(ctx context.Context, req liquid.CommitmentChangeRequest) (result liquid.CommitmentChangeResponse, err error)
}

// NewLiquidClient is usually a synonym for liquidapi.NewClient().
// In tests, it serves as a dependency injection slot to allow type Cluster to
// access mock liquids prepared by the test's specific setup code.
var NewLiquidClient = func(provider *gophercloud.ProviderClient, eo gophercloud.EndpointOpts, opts liquidapi.ClientOpts) (LiquidClient, error) {
	client, err := liquidapi.NewClient(provider, eo, opts)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize ServiceClient for %s: %w", opts.ServiceType, err)
	}
	return client, nil
}
