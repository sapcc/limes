// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/lib/pq"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"

	"go.xyrillian.de/gg/gsql"
	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/gg/pgruntime"
	"go.xyrillian.de/oblast"
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

// isEmpty returns whether all optional filters are not set.
func (s ServiceInfoFilter) isEmpty() bool {
	return s.ServiceArea.IsNone() && s.ServiceType.IsNone() && s.Category.IsNone() && s.ResourceName.IsNone() && s.RateName.IsNone()
}

///////////////////////////////////////////////////////////////////////

// ServiceInfoReader defines shared methods for reading filtered or unfiltered ServiceInfoSnapshots.
//
// It is implemented by types [ServiceInfoSnapshot] and [FilteredServiceInfoSnapshot].
type ServiceInfoReader interface {
	// GetServices returns all services.
	GetServices() util.ConstMap[db.ServiceType, db.Service]
	// GetServiceForType returns the service for the given service type.
	GetServiceForType(serviceType db.ServiceType) (db.Service, bool)
	// GetServiceForID returns the service for the given service ID.
	GetServiceForID(id db.ServiceID) (db.Service, bool)
	// GetResourcesForType returns all resources for the given service type.
	GetResourcesForType(serviceType db.ServiceType) util.ConstMap[liquid.ResourceName, db.Resource]
	// GetResourceForPath returns the resource for the given path.
	GetResourceForPath(path db.ResourcePath) (db.Resource, bool)
	// GetResourceForID returns the resource for the given resource ID.
	GetResourceForID(id db.ResourceID) (db.Resource, bool)
	// GetAZResourcesForPath returns all AZ resources for the given ResourcePath.
	GetAZResourcesForPath(path db.ResourcePath) util.ConstMap[limes.AvailabilityZone, db.AZResource]
	// GetAZResourceForPath returns the AZ resource for the given AZResourcePath.
	GetAZResourceForPath(path db.AZResourcePath) (db.AZResource, bool)
	// GetAZResourceForID returns the AZ resource for the given AZ resource ID.
	GetAZResourceForID(id db.AZResourceID) (db.AZResource, bool)
	// GetRatesForType returns all rates for the given service type.
	GetRatesForType(serviceType db.ServiceType) util.ConstMap[liquid.RateName, db.Rate]
	// GetRateForPath returns the rate for the given service type and rate name.
	GetRateForPath(path db.RatePath) (db.Rate, bool)
	// GetRateForID returns the rate for the given rate ID.
	GetRateForID(id db.RateID) (db.Rate, bool)
	// GetCategoriesForType returns all categories for the given service type.
	GetCategoriesForType(serviceType db.ServiceType) util.ConstMap[db.CategoryID, db.Category]
	// GetCategoryForID returns the category for the given ID.
	GetCategoryForID(categoryID db.CategoryID) (db.Category, bool)
}

var (
	// prove interface implementations
	_ ServiceInfoReader = ServiceInfoSnapshot{}
	_ ServiceInfoReader = FilteredServiceInfoSnapshot{}
)

///////////////////////////////////////////////////////////////////////

// ServiceInfoSnapshot is the combined representation of db.Service, db.Resource
// db.AZResource, db.Rate and db.Category. We chose to provide this as only
// data structure and output of ServiceInfoCache mainly for 2 reasons:
// This ensures we get one consistent state from the ServiceInfoCache and
// not multiple outputs where the cache might have changed in between.
// Also, we can express filtering all entities by the same ServiceInfoFilter
// which makes application of the same filter to all entities easier.
//
// All internal fields are util.ConstMap (read-only), which means that
// ServiceInfoSnapshot can be shared across goroutines without deep cloning.
type ServiceInfoSnapshot struct {
	services    util.ConstMap[db.ServiceType, db.Service]
	resources   util.ConstMap[db.ServiceType, util.ConstMap[liquid.ResourceName, db.Resource]]
	azResources util.ConstMap[db.ServiceType, util.ConstMap[liquid.ResourceName, util.ConstMap[limes.AvailabilityZone, db.AZResource]]]
	rates       util.ConstMap[db.ServiceType, util.ConstMap[liquid.RateName, db.Rate]]
	categories  util.ConstMap[db.ServiceType, util.ConstMap[db.CategoryID, db.Category]]
	// ID-based indexes for O(1) lookup by primary key
	servicesByID    util.ConstMap[db.ServiceID, db.Service]
	resourcesByID   util.ConstMap[db.ResourceID, db.Resource]
	azResourcesByID util.ConstMap[db.AZResourceID, db.AZResource]
	ratesByID       util.ConstMap[db.RateID, db.Rate]
	categoriesByID  util.ConstMap[db.CategoryID, db.Category]
	// necessary for constructing filters by area
	areaMapping util.ConstMap[db.ServiceType, string]
}

// Filter applies the filter to the ServiceInfoSnapshot and produces an
// eagerly filtered FilteredServiceInfoSnapshot using a copy-on-filter approach.
// Only entries that pass the filter are copied into the new snapshot.
func (s ServiceInfoSnapshot) Filter(filter ServiceInfoFilter) FilteredServiceInfoSnapshot {
	// Phase 1: determine service types which survive area and type filter
	survivingServiceTypes := make(map[db.ServiceType]struct{})
	for serviceType := range s.services.All() {
		if area, ok := filter.ServiceArea.Unpack(); ok {
			if s.areaMapping.GetOrZero(serviceType) != area {
				continue
			}
		}
		if typeFilter, ok := filter.ServiceType.Unpack(); ok {
			if serviceType != typeFilter {
				continue
			}
		}
		survivingServiceTypes[serviceType] = struct{}{}
	}

	// Phase 2: determine categories to remove, when filtering by category
	categoriesToRemove := make(map[db.CategoryID]struct{})
	categoryFilter, categoryFilterExists := filter.Category.Unpack()
	if categoryFilterExists {
		for serviceType := range survivingServiceTypes {
			categories := s.categories.GetOrZero(serviceType)
			for categoryID, info := range categories.All() {
				if info.Name != categoryFilter {
					categoriesToRemove[categoryID] = struct{}{}
				}
			}
		}
	}

	// Phase 3: build filtered resources and az_resources
	resourceNameFilter, resourceFilterExists := filter.ResourceName.Unpack()
	seenCategories := make(map[db.CategoryID]struct{})
	newResources := make(map[db.ServiceType]util.ConstMap[liquid.ResourceName, db.Resource], len(survivingServiceTypes))
	newAZResources := make(map[db.ServiceType]util.ConstMap[liquid.ResourceName, util.ConstMap[limes.AvailabilityZone, db.AZResource]], len(survivingServiceTypes))

	for serviceType := range survivingServiceTypes {
		resByName := s.resources.GetOrZero(serviceType)
		azResByName := s.azResources.GetOrZero(serviceType)
		filteredRes := make(map[liquid.ResourceName]db.Resource)
		filteredAZRes := make(map[liquid.ResourceName]util.ConstMap[limes.AvailabilityZone, db.AZResource])

		for resourceName, resource := range resByName.All() {
			_, inRemoveSet := categoriesToRemove[resource.CategoryID]
			if resourceFilterExists && resourceName != resourceNameFilter {
				continue
			}
			if categoryFilterExists && inRemoveSet {
				continue
			}
			filteredRes[resourceName] = resource
			seenCategories[resource.CategoryID] = struct{}{}
			if azByAZ, ok := azResByName.Get(resourceName); ok {
				filteredAZRes[resourceName] = azByAZ
			}
		}
		if len(filteredRes) > 0 {
			newResources[serviceType] = util.NewConstMap(filteredRes)
		}
		if len(filteredAZRes) > 0 {
			newAZResources[serviceType] = util.NewConstMap(filteredAZRes)
		}
	}

	// Phase 4: build filtered rates
	rateNameFilter, rateFilterExists := filter.RateName.Unpack()
	newRates := make(map[db.ServiceType]util.ConstMap[liquid.RateName, db.Rate], len(survivingServiceTypes))

	for serviceType := range survivingServiceTypes {
		rateByName := s.rates.GetOrZero(serviceType)
		filteredRates := make(map[liquid.RateName]db.Rate)

		for rateName, rate := range rateByName.All() {
			_, inRemoveSet := categoriesToRemove[rate.CategoryID]
			if rateFilterExists && rateName != rateNameFilter {
				continue
			}
			if categoryFilterExists && inRemoveSet {
				continue
			}
			filteredRates[rateName] = rate
			seenCategories[rate.CategoryID] = struct{}{}
		}
		if len(filteredRates) > 0 {
			newRates[serviceType] = util.NewConstMap(filteredRates)
		}
	}

	// Phase 5: remove empty service types
	newServices := make(map[db.ServiceType]db.Service, len(survivingServiceTypes))
	for serviceType := range survivingServiceTypes {
		if newResources[serviceType].Len() == 0 && newRates[serviceType].Len() == 0 {
			delete(newResources, serviceType)
			delete(newAZResources, serviceType)
			delete(newRates, serviceType)
			continue
		}
		svc, _ := s.services.Get(serviceType)
		newServices[serviceType] = svc
	}

	// Phase 6: build filtered categories (using seenCategories).
	newCategories := make(map[db.ServiceType]util.ConstMap[db.CategoryID, db.Category])
	for serviceType := range newServices {
		cats := s.categories.GetOrZero(serviceType)
		filteredCats := make(map[db.CategoryID]db.Category)
		for categoryID, cat := range cats.All() {
			if _, ok := seenCategories[categoryID]; ok {
				filteredCats[categoryID] = cat
			}
		}
		if len(filteredCats) > 0 {
			newCategories[serviceType] = util.NewConstMap(filteredCats)
		}
	}

	// Phase 7: build ID indexes
	newServicesByID := make(map[db.ServiceID]db.Service, len(newServices))
	for _, svc := range newServices {
		newServicesByID[svc.ID] = svc
	}
	newResourcesByID := make(map[db.ResourceID]db.Resource)
	for _, resByName := range newResources {
		for _, resource := range resByName.All() {
			newResourcesByID[resource.ID] = resource
		}
	}
	newAZResourcesByID := make(map[db.AZResourceID]db.AZResource)
	for _, azResByName := range newAZResources {
		for _, azResByAZ := range azResByName.All() {
			for _, azRes := range azResByAZ.All() {
				newAZResourcesByID[azRes.ID] = azRes
			}
		}
	}
	newRatesByID := make(map[db.RateID]db.Rate)
	for _, rateByName := range newRates {
		for _, rate := range rateByName.All() {
			newRatesByID[rate.ID] = rate
		}
	}
	newCategoriesByID := make(map[db.CategoryID]db.Category)
	for _, catByID := range newCategories {
		for _, cat := range catByID.All() {
			newCategoriesByID[cat.ID] = cat
		}
	}

	return FilteredServiceInfoSnapshot{
		snapshot: ServiceInfoSnapshot{
			services:        util.NewConstMap(newServices),
			resources:       util.NewConstMap(newResources),
			azResources:     util.NewConstMap(newAZResources),
			rates:           util.NewConstMap(newRates),
			categories:      util.NewConstMap(newCategories),
			servicesByID:    util.NewConstMap(newServicesByID),
			resourcesByID:   util.NewConstMap(newResourcesByID),
			azResourcesByID: util.NewConstMap(newAZResourcesByID),
			ratesByID:       util.NewConstMap(newRatesByID),
			categoriesByID:  util.NewConstMap(newCategoriesByID),
			areaMapping:     s.areaMapping, // shared, never modified
		},
		filter: filter,
	}
}

// GetServices implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetServices() util.ConstMap[db.ServiceType, db.Service] {
	return s.services
}

// GetServiceForType implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetServiceForType(serviceType db.ServiceType) (db.Service, bool) {
	return s.services.Get(serviceType)
}

// GetServiceForID implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetServiceForID(id db.ServiceID) (db.Service, bool) {
	return s.servicesByID.Get(id)
}

// GetResourcesForType implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetResourcesForType(serviceType db.ServiceType) util.ConstMap[liquid.ResourceName, db.Resource] {
	return s.resources.GetOrZero(serviceType)
}

// GetResourceForPath implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetResourceForPath(path db.ResourcePath) (db.Resource, bool) {
	return s.resources.GetOrZero(path.ServiceType).Get(path.ResourceName)
}

// GetResourceForID implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetResourceForID(id db.ResourceID) (db.Resource, bool) {
	return s.resourcesByID.Get(id)
}

// GetAZResourcesForPath implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetAZResourcesForPath(path db.ResourcePath) util.ConstMap[limes.AvailabilityZone, db.AZResource] {
	return s.azResources.GetOrZero(path.ServiceType).GetOrZero(path.ResourceName)
}

// GetAZResourceForPath implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetAZResourceForPath(path db.AZResourcePath) (db.AZResource, bool) {
	return s.azResources.GetOrZero(path.ServiceType).GetOrZero(path.ResourceName).Get(path.AvailabilityZone)
}

// GetAZResourceForID implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetAZResourceForID(id db.AZResourceID) (db.AZResource, bool) {
	return s.azResourcesByID.Get(id)
}

// GetRatesForType implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetRatesForType(serviceType db.ServiceType) util.ConstMap[liquid.RateName, db.Rate] {
	return s.rates.GetOrZero(serviceType)
}

// GetRateForPath implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetRateForPath(path db.RatePath) (db.Rate, bool) {
	return s.rates.GetOrZero(path.ServiceType).Get(path.RateName)
}

// GetRateForID implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetRateForID(id db.RateID) (db.Rate, bool) {
	return s.ratesByID.Get(id)
}

// GetCategoriesForType implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetCategoriesForType(serviceType db.ServiceType) util.ConstMap[db.CategoryID, db.Category] {
	return s.categories.GetOrZero(serviceType)
}

// GetCategoryForID implements the [ServiceInfoReader] interface.
func (s ServiceInfoSnapshot) GetCategoryForID(categoryID db.CategoryID) (db.Category, bool) {
	return s.categoriesByID.Get(categoryID)
}

///////////////////////////////////////////////////////////////////////

// FilteredServiceInfoSnapshot is a ServiceInfoSnapshot
// filtered by the specification of the ServiceInfoFilter.
// It offers the same method-set as ServiceInfoSnapshot.
type FilteredServiceInfoSnapshot struct {
	snapshot ServiceInfoSnapshot
	filter   ServiceInfoFilter
}

// FilterIsEmpty returns whether all optional filters are empty.
// As the FilteredServiceInfoSnapshot get's re-used for applying parsed API
// options to the ServiceInfoSnapshot, the filter can also be empty.
func (f FilteredServiceInfoSnapshot) FilterIsEmpty() bool {
	return f.filter.isEmpty()
}

// GetServices implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetServices() util.ConstMap[db.ServiceType, db.Service] {
	return f.snapshot.GetServices()
}

// GetServiceForType implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetServiceForType(serviceType db.ServiceType) (db.Service, bool) {
	return f.snapshot.GetServiceForType(serviceType)
}

// GetServiceForID implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetServiceForID(id db.ServiceID) (db.Service, bool) {
	return f.snapshot.GetServiceForID(id)
}

// GetResourcesForType implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetResourcesForType(serviceType db.ServiceType) util.ConstMap[liquid.ResourceName, db.Resource] {
	return f.snapshot.GetResourcesForType(serviceType)
}

// GetResourceForPath implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetResourceForPath(path db.ResourcePath) (db.Resource, bool) {
	return f.snapshot.GetResourceForPath(path)
}

// GetResourceForID implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetResourceForID(id db.ResourceID) (db.Resource, bool) {
	return f.snapshot.GetResourceForID(id)
}

// GetAZResourcesForPath implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetAZResourcesForPath(path db.ResourcePath) util.ConstMap[limes.AvailabilityZone, db.AZResource] {
	return f.snapshot.GetAZResourcesForPath(path)
}

// GetAZResourceForPath implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetAZResourceForPath(path db.AZResourcePath) (db.AZResource, bool) {
	return f.snapshot.GetAZResourceForPath(path)
}

// GetAZResourceForID implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetAZResourceForID(id db.AZResourceID) (db.AZResource, bool) {
	return f.snapshot.GetAZResourceForID(id)
}

// GetRatesForType implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetRatesForType(serviceType db.ServiceType) util.ConstMap[liquid.RateName, db.Rate] {
	return f.snapshot.GetRatesForType(serviceType)
}

// GetRateForPath implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetRateForPath(path db.RatePath) (db.Rate, bool) {
	return f.snapshot.GetRateForPath(path)
}

// GetRateForID implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetRateForID(id db.RateID) (db.Rate, bool) {
	return f.snapshot.GetRateForID(id)
}

// GetCategoriesForType implements the [ServiceInfoReader] interface.
func (f FilteredServiceInfoSnapshot) GetCategoriesForType(serviceType db.ServiceType) util.ConstMap[db.CategoryID, db.Category] {
	return f.snapshot.GetCategoriesForType(serviceType)
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
	DB       *gsql.DB
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
func NewServiceInfoCache(ctx context.Context, dbm *gsql.DB, config ClusterConfiguration, connTarget Option[pgruntime.ConnectionTarget]) (*ServiceInfoCache, error) {
	sic := &ServiceInfoCache{
		DB:     dbm,
		config: config,
	}

	// populate all data from the DB on startup
	err := sic.InvalidateService(ctx, None[db.ServiceType]())
	if err != nil {
		return nil, err
	}

	// set up NOTIFY listener if a DB URL was provided (disabled in tests)
	if t, ok := connTarget.Unpack(); ok {
		u, err := t.IntoURL()
		if err != nil {
			return nil, err
		}
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
		go sic.listenForInvalidations(ctx)
	}

	return sic, nil
}

// listenForInvalidations waits for NOTIFY messages on serviceNotifyChannel.
// The payload is expected to be the service type string. On reconnect, a nil
// notification is sent by pq — in that case we invalidate all services to be safe.
func (s *ServiceInfoCache) listenForInvalidations(ctx context.Context) {
	for notification := range s.listener.Notify {
		if notification == nil {
			// connection was re-established; we may have missed notifications, so we invalidate all
			logg.Info("SIC pg listener reconnected, reloading all services")
			err := s.InvalidateService(ctx, None[db.ServiceType]())
			if err != nil {
				logg.Fatal("SIC failed to reload all services after reconnect: %s", err.Error())
			}
			s.signalInvalidation()
			continue
		}

		serviceType := db.ServiceType(notification.Extra)
		logg.Info("SIC invalidating service %q due to pg NOTIFY", serviceType)
		err := s.InvalidateService(ctx, Some(serviceType))
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
// It rebuilds the internal util.ConstMap fields from scratch for the affected
// services so that the existing references to those maps are not affected.
func (s *ServiceInfoCache) InvalidateService(ctx context.Context, serviceType Option[db.ServiceType]) error {
	s.dataMutex.Lock()
	defer s.dataMutex.Unlock()

	// build area mapping (constant, derived from config)
	areaMappingPlain := make(map[db.ServiceType]string, len(s.config.Liquids))
	for st, liquidConfiguration := range s.config.Liquids {
		areaMappingPlain[st] = liquidConfiguration.Area
	}

	// we start with empty maps, get the data we want from the database and then possibly copy over the old values
	services := make(map[db.ServiceType]db.Service)
	resources := make(map[db.ServiceType]util.ConstMap[liquid.ResourceName, db.Resource])
	azResources := make(map[db.ServiceType]util.ConstMap[liquid.ResourceName, util.ConstMap[liquid.AvailabilityZone, db.AZResource]])
	rates := make(map[db.ServiceType]util.ConstMap[liquid.RateName, db.Rate])
	categories := make(map[db.ServiceType]util.ConstMap[db.CategoryID, db.Category])

	// load fresh data for affected services
	dbServices, err := db.ServiceStore.SelectWhere(ctx, s.DB, `type = $1 OR $1 IS NULL`, serviceType).Collect()
	if err != nil {
		return fmt.Errorf("while reading services for type(s) %v: %w", serviceType, err)
	}
	serviceIDs := make([]db.ServiceID, 0, len(dbServices))
	for _, dbService := range dbServices {
		services[dbService.Type] = dbService
		serviceIDs = append(serviceIDs, dbService.ID)
	}

	dbResources, err := db.ResourceStore.Select(ctx, s.DB, `SELECT r.* FROM resources r WHERE r.service_id = ANY($1) OR CARDINALITY($1) = 0 ORDER BY path`, pq.Array(serviceIDs)).Collect()
	if err != nil {
		return fmt.Errorf("while reading resources for type(s) %v: %w", serviceType, err)
	}
	var (
		currentService               db.ServiceType
		resourcesForCurrentService   map[liquid.ResourceName]db.Resource
		azResourcesForCurrentService map[liquid.ResourceName]map[liquid.AvailabilityZone]db.AZResource
		ratesForCurrentService       map[liquid.RateName]db.Rate
		categoriesForCurrentService  map[db.CategoryID]db.Category
	)
	currentService = ""
	for _, dbResource := range dbResources {
		path := dbResource.Path
		if currentService != path.ServiceType {
			// flush
			resources[currentService] = util.NewConstMap(resourcesForCurrentService)
			resourcesForCurrentService = make(map[liquid.ResourceName]db.Resource)
			currentService = path.ServiceType
		}
		resourcesForCurrentService[path.ResourceName] = dbResource
	}
	resources[currentService] = util.NewConstMap(resourcesForCurrentService)

	dbAZResources, err := db.AZResourceStore.Select(ctx, s.DB, `SELECT azr.* FROM az_resources azr JOIN resources r ON azr.resource_id = r.id WHERE r.service_id = ANY($1) OR CARDINALITY($1) = 0 ORDER BY path`, pq.Array(serviceIDs)).Collect()
	if err != nil {
		return fmt.Errorf("while reading az_resources for type(s) %v: %w", serviceType, err)
	}
	currentService = ""
	for _, dbAZResource := range dbAZResources {
		path := dbAZResource.Path
		if currentService != path.ServiceType {
			// flush
			azResources[currentService] = util.New2LevelConstMap(azResourcesForCurrentService)
			azResourcesForCurrentService = make(map[liquid.ResourceName]map[liquid.AvailabilityZone]db.AZResource)
			currentService = path.ServiceType
		}
		if azResourcesForCurrentService[path.ResourceName] == nil {
			azResourcesForCurrentService[path.ResourceName] = make(map[liquid.AvailabilityZone]db.AZResource)
		}
		azResourcesForCurrentService[path.ResourceName][path.AvailabilityZone] = dbAZResource
	}
	azResources[currentService] = util.New2LevelConstMap(azResourcesForCurrentService)

	dbRates, err := db.RateStore.Select(ctx, s.DB, "SELECT ra.* FROM rates ra WHERE ra.service_id = ANY($1) OR CARDINALITY($1) = 0 ORDER BY path", pq.Array(serviceIDs)).Collect()
	if err != nil {
		return fmt.Errorf("while reading rates for type(s) %v: %w", serviceType, err)
	}
	currentService = ""
	for _, dbRate := range dbRates {
		path := dbRate.Path
		if currentService != path.ServiceType {
			// flush
			rates[currentService] = util.NewConstMap(ratesForCurrentService)
			ratesForCurrentService = make(map[liquid.RateName]db.Rate)
			currentService = path.ServiceType
		}
		ratesForCurrentService[path.RateName] = dbRate
	}
	rates[currentService] = util.NewConstMap(ratesForCurrentService)

	type categoryRecord struct {
		db.Category
		ServiceType db.ServiceType `db:"service_type"`
	}
	categoryRecords, err := oblast.MustNewStore[categoryRecord](oblast.PostgresDialect()).Select(ctx, s.DB,
		`SELECT c.*, s.type AS service_type FROM categories c JOIN services s ON c.service_id = s.id WHERE s.id = ANY($1) OR CARDINALITY($1) = 0 ORDER BY s.type`, pq.Array(serviceIDs),
	).Collect()
	if err != nil {
		return fmt.Errorf("while reading categories for type(s) %v: %w", serviceType, err)
	}
	currentService = ""
	for _, record := range categoryRecords {
		if currentService != record.ServiceType {
			// flush
			categories[currentService] = util.NewConstMap(categoriesForCurrentService)
			categoriesForCurrentService = make(map[db.CategoryID]db.Category)
			currentService = record.ServiceType
		}
		categoriesForCurrentService[record.ID] = record.Category
	}
	categories[currentService] = util.NewConstMap(categoriesForCurrentService)

	// copy unchanged entries, if there are any
	if stFilter, ok := serviceType.Unpack(); ok {
		for st, oldService := range s.data.services.All() {
			if st == stFilter {
				continue
			}
			services[st] = oldService
			resources[st], _ = s.data.resources.Get(st)
			azResources[st], _ = s.data.azResources.Get(st)
			rates[st], _ = s.data.rates.Get(st)
			categories[st], _ = s.data.categories.Get(st)
		}
	}

	// Build ID indexes.
	servicesByID := make(map[db.ServiceID]db.Service, len(services))
	for _, svc := range services {
		servicesByID[svc.ID] = svc
	}
	resourcesByID := make(map[db.ResourceID]db.Resource)
	for _, resByName := range resources {
		for resource := range resByName.Values() {
			resourcesByID[resource.ID] = resource
		}
	}
	azResourcesByID := make(map[db.AZResourceID]db.AZResource)
	for _, azResByName := range azResources {
		for azResByAZ := range azResByName.Values() {
			for azRes := range azResByAZ.Values() {
				azResourcesByID[azRes.ID] = azRes
			}
		}
	}
	ratesByID := make(map[db.RateID]db.Rate)
	for _, rateByName := range rates {
		for rate := range rateByName.Values() {
			ratesByID[rate.ID] = rate
		}
	}
	categoriesByID := make(map[db.CategoryID]db.Category)
	for _, catByID := range categories {
		for cat := range catByID.Values() {
			categoriesByID[cat.ID] = cat
		}
	}

	// Wrap nested maps and assign the new snapshot atomically.
	s.data = ServiceInfoSnapshot{
		services:        util.NewConstMap(services),
		resources:       util.NewConstMap(resources),
		azResources:     util.NewConstMap(azResources),
		rates:           util.NewConstMap(rates),
		categories:      util.NewConstMap(categories),
		servicesByID:    util.NewConstMap(servicesByID),
		resourcesByID:   util.NewConstMap(resourcesByID),
		azResourcesByID: util.NewConstMap(azResourcesByID),
		ratesByID:       util.NewConstMap(ratesByID),
		categoriesByID:  util.NewConstMap(categoriesByID),
		areaMapping:     util.NewConstMap(areaMappingPlain),
	}

	return nil
}

// GetSnapshot returns a ServiceInfoSnapshot with the current data in the ServiceInfoCache.
// This is a cheap O(1) operation because all internal fields are immutable util.ConstMap values.
func (s *ServiceInfoCache) GetSnapshot() ServiceInfoSnapshot {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	return s.data
}

// GetServiceInfo should only be used when interacting with the liquid
// where the data of ServiceInfoCache needs to be available in the form of
// liquid.ServiceInfo!
func (s *ServiceInfoCache) GetServiceInfo(serviceType db.ServiceType) (info liquid.ServiceInfo, err error) {
	s.dataMutex.RLock()
	defer s.dataMutex.RUnlock()
	// we can assume the data is saved because of the call context
	service := s.data.services.GetOrZero(serviceType)
	resources := s.data.resources.GetOrZero(serviceType)
	rates := s.data.rates.GetOrZero(serviceType)
	categories := s.data.categoriesByID

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
		Resources:                              make(map[liquid.ResourceName]liquid.ResourceInfo, resources.Len()),
		Rates:                                  make(map[liquid.RateName]liquid.RateInfo, rates.Len()),
		Categories:                             make(map[liquid.CategoryName]liquid.CategoryInfo),
		CapacityMetricFamilies:                 capacityMetricFamilies,
		UsageMetricFamilies:                    usageMetricFamilies,
	}
	// reconstruct resource infos
	for name, res := range resources.All() {
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
		if cat, exists := categories.Get(res.CategoryID); exists {
			resInfo.Category = Some(cat.Name)
			info.Categories[cat.Name] = liquid.CategoryInfo{
				DisplayName: cat.DisplayName,
			}
		}
		info.Resources[name] = resInfo
	}
	// reconstruct rate infos
	for name, rate := range rates.All() {
		rateInfo := liquid.RateInfo{
			DisplayName: rate.DisplayName,
			Unit:        rate.Unit,
			Topology:    rate.Topology,
			HasUsage:    rate.HasUsage,
		}
		if cat, exists := categories.Get(rate.CategoryID); exists {
			rateInfo.Category = Some(cat.Name)
			info.Categories[cat.Name] = liquid.CategoryInfo{
				DisplayName: cat.DisplayName,
			}
		}
		info.Rates[name] = rateInfo
	}
	return info, nil
}
