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
	"sort"

	"github.com/go-gorp/gorp/v3"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
)

// ValidateDomainServices ensures that all required DomainService records for
// this domain exist (and none other). It returns the full set of domain services.
func ValidateDomainServices(tx *gorp.Transaction, cluster *core.Cluster, domain db.Domain) ([]db.DomainService, error) {
	//list existing records
	seen := make(map[string]bool)
	var services []db.DomainService
	_, err := tx.Select(&services,
		`SELECT * FROM domain_services WHERE domain_id = $1 ORDER BY type`, domain.ID)
	if err != nil {
		return nil, err
	}
	logg.Info("checking consistency for %d domain services in domain %s...", len(services), domain.UUID)

	var constraints core.QuotaConstraints
	if cluster.QuotaConstraints != nil {
		constraints = cluster.QuotaConstraints.Domains[domain.Name]
	}

	for _, srv := range services {
		//cleanup entries for services that have been removed from the configuration
		seen[srv.Type] = true
		if !cluster.HasService(srv.Type) {
			logg.Info("cleaning up %s service entry for domain %s", srv.Type, domain.Name)
			_, err := tx.Delete(&srv) //nolint:gosec // Delete is not holding onto the pointer after it returns
			if err != nil {
				return nil, err
			}
			continue
		}

		//valid service -> check that all domain_resources entries exist, and that
		//their quota is consistent with configured constraints and computation
		//constraints
		err := createMissingDomainResources(tx, cluster, domain, srv, constraints[srv.Type])
		if err != nil {
			return nil, err
		}
		err = checkDomainServiceConstraints(tx, cluster, domain, srv, constraints[srv.Type])
		if err != nil {
			return nil, err
		}
		err = ApplyComputedDomainQuota(tx, cluster, domain.ID, srv.Type)
		if err != nil {
			return nil, err
		}
	}

	//create missing service entries
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
			return nil, err
		}
		services = append(services, srv)

		err = createMissingDomainResources(tx, cluster, domain, srv, constraints[serviceType])
		if err != nil {
			return nil, err
		}
	}

	return services, nil
}

func checkDomainServiceConstraints(tx *gorp.Transaction, cluster *core.Cluster, domain db.Domain, srv db.DomainService, serviceConstraints map[string]core.QuotaConstraint) error {
	//do not hit the database if there are no constraints to check
	if len(serviceConstraints) == 0 && len(cluster.Config.QuotaDistributionConfigs) == 0 {
		return nil
	}

	var resources []db.DomainResource
	_, err := tx.Select(&resources,
		`SELECT * FROM domain_resources WHERE service_id = $1 ORDER BY name`, srv.ID)
	if err != nil {
		return err
	}

	//check existing domain_resources for any quota values that violate constraints
	var resourcesToUpdate []interface{}
	for _, res := range resources {
		constraint := serviceConstraints[res.Name]
		if newQuota := constraint.ApplyTo(res.Quota); newQuota != res.Quota {
			resInfo := cluster.InfoForResource(srv.Type, res.Name)
			logg.Info("changing %s/%s quota for domain %s from %s to %s to satisfy constraint %q",
				srv.Type, res.Name, domain.Name,
				limes.ValueWithUnit{Value: res.Quota, Unit: resInfo.Unit},
				limes.ValueWithUnit{Value: newQuota, Unit: resInfo.Unit},
				constraint.String(),
			)

			//take a copy of the loop variable (it will be updated by the loop, so if
			//we didn't take a copy manually, the resourcesToUpdate list would
			//contain only identical pointers)
			res := res

			res.Quota = newQuota
			resourcesToUpdate = append(resourcesToUpdate, &res)
		}
	}
	if len(resourcesToUpdate) > 0 {
		onlyQuota := func(c *gorp.ColumnMap) bool {
			return c.ColumnName == "quota"
		}
		_, err = tx.UpdateColumns(onlyQuota, resourcesToUpdate...)
		if err != nil {
			return err
		}
	}
	return nil
}

func createMissingDomainResources(tx *gorp.Transaction, cluster *core.Cluster, domain db.Domain, srv db.DomainService, serviceConstraints map[string]core.QuotaConstraint) error {
	var dbResources []db.DomainResource
	_, err := tx.Select(&dbResources,
		`SELECT * FROM domain_resources WHERE service_id = $1 ORDER BY name`, srv.ID)
	if err != nil {
		return err
	}
	resourceExists := make(map[string]bool)
	for _, res := range dbResources {
		resourceExists[res.Name] = true
	}

	plugin := cluster.QuotaPlugins[srv.Type]
	if plugin == nil {
		return fmt.Errorf("no quota plugin registered for service type %s", srv.Type)
	}

	//ensure deterministic ordering of resources (useful for tests)
	resources := plugin.Resources()
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].Name < resources[j].Name
	})

	for _, resInfo := range resources {
		if resourceExists[resInfo.Name] {
			continue
		}

		constraint := serviceConstraints[resInfo.Name]
		initialQuota := constraint.ApplyTo(0)
		if initialQuota != 0 {
			logg.Info("initializing %s/%s quota for domain %s to %s to satisfy constraint %q",
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
	}
	return nil
}
