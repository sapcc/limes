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

//ValidateDomainServices ensures that all required DomainService records for
//this domain exist (and none other). It returns the full set of domain services.
func (s Scope) ValidateDomainServices(domain db.Domain) ([]db.DomainService, error) {
	//list existing records
	seen := make(map[string]bool)
	var services []db.DomainService
	_, err := s.Tx.Select(&services,
		`SELECT * FROM domain_services WHERE domain_id = $1 ORDER BY type`, domain.ID)
	if err != nil {
		return nil, err
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
			return nil, err
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
		s.logAutomaticAction("creating %s service entry for domain %s", serviceType, domain.Name)
		srv := db.DomainService{
			DomainID: domain.ID,
			Type:     serviceType,
		}
		err := s.Tx.Insert(&srv)
		if err != nil {
			return nil, err
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
					return nil, err
				}
			}
		}
	}

	return services, nil
}
