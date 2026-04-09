// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
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

	. "github.com/majewsky/gg/option"
)

// serviceNotifyChannel is the PostgreSQL NOTIFY channel name used to signal
// that a service's data has changed and the cache should be invalidated.
const serviceNotifyChannel = "limes_service_update"

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
	DB                               *gorp.DbMap
	listener                         *pq.Listener
	servicesByTypeMutex              sync.RWMutex
	resourcesByNameByTypeMutex       sync.RWMutex
	azResourcesByAZByNameByTypeMutex sync.RWMutex
	ratesByNameByTypeMutex           sync.RWMutex
	categoriesByIDMutex              sync.RWMutex

	// OnInvalidate is an optional channel that receives a signal after each
	// successful cache invalidation triggered by pg-notify. Used in tests to
	// synchronize with the asynchronous notification mechanism. The send is
	// non-blocking so production code is unaffected when nobody reads.
	OnInvalidate   <-chan struct{}
	sendInvalidate chan<- struct{}

	// data
	servicesByType              map[db.ServiceType]db.Service
	resourcesByNameByType       map[db.ServiceType]map[liquid.ResourceName]db.Resource
	azResourcesByAZByNameByType map[db.ServiceType]map[liquid.ResourceName]map[limes.AvailabilityZone]db.AZResource
	ratesByNameByType           map[db.ServiceType]map[liquid.RateName]db.Rate
	categoriesByID              map[db.CategoryID]db.Category
}

// NewServiceInfoCache generates a ServiceInfoCache and fills all services' data.
func NewServiceInfoCache(dbm *gorp.DbMap, dbURL Option[url.URL]) (*ServiceInfoCache, error) {
	sic := &ServiceInfoCache{
		DB: dbm,

		servicesByType:              make(map[db.ServiceType]db.Service),
		resourcesByNameByType:       make(map[db.ServiceType]map[liquid.ResourceName]db.Resource),
		azResourcesByAZByNameByType: make(map[db.ServiceType]map[liquid.ResourceName]map[limes.AvailabilityZone]db.AZResource),
		ratesByNameByType:           make(map[db.ServiceType]map[liquid.RateName]db.Rate),
		categoriesByID:              make(map[db.CategoryID]db.Category),
	}

	// populate all data from the DB on startup
	err := sic.fillData()
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
					logg.Error("ServiceInfoCache pg listener event %d: %s", ev, err.Error())
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
			logg.Info("ServiceInfoCache pg listener reconnected, reloading all services")
			err := s.invalidateServices()
			if err != nil {
				logg.Fatal("ServiceInfoCache failed to reload all services after reconnect: %s", err.Error())
			}
			s.signalInvalidation()
			continue
		}

		serviceType := db.ServiceType(notification.Extra)
		logg.Info("ServiceInfoCache invalidating service %q due to pg NOTIFY", serviceType)
		err := s.invalidateServices(serviceType)
		if err != nil {
			logg.Fatal("ServiceInfoCache failed to reload service %q: %s", serviceType, err.Error())
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

func (s *ServiceInfoCache) invalidateServices(serviceTypes ...db.ServiceType) error {
	s.servicesByTypeMutex.Lock()
	defer s.servicesByTypeMutex.Unlock()
	s.resourcesByNameByTypeMutex.Lock()
	defer s.resourcesByNameByTypeMutex.Unlock()
	s.azResourcesByAZByNameByTypeMutex.Lock()
	defer s.azResourcesByAZByNameByTypeMutex.Unlock()
	s.ratesByNameByTypeMutex.Lock()
	defer s.ratesByNameByTypeMutex.Unlock()
	s.categoriesByIDMutex.Lock()
	defer s.categoriesByIDMutex.Unlock()

	if len(serviceTypes) == 0 {
		s.servicesByType = make(map[db.ServiceType]db.Service)
		s.resourcesByNameByType = make(map[db.ServiceType]map[liquid.ResourceName]db.Resource)
		s.azResourcesByAZByNameByType = make(map[db.ServiceType]map[liquid.ResourceName]map[limes.AvailabilityZone]db.AZResource)
		s.ratesByNameByType = make(map[db.ServiceType]map[liquid.RateName]db.Rate)
		s.categoriesByID = make(map[db.CategoryID]db.Category)
	} else {
		for _, serviceType := range serviceTypes {
			delete(s.servicesByType, serviceType)
			delete(s.resourcesByNameByType, serviceType)
			delete(s.azResourcesByAZByNameByType, serviceType)
			delete(s.ratesByNameByType, serviceType)
		}
		s.categoriesByID = make(map[db.CategoryID]db.Category)
	}

	// now we fill the cache for the invalidated services again
	err := s.fillData(serviceTypes...)
	if err != nil {
		return err
	}
	return nil
}

// fillData requires a write lock on all the variables holding data, if not called on init!
// Also, it requires all maps of the variables holding data to be initialized on the first level.
// It loads all service, resource, az_resource, rate, and category data for the requested serviceTypes.
func (s *ServiceInfoCache) fillData(serviceTypes ...db.ServiceType) error {
	servicesByType, err := db.BuildIndexOfDBResult(s.DB, func(s db.Service) db.ServiceType { return s.Type }, "SELECT * FROM services WHERE type = ANY($1) OR $1 IS NULL", pq.Array(serviceTypes))
	if err != nil {
		return err
	}
	maps.Copy(s.servicesByType, servicesByType)

	resourcesByPath, err := db.BuildIndexOfDBResult(
		s.DB,
		func(r db.Resource) db.ResourcePath { return db.ResourcePath(r.Path) },
		"SELECT r.* FROM resources r JOIN services s ON r.service_id = s.id WHERE s.type = ANY($1) OR $1 IS NULL",
		pq.Array(serviceTypes),
	)
	if err != nil {
		return err
	}
	for path, resource := range resourcesByPath {
		if _, sExists := s.resourcesByNameByType[path.ServiceType()]; !sExists {
			s.resourcesByNameByType[path.ServiceType()] = make(map[liquid.ResourceName]db.Resource)
		}
		s.resourcesByNameByType[path.ServiceType()][path.ResourceName()] = resource
	}

	azResourcesByPath, err := db.BuildIndexOfDBResult(
		s.DB,
		func(a db.AZResource) db.AZResourcePath { return db.AZResourcePath(a.Path) },
		"SELECT azr.* FROM az_resources azr JOIN resources r ON azr.resource_id = r.id JOIN services s ON r.service_id = s.id WHERE s.type = ANY($1) OR $1 IS NULL",
		pq.Array(serviceTypes),
	)
	if err != nil {
		return err
	}
	for path, azResource := range azResourcesByPath {
		if _, sExists := s.azResourcesByAZByNameByType[path.ServiceType()]; !sExists {
			s.azResourcesByAZByNameByType[path.ServiceType()] = make(map[liquid.ResourceName]map[limes.AvailabilityZone]db.AZResource)
		}
		if _, rExists := s.azResourcesByAZByNameByType[path.ServiceType()][path.ResourceName()]; !rExists {
			s.azResourcesByAZByNameByType[path.ServiceType()][path.ResourceName()] = make(map[limes.AvailabilityZone]db.AZResource)
		}
		s.azResourcesByAZByNameByType[path.ServiceType()][path.ResourceName()][path.AvailabilityZone()] = azResource
	}

	ratesByPath, err := db.BuildIndexOfDBResult(
		s.DB,
		func(r db.Rate) db.RatePath { return db.RatePath(r.Path) },
		"SELECT ra.* FROM rates ra JOIN services s ON ra.service_id = s.id WHERE s.type = ANY($1) OR $1 IS NULL",
		pq.Array(serviceTypes),
	)
	if err != nil {
		return err
	}
	for path, rate := range ratesByPath {
		if _, rExists := s.ratesByNameByType[path.ServiceType()]; !rExists {
			s.ratesByNameByType[path.ServiceType()] = make(map[liquid.RateName]db.Rate)
		}
		s.ratesByNameByType[path.ServiceType()][path.RateName()] = rate
	}

	categoriesByID, err := db.BuildIndexOfDBResult(
		s.DB,
		func(c db.Category) db.CategoryID { return c.ID },
		"SELECT * FROM categories",
	)
	if err != nil {
		return err
	}
	maps.Copy(s.categoriesByID, categoriesByID)
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
func (s *ServiceInfoCache) GetServices() map[db.ServiceType]db.Service {
	s.servicesByTypeMutex.RLock()
	defer s.servicesByTypeMutex.RUnlock()
	return maps.Clone(s.servicesByType)
}

// GetServiceForType returns the cached service for the given service type.
func (s *ServiceInfoCache) GetServiceForType(serviceType db.ServiceType) db.Service {
	s.servicesByTypeMutex.RLock()
	defer s.servicesByTypeMutex.RUnlock()
	return s.servicesByType[serviceType]
}

// GetResources returns all cached resources, indexed by service type and resource name.
func (s *ServiceInfoCache) GetResources() map[db.ServiceType]map[liquid.ResourceName]db.Resource {
	s.resourcesByNameByTypeMutex.RLock()
	defer s.resourcesByNameByTypeMutex.RUnlock()
	return deepCloneMap(s.resourcesByNameByType, maps.Clone)
}

// GetResourcesForType returns all cached resources for the given service type, keyed by resource name.
func (s *ServiceInfoCache) GetResourcesForType(serviceType db.ServiceType) map[liquid.ResourceName]db.Resource {
	s.resourcesByNameByTypeMutex.RLock()
	defer s.resourcesByNameByTypeMutex.RUnlock()
	return maps.Clone(s.resourcesByNameByType[serviceType])
}

// GetResourceForTypeName returns the cached resource for the given service type and resource name.
func (s *ServiceInfoCache) GetResourceForTypeName(serviceType db.ServiceType, resourceName liquid.ResourceName) db.Resource {
	s.resourcesByNameByTypeMutex.RLock()
	defer s.resourcesByNameByTypeMutex.RUnlock()
	return s.resourcesByNameByType[serviceType][resourceName]
}

// GetAZResources returns all cached AZ resources, indexed by service type, resource name, and availability zone.
func (s *ServiceInfoCache) GetAZResources() map[db.ServiceType]map[liquid.ResourceName]map[limes.AvailabilityZone]db.AZResource {
	s.azResourcesByAZByNameByTypeMutex.RLock()
	defer s.azResourcesByAZByNameByTypeMutex.RUnlock()
	return deepCloneMap(s.azResourcesByAZByNameByType, func(inner map[liquid.ResourceName]map[limes.AvailabilityZone]db.AZResource) map[liquid.ResourceName]map[limes.AvailabilityZone]db.AZResource {
		return deepCloneMap(inner, maps.Clone)
	})
}

// GetAZResourcesForType returns all cached AZ resources for the given service type, keyed by resource name and availability zone.
func (s *ServiceInfoCache) GetAZResourcesForType(serviceType db.ServiceType) map[liquid.ResourceName]map[limes.AvailabilityZone]db.AZResource {
	s.azResourcesByAZByNameByTypeMutex.RLock()
	defer s.azResourcesByAZByNameByTypeMutex.RUnlock()
	return deepCloneMap(s.azResourcesByAZByNameByType[serviceType], maps.Clone)
}

// GetAZResourcesForTypeName returns all cached AZ resources for the given service type, resource name, keyed by availability zone.
func (s *ServiceInfoCache) GetAZResourcesForTypeName(serviceType db.ServiceType, resourceName liquid.ResourceName) map[limes.AvailabilityZone]db.AZResource {
	s.azResourcesByAZByNameByTypeMutex.RLock()
	defer s.azResourcesByAZByNameByTypeMutex.RUnlock()
	return maps.Clone(s.azResourcesByAZByNameByType[serviceType][resourceName])
}

// GetAZResourceForTypeNameAZ returns the cached AZ resource for the given service type, resource name, and availability zone.
func (s *ServiceInfoCache) GetAZResourceForTypeNameAZ(serviceType db.ServiceType, resourceName liquid.ResourceName, az limes.AvailabilityZone) db.AZResource {
	s.azResourcesByAZByNameByTypeMutex.RLock()
	defer s.azResourcesByAZByNameByTypeMutex.RUnlock()
	return s.azResourcesByAZByNameByType[serviceType][resourceName][az]
}

// GetRates returns all cached rates, indexed by service type and rate name.
func (s *ServiceInfoCache) GetRates() map[db.ServiceType]map[liquid.RateName]db.Rate {
	s.ratesByNameByTypeMutex.RLock()
	defer s.ratesByNameByTypeMutex.RUnlock()
	return deepCloneMap(s.ratesByNameByType, maps.Clone)
}

// GetRatesForType returns all cached rates, indexed by service type and rate name.
func (s *ServiceInfoCache) GetRatesForType(serviceType db.ServiceType) map[liquid.RateName]db.Rate {
	s.ratesByNameByTypeMutex.RLock()
	defer s.ratesByNameByTypeMutex.RUnlock()
	return maps.Clone(s.ratesByNameByType[serviceType])
}

// GetRateForTypeName returns the cached rate for the given service type and rate name.
func (s *ServiceInfoCache) GetRateForTypeName(serviceType db.ServiceType, rateName liquid.RateName) db.Rate {
	s.ratesByNameByTypeMutex.RLock()
	defer s.ratesByNameByTypeMutex.RUnlock()
	return s.ratesByNameByType[serviceType][rateName]
}

// GetCategories returns all cached categories, indexed by category ID.
func (s *ServiceInfoCache) GetCategories() map[db.CategoryID]db.Category {
	s.categoriesByIDMutex.RLock()
	defer s.categoriesByIDMutex.RUnlock()
	return maps.Clone(s.categoriesByID)
}

// GetCategoryForID returns the cached category for the given category ID.
func (s *ServiceInfoCache) GetCategoryForID(categoryID db.CategoryID) db.Category {
	s.categoriesByIDMutex.RLock()
	defer s.categoriesByIDMutex.RUnlock()
	return s.categoriesByID[categoryID]
}
