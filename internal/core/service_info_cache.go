// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"sync"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/lib/pq"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"

	. "go.xyrillian.de/gg/option"
)

// serviceNotifyChannel is the PostgreSQL NOTIFY channel name used to signal
// that a service's data has changed and the cache should be invalidated.
const serviceNotifyChannel = "limitas_service_update"

// The following types are defined explicitly to aid using the return
// types of functions of ServiceInfoCache as parameters of other functions.
// Basically, they are the internal representation of liquid.ServiceInfo.
type (
	// ServicesByType are all services, indexed by their service type.
	ServicesByType map[db.ServiceType]db.Service
	// ResourcesByNameType are all resources, indexed by their name and type.
	ResourcesByNameType map[db.ServiceType]ResourcesByName
	// ResourcesByName are all resources of a single service, indexed by their name.
	ResourcesByName map[liquid.ResourceName]db.Resource
	// AZResourcesByAZNameType are all az_resources, indexed by their az, name and type.
	AZResourcesByAZNameType map[db.ServiceType]AZResourcesByAZName
	// AZResourcesByAZName are all az_resources of a single service, indexed by their az and name.
	AZResourcesByAZName map[liquid.ResourceName]AZResourcesByAZ
	// AZResourcesByAZ are all az_resources of a single service and resource, indexed by their az.
	AZResourcesByAZ map[limes.AvailabilityZone]db.AZResource
	// RatesByNameType are all rates, indexed by their name and type.
	RatesByNameType map[db.ServiceType]RatesByName
	// RatesByName are all rates of a single service, indexed by their name.
	RatesByName map[liquid.RateName]db.Rate
	// CategoriesByID are all categories, indexed by their id.
	CategoriesByID map[db.CategoryID]db.Category
)

// ServiceInfoCache is the interface to the database to retrieve all data,
// which was previously populated from the liquid.ServiceInfo. The principle
// of this cache is to load all the data once on startup and then use a
// postgresql NOTIFY-mechanism to keep it up to date by service.
// The reload mechanism can be disabled for testing purposes, because data does
// not change in a separate go-routine during normal testing, only in the specific tests.
// As services can only change by restart (because they also require the limes
// configuration to change), a change in the set of services is not possible
// during runtime.
type ServiceInfoCache struct {
	// state
	DB       *gorp.DbMap
	listener *pq.Listener
	// we use one mutex as all data is written together and reading is quick
	dataMutex sync.RWMutex

	// OnInvalidate is an optional channel that receives a signal after each
	// successful cache invalidation triggered by pg-notify. Used in tests to
	// synchronize with the asynchronous notification mechanism. The send is
	// non-blocking so production code is unaffected when nobody reads.
	OnInvalidate   <-chan struct{}
	sendInvalidate chan<- struct{}

	// data
	servicesByType          ServicesByType
	resourcesByNameType     ResourcesByNameType
	azResourcesByAZNameType AZResourcesByAZNameType
	ratesByNameType         RatesByNameType
	categoriesByID          CategoriesByID
}

// NewServiceInfoCache generates a ServiceInfoCache and fills all services' data.
func NewServiceInfoCache(dbm *gorp.DbMap, dbURL Option[url.URL]) (*ServiceInfoCache, error) {
	sic := &ServiceInfoCache{
		DB: dbm,

		servicesByType:          make(ServicesByType),
		resourcesByNameType:     make(ResourcesByNameType),
		azResourcesByAZNameType: make(AZResourcesByAZNameType),
		ratesByNameType:         make(RatesByNameType),
		categoriesByID:          make(CategoriesByID),
	}

	// populate all data from the DB on startup
	err := sic.InvalidateService(None[db.ServiceType]())
	if err != nil {
		return nil, err
	}

	// set up NOTIFY listener if a DB URL was provided (disabled in tests)
	if u, ok := dbURL.Unpack(); ok {
		ch := make(chan struct{}, 1)
		sic.OnInvalidate = ch
		sic.sendInvalidate = ch
		sic.listener = pq.NewListener(
			u.String(),
			10*time.Second,
			time.Minute,
			func(ev pq.ListenerEventType, err error) {
				if err != nil {
					logg.Error("SIC pg listener event %d: %s", ev, err.Error())
				}
			},
		)
		if err := sic.listener.Listen(serviceNotifyChannel); err != nil {
			return nil, err
		}
		go sic.listenForInvalidations()
	}

	return sic, nil
}

// listenForInvalidations waits for NOTIFY messages on serviceNotifyChannel.
// The payload is expected to be the service type string. On reconnect, a nil
// notification is sent by pq — in that case we invalidate all services to be safe.
func (s *ServiceInfoCache) listenForInvalidations() {
	for notification := range s.listener.Notify {
		if notification == nil {
			// connection was re-established; we may have missed notifications, so we invalidate all
			logg.Info("SIC pg listener reconnected, reloading all services")
			err := s.InvalidateService(None[db.ServiceType]())
			if err != nil {
				logg.Fatal("SIC failed to reload all services after reconnect: %s", err.Error())
			}
			s.signalInvalidation()
			continue
		}

		serviceType := db.ServiceType(notification.Extra)
		logg.Info("SIC invalidating service %q due to pg NOTIFY", serviceType)
		err := s.InvalidateService(Some(serviceType))
		if err != nil {
			logg.Fatal("SIC failed to reload service %q: %s", serviceType, err.Error())
		}
		s.signalInvalidation()
	}
}

// Close shuts down the pg-notify listener connection, if one is active.
// This causes the listenForInvalidations goroutine to exit cleanly.
func (s *ServiceInfoCache) Close() {
	if s.listener != nil {
		// as this is a teardown operation, we can ignore - e.g. if the db connection was already lost.
		_ = s.listener.Close()
	}
}

// signalInvalidation sends a non-blocking signal on OnInvalidate (if set)
// to notify waiters that a cache invalidation has completed.
func (s *ServiceInfoCache) signalInvalidation() {
	if s.sendInvalidate != nil {
		select {
		case s.sendInvalidate <- struct{}{}:
		default:
		}
	}
}

// InvalidateService will make the ServiceInfoCache reload one service (if
// serviceType is provided) or all services (if no serviceType is provided).
func (s *ServiceInfoCache) InvalidateService(serviceType Option[db.ServiceType]) error {
	s.dataMutex.Lock()
	defer s.dataMutex.Unlock()

	if st, ok := serviceType.Unpack(); ok {
		delete(s.servicesByType, st)
		delete(s.resourcesByNameType, st)
		delete(s.azResourcesByAZNameType, st)
		delete(s.ratesByNameType, st)

		s.categoriesByID = make(CategoriesByID)
	} else {
		s.servicesByType = make(ServicesByType)
		s.resourcesByNameType = make(ResourcesByNameType)
		s.azResourcesByAZNameType = make(AZResourcesByAZNameType)
		s.ratesByNameType = make(RatesByNameType)
		s.categoriesByID = make(CategoriesByID)
	}

	// now we fill the cache for the invalidated services again
	servicesByType, err := db.BuildIndexOfDBResult(s.DB, func(s db.Service) db.ServiceType { return s.Type }, "SELECT * FROM services WHERE type = $1 OR $1 IS NULL", serviceType)
	if err != nil {
		return fmt.Errorf("while reading services for type(s) %v: %w", serviceType, err)
	}
	maps.Copy(s.servicesByType, servicesByType)

	resourcesByPath, err := db.BuildIndexOfDBResult(
		s.DB,
		func(r db.Resource) db.ResourcePath { return r.Path },
		"SELECT r.* FROM resources r JOIN services s ON r.service_id = s.id WHERE s.type = $1 OR $1 IS NULL",
		serviceType,
	)
	if err != nil {
		return fmt.Errorf("while reading resources for type(s) %v: %w", serviceType, err)
	}
	for path, resource := range resourcesByPath {
		if _, sExists := s.resourcesByNameType[path.ServiceType]; !sExists {
			s.resourcesByNameType[path.ServiceType] = make(ResourcesByName)
		}
		s.resourcesByNameType[path.ServiceType][path.ResourceName] = resource
	}

	azResourcesByPath, err := db.BuildIndexOfDBResult(
		s.DB,
		func(a db.AZResource) db.AZResourcePath { return a.Path },
		"SELECT azr.* FROM az_resources azr JOIN resources r ON azr.resource_id = r.id JOIN services s ON r.service_id = s.id WHERE s.type = $1 OR $1 IS NULL",
		serviceType,
	)
	if err != nil {
		return fmt.Errorf("while reading az_resources for type(s) %v: %w", serviceType, err)
	}
	for path, azResource := range azResourcesByPath {
		if _, sExists := s.azResourcesByAZNameType[path.ServiceType]; !sExists {
			s.azResourcesByAZNameType[path.ServiceType] = make(AZResourcesByAZName)
		}
		if _, rExists := s.azResourcesByAZNameType[path.ServiceType][path.ResourceName]; !rExists {
			s.azResourcesByAZNameType[path.ServiceType][path.ResourceName] = make(AZResourcesByAZ)
		}
		s.azResourcesByAZNameType[path.ServiceType][path.ResourceName][path.AvailabilityZone] = azResource
	}

	ratesByPath, err := db.BuildIndexOfDBResult(
		s.DB,
		func(r db.Rate) db.RatePath { return r.Path },
		"SELECT ra.* FROM rates ra JOIN services s ON ra.service_id = s.id WHERE s.type = $1 OR $1 IS NULL",
		serviceType,
	)
	if err != nil {
		return fmt.Errorf("while reading rates for type(s) %v: %w", serviceType, err)
	}
	for path, rate := range ratesByPath {
		if _, rExists := s.ratesByNameType[path.ServiceType]; !rExists {
			s.ratesByNameType[path.ServiceType] = make(RatesByName)
		}
		s.ratesByNameType[path.ServiceType][path.RateName] = rate
	}

	s.categoriesByID, err = db.BuildIndexOfDBResult(
		s.DB,
		func(c db.Category) db.CategoryID { return c.ID },
		"SELECT * FROM categories",
	)
	if err != nil {
		return fmt.Errorf("while reading categories: %w", err)
	}
	return nil
}

// deepCloneMap returns a deep copy of a map by cloning each value using the
// provided cloneValue function. For leaf maps (where values are not maps),
// pass maps.Clone directly. For nested maps, pass a function that itself
// calls deepCloneMap to clone the next level.
func deepCloneMap[M ~map[K]V, K comparable, V any](m M, cloneValue func(V) V) M {
	if m == nil {
		return nil
	}
	result := make(M, len(m))
	for k, v := range m {
		result[k] = cloneValue(v)
	}
	return result
}

// GetServices returns a map of all cached services keyed by their service type.
func (s *ServiceInfoCache) GetServices() ServicesByType {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	return maps.Clone(s.servicesByType)
}

// GetServiceForType returns the cached service for the given service type.
func (s *ServiceInfoCache) GetServiceForType(serviceType db.ServiceType) (db.Service, bool) {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	val, ok := s.servicesByType[serviceType]
	return val, ok
}

// GetServiceForLoc returns the cached service for the serviceType of loc.
func (s *ServiceInfoCache) GetServiceForLoc(loc AZResourceLocation) (db.Service, bool) {
	return s.GetServiceForType(loc.ServiceType)
}

// HasServiceForType checks whether the given service exists.
func (s *ServiceInfoCache) HasServiceForType(serviceType db.ServiceType) bool {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	_, exists := s.servicesByType[serviceType]
	return exists
}

// GetResources returns all cached resources, indexed by service type and resource name.
func (s *ServiceInfoCache) GetResources() ResourcesByNameType {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	return deepCloneMap(s.resourcesByNameType, maps.Clone)
}

// GetResourcesForType returns all cached resources for the given service type, keyed by resource name.
func (s *ServiceInfoCache) GetResourcesForType(serviceType db.ServiceType) (ResourcesByName, bool) {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	val, ok := s.resourcesByNameType[serviceType]
	return val, ok
}

// GetResourceForTypeName returns the cached resource for the given service type and resource name.
func (s *ServiceInfoCache) GetResourceForTypeName(serviceType db.ServiceType, resourceName liquid.ResourceName) (db.Resource, bool) {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	val, ok := s.resourcesByNameType[serviceType][resourceName]
	return val, ok
}

// GetResourceForLoc returns the cached resource for the serviceType and resourceName of loc.
func (s *ServiceInfoCache) GetResourceForLoc(loc AZResourceLocation) (db.Resource, bool) {
	return s.GetResourceForTypeName(loc.ServiceType, loc.ResourceName)
}

// GetAZResources returns all cached AZ resources, indexed by service type, resource name, and availability zone.
func (s *ServiceInfoCache) GetAZResources() AZResourcesByAZNameType {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	return deepCloneMap(s.azResourcesByAZNameType, func(inner AZResourcesByAZName) AZResourcesByAZName {
		return deepCloneMap(inner, maps.Clone)
	})
}

// GetAZResourcesForType returns all cached AZ resources for the given service type, keyed by resource name and availability zone.
func (s *ServiceInfoCache) GetAZResourcesForType(serviceType db.ServiceType) (AZResourcesByAZName, bool) {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	val, ok := s.azResourcesByAZNameType[serviceType]
	return deepCloneMap(val, maps.Clone), ok
}

// GetAZResourcesForTypeName returns all cached AZ resources for the given service type, resource name, keyed by availability zone.
func (s *ServiceInfoCache) GetAZResourcesForTypeName(serviceType db.ServiceType, resourceName liquid.ResourceName) (AZResourcesByAZ, bool) {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	val, ok := s.azResourcesByAZNameType[serviceType][resourceName]
	return maps.Clone(val), ok
}

// GetAZResourceForTypeNameAZ returns the cached AZ resource for the given service type, resource name, and availability zone.
func (s *ServiceInfoCache) GetAZResourceForTypeNameAZ(serviceType db.ServiceType, resourceName liquid.ResourceName, az limes.AvailabilityZone) (db.AZResource, bool) {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	val, ok := s.azResourcesByAZNameType[serviceType][resourceName][az]
	return val, ok
}

// GetAZResourceForLoc returns the cached AZ resource for this location.
func (s *ServiceInfoCache) GetAZResourceForLoc(loc AZResourceLocation) (db.AZResource, bool) {
	return s.GetAZResourceForTypeNameAZ(loc.ServiceType, loc.ResourceName, loc.AvailabilityZone)
}

// GetRates returns all cached rates, indexed by service type and rate name.
func (s *ServiceInfoCache) GetRates() RatesByNameType {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	return deepCloneMap(s.ratesByNameType, maps.Clone)
}

// GetRatesForType returns all cached rates, indexed by service type and rate name.
func (s *ServiceInfoCache) GetRatesForType(serviceType db.ServiceType) (RatesByName, bool) {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	val, ok := s.ratesByNameType[serviceType]
	return maps.Clone(val), ok
}

// GetRateForTypeName returns the cached rate for the given service type and rate name.
func (s *ServiceInfoCache) GetRateForTypeName(serviceType db.ServiceType, rateName liquid.RateName) (db.Rate, bool) {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	val, ok := s.ratesByNameType[serviceType][rateName]
	return val, ok
}

// GetCategories returns all cached categories, indexed by category ID.
func (s *ServiceInfoCache) GetCategories() CategoriesByID {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	return maps.Clone(s.categoriesByID)
}

// GetCategoryForID returns the cached category for the given category ID.
func (s *ServiceInfoCache) GetCategoryForID(categoryID db.CategoryID) (db.Category, bool) {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	val, ok := s.categoriesByID[categoryID]
	return val, ok
}

// GetServiceInfo should only be used when interacting with the liquid
// where the data of ServiceInfoCache needs to be available in the form of
// liquid.ServiceInfo!
func (s *ServiceInfoCache) GetServiceInfo(serviceType db.ServiceType) (info liquid.ServiceInfo, err error) {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	// we can assume the data is saved because of the call context
	service := s.servicesByType[serviceType]
	resources := s.resourcesByNameType[serviceType]
	rates := s.ratesByNameType[serviceType]
	categories := s.categoriesByID

	capacityMetricFamilies, err := util.JSONToAny[map[liquid.MetricName]liquid.MetricFamilyInfo](service.CapacityMetricFamiliesJSON, "capacity_metric_families")
	if err != nil {
		return info, fmt.Errorf("while unmarshalling CapacityMetricFamilies: %w", err)
	}
	usageMetricFamilies, err := util.JSONToAny[map[liquid.MetricName]liquid.MetricFamilyInfo](service.UsageMetricFamiliesJSON, "usage_metric_families")
	if err != nil {
		return info, fmt.Errorf("while unmarshalling UsageMetricFamilies: %w", err)
	}

	info = liquid.ServiceInfo{
		Version:                                service.LiquidVersion,
		DisplayName:                            service.DisplayName,
		UsageReportNeedsProjectMetadata:        service.UsageReportNeedsProjectMetadata,
		QuotaUpdateNeedsProjectMetadata:        service.QuotaUpdateNeedsProjectMetadata,
		CommitmentHandlingNeedsProjectMetadata: service.CommitmentHandlingNeedsProjectMetadata,
		Resources:                              make(map[liquid.ResourceName]liquid.ResourceInfo, len(resources)),
		Rates:                                  make(map[liquid.RateName]liquid.RateInfo, len(rates)),
		Categories:                             make(map[liquid.CategoryName]liquid.CategoryInfo),
		CapacityMetricFamilies:                 capacityMetricFamilies,
		UsageMetricFamilies:                    usageMetricFamilies,
	}
	// reconstruct resource infos
	for name, res := range resources {
		resInfo := liquid.ResourceInfo{
			DisplayName:         res.DisplayName,
			Unit:                res.Unit,
			Topology:            res.Topology,
			HasCapacity:         res.HasCapacity,
			NeedsResourceDemand: res.NeedsResourceDemand,
			HasQuota:            res.HasQuota,
			HandlesCommitments:  res.HandlesCommitments,
		}
		if res.AttributesJSON != "" {
			resInfo.Attributes = json.RawMessage(res.AttributesJSON)
		}
		if categoryID, ok := res.CategoryID.Unpack(); ok {
			if cat, exists := categories[categoryID]; exists {
				resInfo.Category = Some(cat.Name)
				info.Categories[cat.Name] = liquid.CategoryInfo{
					DisplayName: cat.DisplayName,
				}
			}
		}
		info.Resources[name] = resInfo
	}
	// reconstruct rate infos
	for name, rate := range rates {
		if !rate.FromLiquid {
			// important, so that we don't report missing rates when validating reports coming from liquid
			continue
		}
		rateInfo := liquid.RateInfo{
			DisplayName: rate.DisplayName,
			Unit:        rate.Unit,
			Topology:    rate.Topology,
			HasUsage:    rate.HasUsage,
		}
		if categoryID, ok := rate.CategoryID.Unpack(); ok {
			if cat, exists := categories[categoryID]; exists {
				rateInfo.Category = Some(cat.Name)
				info.Categories[cat.Name] = liquid.CategoryInfo{
					DisplayName: cat.DisplayName,
				}
			}
		}
		info.Rates[name] = rateInfo
	}
	return info, nil
}

///////////////////////////////
// utility functions on the results of the above functions
///////////////////////////////

// HasResourceForTypeName checks whether the given service and resource exist.
func (s *ServiceInfoCache) HasResourceForTypeName(serviceType db.ServiceType, resourceName liquid.ResourceName) bool {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	_, exists := s.resourcesByNameType[serviceType][resourceName]
	return exists
}

// HasUsageForRate checks whether the given service and resource exist and whether it has usage.
func (r RatesByNameType) HasUsageForRate(serviceType db.ServiceType, rateName liquid.RateName) bool {
	rate, exists := r[serviceType][rateName]
	if !exists {
		return false
	}
	return rate.HasUsage
}
