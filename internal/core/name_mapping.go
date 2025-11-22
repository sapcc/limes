// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"maps"
	"slices"

	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/db"
)

// ResourceNameMapping contains an efficient pre-computed mapping between
// API-level and DB-level service and resource identifiers.
type ResourceNameMapping struct {
	fromAPIToDB map[ResourceRef]dbResourceRef
	fromDBToAPI map[dbResourceRef]ResourceRef
}

type dbResourceRef struct {
	ServiceType  db.ServiceType
	ResourceName liquid.ResourceName
}

// RateNameMapping is like ResourceNameMapping, but for rates instead.
type RateNameMapping struct {
	cluster     *Cluster
	fromAPIToDB map[RateRef]dbRateRef
	fromDBToAPI map[dbRateRef]RateRef
}

type dbRateRef struct {
	ServiceType db.ServiceType
	RateName    liquid.RateName
}

// BuildResourceNameMapping constructs a new ResourceNameMapping instance.
func BuildResourceNameMapping(cluster *Cluster, serviceInfos map[db.ServiceType]liquid.ServiceInfo) ResourceNameMapping {
	nm := ResourceNameMapping{
		fromAPIToDB: make(map[ResourceRef]dbResourceRef),
		fromDBToAPI: make(map[dbResourceRef]ResourceRef),
	}
	for dbServiceType, serviceInfo := range serviceInfos {
		for dbResourceName := range serviceInfo.Resources {
			dbRef := dbResourceRef{dbServiceType, dbResourceName}
			apiRef := cluster.BehaviorForResource(dbServiceType, dbResourceName).IdentityInV1API
			nm.fromAPIToDB[apiRef] = dbRef
			nm.fromDBToAPI[dbRef] = apiRef
		}
	}
	return nm
}

// BuildRateNameMapping constructs a new RateNameMapping instance.
func BuildRateNameMapping(cluster *Cluster, serviceInfos map[db.ServiceType]liquid.ServiceInfo) RateNameMapping {
	nm := RateNameMapping{
		cluster:     cluster,
		fromAPIToDB: make(map[RateRef]dbRateRef),
		fromDBToAPI: make(map[dbRateRef]RateRef),
	}
	serviceTypes := slices.Sorted(maps.Keys(serviceInfos))
	for _, dbServiceType := range serviceTypes {
		dbRateNames := make(map[liquid.RateName]struct{})
		for dbRateName := range RatesForService(serviceInfos, dbServiceType) {
			dbRateNames[dbRateName] = struct{}{}
		}
		cfg, _ := cluster.Config.GetLiquidConfigurationForType(dbServiceType)
		for _, rateLimit := range cfg.RateLimits.Global {
			dbRateNames[rateLimit.Name] = struct{}{}
		}
		for _, rateLimit := range cfg.RateLimits.ProjectDefault {
			dbRateNames[rateLimit.Name] = struct{}{}
		}
		for dbRateName := range dbRateNames {
			dbRef := dbRateRef{dbServiceType, dbRateName}
			apiRef := cluster.BehaviorForRate(dbServiceType, dbRateName).IdentityInV1API
			nm.fromAPIToDB[apiRef] = dbRef
			nm.fromDBToAPI[dbRef] = apiRef
		}
	}
	return nm
}

// MapFromV1API maps API-level identifiers for a resource into DB-level identifiers.
// False is returned if the given resource does not exist.
func (nm ResourceNameMapping) MapFromV1API(serviceType limes.ServiceType, resourceName limesresources.ResourceName) (db.ServiceType, liquid.ResourceName, bool) {
	ref, ok := nm.fromAPIToDB[ResourceRef{ServiceType: serviceType, Name: resourceName}]
	if !ok {
		return "", "", false
	}
	return ref.ServiceType, ref.ResourceName, true
}

// MapToV1API maps DB-level identifiers for a resource into API-level identifiers.
// False is returned if the given resource does not exist.
func (nm ResourceNameMapping) MapToV1API(serviceType db.ServiceType, resourceName liquid.ResourceName) (limes.ServiceType, limesresources.ResourceName, bool) {
	ref, ok := nm.fromDBToAPI[dbResourceRef{serviceType, resourceName}]
	if !ok {
		return "", "", false
	}
	return ref.ServiceType, ref.Name, true
}

// MapFromV1API maps API-level identifiers for a rate into DB-level identifiers.
func (nm RateNameMapping) MapFromV1API(serviceType limes.ServiceType, rateName limesrates.RateName) (db.ServiceType, liquid.RateName, bool) {
	ref, ok := nm.fromAPIToDB[RateRef{ServiceType: serviceType, Name: rateName}]
	if !ok {
		return "", "", false
	}
	return ref.ServiceType, ref.RateName, true
}

// MapToV1API maps API-level identifiers for a rate into DB-level identifiers.
func (nm RateNameMapping) MapToV1API(serviceType db.ServiceType, rateName liquid.RateName) (limes.ServiceType, limesrates.RateName, bool) {
	ref, ok := nm.fromDBToAPI[dbRateRef{serviceType, rateName}]
	if !ok {
		return "", "", false
	}
	return ref.ServiceType, ref.Name, true
}
