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
	"github.com/sapcc/limes/pkg/util"
	gorp "gopkg.in/gorp.v2"
)

//ValidateDomainServices ensures that all required DomainService records for
//this domain exist (and none other). It returns the full set of domain services.
func ValidateDomainServices(tx *gorp.Transaction, cluster *limes.Cluster, domain db.Domain) ([]db.DomainService, error) {
	//list existing records
	seen := make(map[string]bool)
	var services []db.DomainService
	_, err := tx.Select(&services,
		`SELECT * FROM domain_services WHERE domain_id = $1 ORDER BY type`, domain.ID)
	if err != nil {
		return nil, err
	}

	var constraints limes.QuotaConstraints
	if cluster.QuotaConstraints != nil {
		constraints = cluster.QuotaConstraints.Domains[domain.Name]
	}

	for _, srv := range services {
		//cleanup entries for services that have been removed from the configuration
		seen[srv.Type] = true
		if !cluster.HasService(srv.Type) {
			util.LogInfo("cleaning up %s service entry for domain %s", srv.Type, domain.Name)
			_, err := tx.Delete(&srv)
			if err != nil {
				return nil, err
			}
			continue
		}

		//valid service -> check whether the existing quota values violate any constraints
		err := checkDomainServiceConstraints(tx, cluster, domain, srv, constraints[srv.Type])
		if err != nil {
			return nil, err
		}
	}

	//create missing service entries
	for _, serviceType := range cluster.ServiceTypes {
		if seen[serviceType] {
			continue
		}
		util.LogInfo("creating %s service entry for domain %s", serviceType, domain.Name)
		srv := db.DomainService{
			DomainID: domain.ID,
			Type:     serviceType,
		}
		err := tx.Insert(&srv)
		if err != nil {
			return nil, err
		}
		services = append(services, srv)

		err = createMissingDomainResources(tx, cluster, domain, srv, constraints[serviceType], nil)
		if err != nil {
			return nil, err
		}
	}

	return services, nil
}

func checkDomainServiceConstraints(tx *gorp.Transaction, cluster *limes.Cluster, domain db.Domain, srv db.DomainService, serviceConstraints map[string]limes.QuotaConstraint) error {
	//do not hit the database if there are no constraints to check
	if len(serviceConstraints) == 0 {
		return nil
	}

	var resources []db.DomainResource
	_, err := tx.Select(&resources,
		`SELECT * FROM domain_resources WHERE service_id = $1 ORDER BY name`, srv.ID)
	if err != nil {
		return err
	}

	//check existing domain_resources for any quota values that violate constraints
	seen := make(map[string]bool)
	var resourcesToUpdate []interface{}
	for _, res := range resources {
		seen[res.Name] = true

		constraint := serviceConstraints[res.Name]
		if newQuota := constraint.ApplyTo(res.Quota); newQuota != res.Quota {
			resInfo := cluster.InfoForResource(srv.Type, res.Name)
			util.LogInfo("changing %s/%s quota for domain %s from %s to %s to satisfy constraint %q",
				srv.Type, res.Name, domain.Name,
				limes.ValueWithUnit{Value: res.Quota, Unit: resInfo.Unit},
				limes.ValueWithUnit{Value: newQuota, Unit: resInfo.Unit},
				constraint.ToString(resInfo.Unit),
			)

			//take a copy of the loop variable (it will be updated by the loop, so if
			//we didn't take a copy manually, the resourcesToUpdate list would
			//contain only identical pointers)
			res := res

			res.Quota = newQuota
			resourcesToUpdate = append(resourcesToUpdate, &res)
		}

		if constraint.Expected != nil && *constraint.Expected != res.Quota {
			unit := cluster.InfoForResource(srv.Type, res.Name).Unit
			util.LogError(`expectation mismatch: %s/%s quota for domain %s should be %s, but is %s`,
				srv.Type, res.Name, domain.Name,
				limes.ValueWithUnit{Value: *constraint.Expected, Unit: unit},
				limes.ValueWithUnit{Value: res.Quota, Unit: unit},
			)
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

	//create any missing domain resources where there are "at least/exactly/should be" constraints
	return createMissingDomainResources(tx, cluster, domain, srv, serviceConstraints, seen)
}

func createMissingDomainResources(tx *gorp.Transaction, cluster *limes.Cluster, domain db.Domain, srv db.DomainService, serviceConstraints map[string]limes.QuotaConstraint, resourceExists map[string]bool) error {
	//do not hit the database if there are no constraints to check
	if len(serviceConstraints) == 0 {
		return nil
	}

	//ensure deterministic ordering of resources (useful for tests)
	resourceNames := make([]string, 0, len(serviceConstraints))
	for resourceName, constraint := range serviceConstraints {
		//initialize domain quotas where constraints require a non-zero quota value
		if constraint.InitialQuotaValue() != 0 && !resourceExists[resourceName] {
			resourceNames = append(resourceNames, resourceName)
		}
	}
	sort.Strings(resourceNames)

	for _, resourceName := range resourceNames {
		resInfo := cluster.InfoForResource(srv.Type, resourceName)
		constraint := serviceConstraints[resourceName]
		newQuota := constraint.InitialQuotaValue()
		util.LogInfo("initializing %s/%s quota for domain %s to %s to satisfy constraint %q",
			srv.Type, resourceName, domain.Name,
			limes.ValueWithUnit{Value: newQuota, Unit: resInfo.Unit},
			constraint.ToString(resInfo.Unit),
		)

		err := tx.Insert(&db.DomainResource{
			ServiceID: srv.ID,
			Name:      resourceName,
			Quota:     newQuota,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
