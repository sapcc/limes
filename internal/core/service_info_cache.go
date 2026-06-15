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

///////////////////////////////////////////////////////////////////////

// ServiceInfoFilter is a set of possible attributes by which [ServiceInfoSnapshot]
// can be filtered. Most of the time, these come directly or indirectly from an API
// requests' scope, e.g. query filters or attributes of a processed commitment.
type ServiceInfoFilter struct {
	ServiceArea  Option[string]
	ServiceType  Option[db.ServiceType]
	Category     Option[liquid.CategoryName]
	ResourceName Option[liquid.ResourceName]
	RateName     Option[liquid.RateName]
}

///////////////////////////////////////////////////////////////////////

// ServiceInfoReader defines shared methods for reading filtered or unfiltered ServiceInfoSnapshots.
//
// It is implemented by types [ServiceInfoSnapshot] and [FilteredServiceInfoSnapshot].
type ServiceInfoReader interface {
	// GetServices returns all services.
	GetServices() map[db.ServiceType]db.Service
	// GetServiceForType returns the service for the given service type.
	GetServiceForType(serviceType db.ServiceType) (db.Service, bool)
	// GetResources returns all resources.
	GetResources() map[db.ServiceType]map[liquid.ResourceName]db.Resource
	// GetResourcesForType returns all resources for the given service type.
	GetResourcesForType(serviceType db.ServiceType) (map[liquid.ResourceName]db.Resource, bool)
	// GetResourceForPath returns the resource for the given path.
	GetResourceForPath(path db.ResourcePath) (db.Resource, bool)
	// GetAZResources returns all AZ resources.
	GetAZResources() map[db.ServiceType]map[liquid.ResourceName]map[limes.AvailabilityZone]db.AZResource
	// GetAZResourcesForType returns all AZ resources for the given service type.
	GetAZResourcesForType(serviceType db.ServiceType) (map[liquid.ResourceName]map[limes.AvailabilityZone]db.AZResource, bool)
	// GetAZResourcesForPath returns all AZ resources for the given ResourcePath.
	GetAZResourcesForPath(path db.ResourcePath) (map[limes.AvailabilityZone]db.AZResource, bool)
	// GetAZResourceForPath returns the AZ resource for the given AZResourcePath.
	GetAZResourceForPath(path db.AZResourcePath) (db.AZResource, bool)
	// GetRates returns all rates.
	GetRates() map[db.ServiceType]map[liquid.RateName]db.Rate
	// GetRatesForType returns all rates for the given service type.
	GetRatesForType(serviceType db.ServiceType) (map[liquid.RateName]db.Rate, bool)
	// GetRateForPath returns the rate for the given service type and rate name.
	GetRateForPath(path db.RatePath) (db.Rate, bool)
	// GetCategories returns all categories.
	GetCategories() map[db.CategoryID]db.Category
	// GetCategoryForID returns the category for the given ID.
	GetCategoryForID(categoryID db.CategoryID) (db.Category, bool)
}

var (
	// prove interface implementations
	_ ServiceInfoReader = ServiceInfoSnapshot{}
	_ ServiceInfoReader = FilteredServiceInfoSnapshot{}
)

///////////////////////////////////////////////////////////////////////

type (
	// ServiceInfoSnapshot is the combined representation of db.Service, db.Resource
	// db.AZResource, db.Rate and db.Category. We chose to provide this as only
	// data structure and output of ServiceInfoCache mainly for 2 reasons:
	// This ensures we get one consistent state from the ServiceInfoCache and
	// not multiple outputs where the cache might have changed in between.
	// Also, we can express filtering all entities by the same ServiceInfoFilter
	// which makes application of the same filter to all entities easier.
	// ServiceInfoSnapshot offers almost the same external method set for
	// on-read access as FilteredServiceInfoSnapshot, they are defined by the
	// ServiceInfoReader interface.
	ServiceInfoSnapshot struct {
		services    servicesByType
		resources   resourcesByNameType
		azResources azResourcesByAZNameType
		rates       ratesByNameType
		categories  categoriesByID
		// necessary for constructing filters by area
		areaMapping map[db.ServiceType]string
	}

	// shorthands for use within this file
	servicesByType          = map[db.ServiceType]db.Service
	resourcesByNameType     = map[db.ServiceType]resourcesByName
	resourcesByName         = map[liquid.ResourceName]db.Resource
	azResourcesByAZNameType = map[db.ServiceType]azResourcesByAZName
	azResourcesByAZName     = map[liquid.ResourceName]azResourcesByAZ
	azResourcesByAZ         = map[limes.AvailabilityZone]db.AZResource
	ratesByNameType         = map[db.ServiceType]ratesByName
	ratesByName             = map[liquid.RateName]db.Rate
	categoriesByID          = map[db.CategoryID]db.Category
)

// removeDataForType removes all data of a given serviceType, making the cache ready
// for populating this serviceType from scratch. Categories are always flushed
// completely.
func (s ServiceInfoSnapshot) removeDataForType(serviceType db.ServiceType) ServiceInfoSnapshot {
	delete(s.services, serviceType)
	delete(s.resources, serviceType)
	delete(s.azResources, serviceType)
	delete(s.rates, serviceType)
	s.categories = make(map[db.CategoryID]db.Category)
	return s
}

// deepClone delivers a deep copy of the ServiceInfoSnapshot. This is used to
// create a copy which can be altered without interfering with the original.
// It shall only be used from ServiceInfoCache or when creating a
// FilteredServiceInfoSnapshot.
func (s ServiceInfoSnapshot) deepClone() ServiceInfoSnapshot {
	return ServiceInfoSnapshot{
		services:  maps.Clone(s.services),
		resources: deepCloneMap(s.resources, maps.Clone),
		azResources: deepCloneMap(s.azResources, func(inner azResourcesByAZName) azResourcesByAZName {
			return deepCloneMap(inner, maps.Clone)
		}),
		rates:       deepCloneMap(s.rates, maps.Clone),
		categories:  maps.Clone(s.categories),
		areaMapping: s.areaMapping, // should never get modified
	}
}

// Filter applies the filter to the ServiceInfoSnapshot and produces an
// eagerly filtered FilteredServiceInfoSnapshot.
func (s ServiceInfoSnapshot) Filter(filter ServiceInfoFilter) FilteredServiceInfoSnapshot {
	f := FilteredServiceInfoSnapshot{
		snapshot: s.deepClone(),
	}
	if serviceArea, ok := filter.ServiceArea.Unpack(); ok {
		f.filter.ServiceArea = Some(serviceArea)
	}
	if serviceType, ok := filter.ServiceType.Unpack(); ok {
		f.filter.ServiceType = Some(serviceType)
	}
	if resourceCategory, ok := filter.Category.Unpack(); ok {
		f.filter.Category = Some(resourceCategory)
	}
	if resourceName, ok := filter.ResourceName.Unpack(); ok {
		f.filter.ResourceName = Some(resourceName)
	}
	if rateName, ok := filter.RateName.Unpack(); ok {
		f.filter.RateName = Some(rateName)
	}

	// filter services by area
	newSnapshot := f.snapshot.deepClone()
	if areaFilter, ok := f.filter.ServiceArea.Unpack(); ok {
		for serviceType, area := range f.snapshot.areaMapping {
			if area == areaFilter {
				continue
			}
			delete(newSnapshot.services, serviceType)
			delete(newSnapshot.resources, serviceType)
			delete(newSnapshot.azResources, serviceType)
			delete(newSnapshot.rates, serviceType)
		}
	}
	// filter services by type
	if typeFilter, ok := f.filter.ServiceType.Unpack(); ok {
		for serviceType := range newSnapshot.services {
			if typeFilter == serviceType {
				continue
			}
			delete(newSnapshot.services, serviceType)
			delete(newSnapshot.azResources, serviceType)
			delete(newSnapshot.resources, serviceType)
			delete(newSnapshot.rates, serviceType)
		}
	}
	categoriesToRemove := make(map[db.CategoryID]struct{})
	// find categories to remove
	if categoryFilter, ok := f.filter.Category.Unpack(); ok {
		for categoryID, info := range newSnapshot.categories {
			if info.Name != categoryFilter {
				delete(newSnapshot.categories, categoryID)
				categoriesToRemove[categoryID] = struct{}{}
			}
		}
	}
	seenCategories := make(map[db.CategoryID]struct{})
	resourceNameFilter, resourceFilterExists := f.filter.ResourceName.Unpack()
	categoryFilterExists := f.filter.Category.IsSome()
	// filter resources/ az_resources by category or name
	if categoryFilterExists || resourceFilterExists {
		for serviceType, resources := range newSnapshot.resources {
			for resourceName, resource := range resources {
				categoryID, cExists := resource.CategoryID.Unpack()
				_, inRemoveSet := categoriesToRemove[categoryID]
				shouldRemove := (resourceFilterExists && resourceName != resourceNameFilter) ||
					(categoryFilterExists && (!cExists || inRemoveSet))
				if shouldRemove {
					delete(newSnapshot.resources[serviceType], resourceName)
					delete(newSnapshot.azResources[serviceType], resourceName)
					if len(newSnapshot.resources[serviceType]) == 0 && len(newSnapshot.rates[serviceType]) == 0 {
						delete(newSnapshot.services, serviceType)
						delete(newSnapshot.resources, serviceType)
						delete(newSnapshot.azResources, serviceType)
						delete(newSnapshot.rates, serviceType)
					}
				} else {
					seenCategories[categoryID] = struct{}{}
				}
			}
		}
	}
	rateNameFilter, rateFilterExists := f.filter.RateName.Unpack()
	// filter rates by category or name
	if categoryFilterExists || rateFilterExists {
		for serviceType, rates := range newSnapshot.rates {
			for rateName, rate := range rates {
				categoryID, cExists := rate.CategoryID.Unpack()
				_, inRemoveSet := categoriesToRemove[categoryID]
				shouldRemove := (rateFilterExists && rateName != rateNameFilter) ||
					(categoryFilterExists && (!cExists || inRemoveSet))
				if shouldRemove {
					delete(newSnapshot.rates[serviceType], rateName)
					if len(newSnapshot.resources[serviceType]) == 0 && len(newSnapshot.rates[serviceType]) == 0 {
						delete(newSnapshot.services, serviceType)
						delete(newSnapshot.resources, serviceType)
						delete(newSnapshot.azResources, serviceType)
						delete(newSnapshot.rates, serviceType)
					}
				} else {
					seenCategories[categoryID] = struct{}{}
				}
			}
		}
	}

	// if we filtered by rate or service name, we must thin out the categories
	if resourceFilterExists || rateFilterExists {
		for categoryID := range newSnapshot.categories {
			if _, ok := seenCategories[categoryID]; !ok {
				delete(newSnapshot.categories, categoryID)
			}
		}
	}
	f.snapshot = newSnapshot
	return f
}

// GetServices implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetServices() servicesByType {
	return maps.Clone(s.services)
}

// GetServiceForType implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetServiceForType(serviceType db.ServiceType) (db.Service, bool) {
	val, ok := s.services[serviceType]
	return val, ok
}

// GetResources implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetResources() resourcesByNameType {
	return deepCloneMap(s.resources, maps.Clone)
}

// GetResourcesForType implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetResourcesForType(serviceType db.ServiceType) (resourcesByName, bool) {
	val, ok := s.resources[serviceType]
	return maps.Clone(val), ok
}

// GetResourceForPath implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetResourceForPath(path db.ResourcePath) (db.Resource, bool) {
	val, ok := s.resources[path.ServiceType][path.ResourceName]
	return val, ok
}

// GetAZResources implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetAZResources() azResourcesByAZNameType {
	return deepCloneMap(s.azResources, func(inner azResourcesByAZName) azResourcesByAZName {
		return deepCloneMap(inner, maps.Clone)
	})
}

// GetAZResourcesForType implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetAZResourcesForType(serviceType db.ServiceType) (azResourcesByAZName, bool) {
	val, ok := s.azResources[serviceType]
	return deepCloneMap(val, maps.Clone), ok
}

// GetAZResourcesForPath implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetAZResourcesForPath(path db.ResourcePath) (azResourcesByAZ, bool) {
	val, ok := s.azResources[path.ServiceType][path.ResourceName]
	return maps.Clone(val), ok
}

// GetAZResourceForPath implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetAZResourceForPath(path db.AZResourcePath) (db.AZResource, bool) {
	val, ok := s.azResources[path.ServiceType][path.ResourceName][path.AvailabilityZone]
	return val, ok
}

// GetRates implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetRates() ratesByNameType {
	return deepCloneMap(s.rates, maps.Clone)
}

// GetRatesForType implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetRatesForType(serviceType db.ServiceType) (ratesByName, bool) {
	val, ok := s.rates[serviceType]
	return maps.Clone(val), ok
}

// GetRateForPath implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetRateForPath(path db.RatePath) (db.Rate, bool) {
	val, ok := s.rates[path.ServiceType][path.RateName]
	return val, ok
}

// GetCategories implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetCategories() categoriesByID {
	return maps.Clone(s.categories)
}

// GetCategoryForID implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetCategoryForID(categoryID db.CategoryID) (db.Category, bool) {
	val, ok := s.categories[categoryID]
	return val, ok
}

// newEmptyServiceInfoSnapshot() returns an empty ServiceInfoSnapshot with all
// maps initialized on their first level.
func newEmptyServiceInfoSnapshot(config ClusterConfiguration) ServiceInfoSnapshot {
	areaMapping := make(map[db.ServiceType]string)
	for serviceType, liquidConfiguration := range config.Liquids {
		areaMapping[serviceType] = liquidConfiguration.Area
	}
	return ServiceInfoSnapshot{
		services:    make(servicesByType),
		resources:   make(resourcesByNameType),
		azResources: make(azResourcesByAZNameType),
		rates:       make(ratesByNameType),
		categories:  make(categoriesByID),
		areaMapping: areaMapping,
	}
}

///////////////////////////////////////////////////////////////////////

// FilteredServiceInfoSnapshot is a ServiceInfoSnapshot
// filtered by the specification of the ServiceInfoFilter.
// It offers the same method-set as ServiceInfoSnapshot.
type FilteredServiceInfoSnapshot struct {
	snapshot ServiceInfoSnapshot
	filter   ServiceInfoFilter
}

// GetFilteredService returns the service for the filtered type.
// It is useful to access the service, when you know it was filtered.
func (f FilteredServiceInfoSnapshot) GetFilteredService() (db.Service, bool) {
	serviceType, ok := f.filter.ServiceType.Unpack()
	if !ok {
		return db.Service{}, false
	}
	service, ok := f.snapshot.services[serviceType]
	return service, ok
}

// GetFilteredResource returns the resource for the filtered type and name.
// It is useful to access the resource, when you know it was filtered.
func (f FilteredServiceInfoSnapshot) GetFilteredResource() (db.Resource, bool) {
	serviceType, ok := f.filter.ServiceType.Unpack()
	if !ok {
		return db.Resource{}, false
	}
	resourceName, ok := f.filter.ResourceName.Unpack()
	if !ok {
		return db.Resource{}, false
	}
	resource, ok := f.snapshot.resources[serviceType][resourceName]
	return resource, ok
}

// GetFilteredRate returns the rate for the filtered type and name.
// It is useful to access the rate, when you know it was filtered.
func (f FilteredServiceInfoSnapshot) GetFilteredRate() (db.Rate, bool) {
	serviceType, ok := f.filter.ServiceType.Unpack()
	if !ok {
		return db.Rate{}, false
	}
	rateName, ok := f.filter.RateName.Unpack()
	if !ok {
		return db.Rate{}, false
	}
	rate, ok := f.snapshot.rates[serviceType][rateName]
	return rate, ok
}

// GetServices implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetServices() servicesByType {
	return f.snapshot.GetServices()
}

// GetServiceForType implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetServiceForType(serviceType db.ServiceType) (db.Service, bool) {
	return f.snapshot.GetServiceForType(serviceType)
}

// GetResources implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetResources() resourcesByNameType {
	return f.snapshot.GetResources()
}

// GetResourcesForType implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetResourcesForType(serviceType db.ServiceType) (resourcesByName, bool) {
	return f.snapshot.GetResourcesForType(serviceType)
}

// GetResourceForPath implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetResourceForPath(path db.ResourcePath) (db.Resource, bool) {
	return f.snapshot.GetResourceForPath(path)
}

// GetAZResources implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetAZResources() azResourcesByAZNameType {
	return f.snapshot.GetAZResources()
}

// GetAZResourcesForType implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetAZResourcesForType(serviceType db.ServiceType) (azResourcesByAZName, bool) {
	return f.snapshot.GetAZResourcesForType(serviceType)
}

// GetAZResourcesForPath implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetAZResourcesForPath(path db.ResourcePath) (azResourcesByAZ, bool) {
	return f.snapshot.GetAZResourcesForPath(path)
}

// GetAZResourceForPath implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetAZResourceForPath(path db.AZResourcePath) (db.AZResource, bool) {
	return f.snapshot.GetAZResourceForPath(path)
}

// GetRates implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetRates() ratesByNameType {
	return f.snapshot.GetRates()
}

// GetRatesForType implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetRatesForType(serviceType db.ServiceType) (ratesByName, bool) {
	return f.snapshot.GetRatesForType(serviceType)
}

// GetRateForPath implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetRateForPath(path db.RatePath) (db.Rate, bool) {
	return f.snapshot.GetRateForPath(path)
}

// GetCategories implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetCategories() categoriesByID {
	return f.snapshot.GetCategories()
}

// GetCategoryForID implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetCategoryForID(categoryID db.CategoryID) (db.Category, bool) {
	return f.snapshot.GetCategoryForID(categoryID)
}

///////////////////////////////////////////////////////////////////////

// serviceNotifyChannel is the PostgreSQL NOTIFY channel name used to signal
// that a service's data has changed and the cache should be invalidated.
const serviceNotifyChannel = "limitas_service_update"

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
	config   ClusterConfiguration
	data     ServiceInfoSnapshot
	// we use one mutex as all data is written together and reading is quick
	dataMutex sync.RWMutex

	// OnInvalidate is an optional channel that receives a signal after each
	// successful cache invalidation triggered by pg-notify. Used in tests to
	// synchronize with the asynchronous notification mechanism. The send is
	// non-blocking so production code is unaffected when nobody reads.
	OnInvalidate   <-chan struct{}
	sendInvalidate chan<- struct{}
}

// NewServiceInfoCache generates a ServiceInfoCache and fills all services' data.
func NewServiceInfoCache(dbm *gorp.DbMap, config ClusterConfiguration, dbURL Option[url.URL]) (*ServiceInfoCache, error) {
	sic := &ServiceInfoCache{
		DB:     dbm,
		config: config,
		data:   newEmptyServiceInfoSnapshot(config),
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
		s.data = s.data.removeDataForType(st)
	} else {
		s.data = newEmptyServiceInfoSnapshot(s.config)
	}

	// now we fill the cache for the invalidated services again
	servicesByType, err := db.BuildIndexOfDBResult(s.DB, func(s db.Service) db.ServiceType { return s.Type }, "SELECT * FROM services WHERE type = $1 OR $1 IS NULL", serviceType)
	if err != nil {
		return fmt.Errorf("while reading services for type(s) %v: %w", serviceType, err)
	}
	maps.Copy(s.data.services, servicesByType)

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
		if _, sExists := s.data.resources[path.ServiceType]; !sExists {
			s.data.resources[path.ServiceType] = make(resourcesByName)
		}
		s.data.resources[path.ServiceType][path.ResourceName] = resource
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
		if _, sExists := s.data.azResources[path.ServiceType]; !sExists {
			s.data.azResources[path.ServiceType] = make(azResourcesByAZName)
		}
		if _, rExists := s.data.azResources[path.ServiceType][path.ResourceName]; !rExists {
			s.data.azResources[path.ServiceType][path.ResourceName] = make(azResourcesByAZ)
		}
		s.data.azResources[path.ServiceType][path.ResourceName][path.AvailabilityZone] = azResource
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
		if _, rExists := s.data.rates[path.ServiceType]; !rExists {
			s.data.rates[path.ServiceType] = make(ratesByName)
		}
		s.data.rates[path.ServiceType][path.RateName] = rate
	}

	s.data.categories, err = db.BuildIndexOfDBResult(
		s.DB,
		func(c db.Category) db.CategoryID { return c.ID },
		"SELECT * FROM categories",
	)
	if err != nil {
		return fmt.Errorf("while reading categories: %w", err)
	}
	return nil
}

// GetSnapshot returns a ServiceInfoSnapshot with the current data in the ServiceInfoCache.
func (s *ServiceInfoCache) GetSnapshot() ServiceInfoSnapshot {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	return s.data.deepClone()
}

// GetServiceInfo should only be used when interacting with the liquid
// where the data of ServiceInfoCache needs to be available in the form of
// liquid.ServiceInfo!
func (s *ServiceInfoCache) GetServiceInfo(serviceType db.ServiceType) (info liquid.ServiceInfo, err error) {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	// we can assume the data is saved because of the call context
	service := s.data.services[serviceType]
	resources := s.data.resources[serviceType]
	rates := s.data.rates[serviceType]
	categories := s.data.categories

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

///////////////////////////////////////////////////////////////////////
// Utility functions

// deepCloneMap returns a deep copy of a map by cloning each value using the
// provided cloneValue function. For leaf maps (where values are not maps),
// pass maps.Clone directly. For nested maps, pass a function that itself
// calls deepCloneMap to deepClone the next level.
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
