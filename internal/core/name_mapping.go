/*******************************************************************************
*
* Copyright 2024 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package core

import (
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/db"
)

// NameMapping contains an efficient pre-computed mapping between API-level and
// DB-level service/resource/rate identifiers.
type NameMapping struct {
	cluster        *Cluster
	resFromAPIToDB map[ResourceRef]dbResourceRef
	resFromDBToAPI map[dbResourceRef]ResourceRef
}

type dbResourceRef struct {
	ServiceType  db.ServiceType
	ResourceName liquid.ResourceName
}

// BuildNameMapping constructs a new NameMapping instance.
func BuildNameMapping(cluster *Cluster) NameMapping {
	nm := NameMapping{
		cluster:        cluster,
		resFromAPIToDB: make(map[ResourceRef]dbResourceRef),
		resFromDBToAPI: make(map[dbResourceRef]ResourceRef),
	}
	for dbServiceType, quotaPlugin := range cluster.QuotaPlugins {
		for dbResourceName := range quotaPlugin.Resources() {
			dbRef := dbResourceRef{dbServiceType, dbResourceName}
			apiRef := cluster.BehaviorForResource(dbServiceType, dbResourceName).IdentityInV1API
			nm.resFromAPIToDB[apiRef] = dbRef
			nm.resFromDBToAPI[dbRef] = apiRef
		}
	}
	return nm
}

// MapResourceFromV1API maps API-level identifiers for a resource into DB-level identifiers.
// False is returned if the given resource does not exist.
func (nm NameMapping) MapResourceFromV1API(serviceType limes.ServiceType, resourceName limesresources.ResourceName) (db.ServiceType, liquid.ResourceName, bool) {
	ref, ok := nm.resFromAPIToDB[ResourceRef{serviceType, resourceName}]
	if !ok {
		return "", "", false
	}
	return ref.ServiceType, ref.ResourceName, true
}

// MapResourceToV1API maps DB-level identifiers for a resource into API-level identifiers.
// False is returned if the given resource does not exist.
func (nm NameMapping) MapResourceToV1API(serviceType db.ServiceType, resourceName liquid.ResourceName) (limes.ServiceType, limesresources.ResourceName, bool) {
	ref, ok := nm.resFromDBToAPI[dbResourceRef{serviceType, resourceName}]
	if !ok {
		return "", "", false
	}
	return ref.ServiceType, ref.ResourceName, true
}

// MapRateFromV1API maps API-level identifiers for a rate into DB-level identifiers.
func (nm NameMapping) MapRateFromV1API(serviceType limes.ServiceType, rateName limesrates.RateName) (db.ServiceType, db.RateName) {
	// NOTE: This is the only place where these particular cross-type casts are allowed.
	//
	// The implementation is currently boring because we don't support renaming
	// here yet, but by funneling all such conversions through here, it is easy
	// to add in the future.
	return db.ServiceType(serviceType), db.RateName(rateName)
}

// MapRateToV1API maps API-level identifiers for a rate into DB-level identifiers.
func (nm NameMapping) MapRateToV1API(serviceType db.ServiceType, rateName db.RateName) (limes.ServiceType, limesrates.RateName) {
	// NOTE: This is the only place where these particular cross-type casts are allowed.
	//
	// The implementation is currently boring because we don't support renaming
	// here yet, but by funneling all such conversions through here, it is easy
	// to add in the future.
	return limes.ServiceType(serviceType), limesrates.RateName(rateName)
}
