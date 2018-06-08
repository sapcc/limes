/*******************************************************************************
*
* Copyright 2018 SAP SE
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

package datamodel

import (
	"sort"

	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
)

//ForeachDomainService calls the supplied action once for each DomainService
//belonging to this Domain. If the action modifies the DomainService instance,
//it will be updated in the DB at the end. Returning an error from the action will
//interrupt the entire function.
func (s Scope) ForeachDomainService(domain db.Domain, action func(db.DomainService) error) error {
	//list existing records
	seen := make(map[string]bool)
	var services []db.DomainService
	_, err := s.Tx.Select(&services,
		`SELECT * FROM domain_services WHERE domain_id = $1 ORDER BY type`, domain.ID)
	if err != nil {
		return err
	}

	//cleanup entries for services that have been removed from the configuration
	for _, srv := range services {
		seen[srv.Type] = true
		if s.Cluster.HasService(srv.Type) {
			continue
		}
		s.logAutomaticAction("cleaning up %s service entry for domain %s", srv.Type, domain.Name)
		_, err := s.Tx.Delete(&srv)
		if err != nil {
			return err
		}
	}

	var constraints limes.QuotaConstraints
	if s.Cluster.QuotaConstraints != nil {
		constraints = s.Cluster.QuotaConstraints.Domains[domain.Name]
	}

	//create missing service entries
	for _, serviceType := range s.Cluster.ServiceTypes {
		if seen[serviceType] {
			continue
		}
		s.logAutomaticAction("creating missing %s service entry for domain %s", serviceType, domain.Name)
		srv := db.DomainService{
			DomainID: domain.ID,
			Type:     serviceType,
		}
		err := s.Tx.Insert(&srv)
		if err != nil {
			return err
		}
		services = append(services, srv)

		//initialize domain quotas from constraints, if there is one
		if serviceConstraints, exists := constraints[serviceType]; exists {
			//ensure deterministic ordering of resources (useful for tests)
			resourceNames := make([]string, 0, len(serviceConstraints))
			for resourceName, constraint := range serviceConstraints {
				if constraint.InitialQuotaValue() != 0 {
					resourceNames = append(resourceNames, resourceName)
				}
			}
			sort.Strings(resourceNames)

			for _, resourceName := range resourceNames {
				err := s.Tx.Insert(&db.DomainResource{
					ServiceID: srv.ID,
					Name:      resourceName,
					Quota:     serviceConstraints[resourceName].InitialQuotaValue(),
				})
				if err != nil {
					return err
				}
			}
		}
	}

	//execute the user-specific action on all services
	for _, srv := range services {
		err := action(srv)
		if err != nil {
			return err
		}
	}
	return nil
}

//CreateAndValidateDomainServices ensures that all required DomainService
//records for this domain exist (and none other).
func (s Scope) CreateAndValidateDomainServices(domain db.Domain) error {
	return s.ForeachDomainService(domain, func(db.DomainService) error { return nil })
}
