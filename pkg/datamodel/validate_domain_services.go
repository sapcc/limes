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
	"fmt"

	"github.com/go-gorp/gorp/v3"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
)

// ValidateDomainServices ensures that all required DomainService and
// DomainResource records for this domain exist (and none other).
func ValidateDomainServices(tx *gorp.Transaction, cluster *core.Cluster, domain db.Domain) error {
	//list existing services
	seen := make(map[string]bool)
	var services []db.DomainService
	_, err := tx.Select(&services,
		`SELECT * FROM domain_services WHERE domain_id = $1 ORDER BY type`, domain.ID)
	if err != nil {
		return err
	}
	logg.Info("checking consistency for %d domain services in domain %s...", len(services), domain.UUID)

	for _, srv := range services {
		//cleanup entries for services that have been removed from the configuration
		seen[srv.Type] = true
		if !cluster.HasService(srv.Type) {
			logg.Info("cleaning up %s service entry for domain %s", srv.Type, domain.Name)
			_, err := tx.Delete(&srv) //nolint:gosec // Delete is not holding onto the pointer after it returns
			if err != nil {
				return err
			}
			continue
		}

		//check domain_resources in this service
		err := convergeResourcesInDomainService(tx, cluster, domain, srv)
		if err != nil {
			return err
		}
	}

	//create missing domain_services
	for _, serviceType := range cluster.ServiceTypesInAlphabeticalOrder() {
		if seen[serviceType] {
			continue
		}
		logg.Info("creating %s service entry for domain %s", serviceType, domain.Name)
		srv := db.DomainService{
			DomainID: domain.ID,
			Type:     serviceType,
		}
		err := tx.Insert(&srv)
		if err != nil {
			return err
		}

		//create domain_resources in this service
		err = convergeResourcesInDomainService(tx, cluster, domain, srv)
		if err != nil {
			return err
		}
	}

	return nil
}

func convergeResourcesInDomainService(tx *gorp.Transaction, cluster *core.Cluster, domain db.Domain, srv db.DomainService) error {
	plugin := cluster.QuotaPlugins[srv.Type]
	if plugin == nil {
		return fmt.Errorf("no quota plugin registered for service type %s", srv.Type)
	}

	var serviceConstraints map[string]core.QuotaConstraint
	if cluster.QuotaConstraints != nil {
		serviceConstraints = cluster.QuotaConstraints.Domains[domain.Name][srv.Type]
	}

	//list existing resources
	seen := make(map[string]bool)
	var resources []db.DomainResource
	_, err := tx.Select(&resources,
		`SELECT * FROM domain_resources WHERE service_id = $1`, srv.ID)
	if err != nil {
		return err
	}

	//check existing resources
	hasChanges := false
	for _, res := range resources {
		resInfo := cluster.InfoForResource(srv.Type, res.Name)
		seen[res.Name] = true

		//enforce quota constraints
		constraint := serviceConstraints[res.Name]
		if newQuota := constraint.ApplyTo(res.Quota); newQuota != res.Quota {
			logg.Other("AUDIT", "changing %s/%s quota for domain %s from %s to %s to satisfy constraint %q",
				srv.Type, res.Name, domain.Name,
				limes.ValueWithUnit{Value: res.Quota, Unit: resInfo.Unit},
				limes.ValueWithUnit{Value: newQuota, Unit: resInfo.Unit},
				constraint.String(),
			)
			_, err := tx.Exec(`UPDATE domain_resources SET quota = $1 WHERE service_id = $2 AND name = $3`,
				newQuota, srv.ID, res.Name)
			if err != nil {
				return err
			}
			hasChanges = true
		}
	}

	//create missing resources
	for _, resInfo := range plugin.Resources() {
		if seen[resInfo.Name] {
			continue
		}

		constraint := serviceConstraints[resInfo.Name]
		initialQuota := constraint.ApplyTo(0)
		if initialQuota != 0 {
			logg.Other("AUDIT", "initializing %s/%s quota for domain %s to %s to satisfy constraint %q",
				srv.Type, resInfo.Name, domain.Name,
				limes.ValueWithUnit{Value: initialQuota, Unit: resInfo.Unit},
				constraint.String(),
			)
		}

		err := tx.Insert(&db.DomainResource{
			ServiceID: srv.ID,
			Name:      resInfo.Name,
			Quota:     initialQuota,
		})
		if err != nil {
			return err
		}
		hasChanges = true
	}

	//if we created or updated any domain_resources, we need to ApplyComputedDomainQuota
	if !hasChanges {
		return nil
	}
	return ApplyComputedDomainQuota(tx, cluster, domain.ID, srv.Type)
}
