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

//ValidateProjectServices ensures that all required ProjectService records for
//this project exist (and none other). It also marks all services as stale
//where quota values contradict the project's quota constraints.
//
//It returns the full set of project services.
func ValidateProjectServices(tx *gorp.Transaction, cluster *limes.Cluster, domain db.Domain, project db.Project) ([]db.ProjectService, error) {
	//list existing records
	seen := make(map[string]bool)
	var services []db.ProjectService
	_, err := tx.Select(&services,
		`SELECT * FROM project_services WHERE project_id = $1 ORDER BY type`, project.ID)
	if err != nil {
		return nil, err
	}

	var constraints limes.QuotaConstraints
	if cluster.QuotaConstraints != nil {
		constraints = cluster.QuotaConstraints.Projects[domain.Name][project.Name]
	}

	for _, srv := range services {
		//cleanup entries for services that have been removed from the configuration
		seen[srv.Type] = true
		if !cluster.HasService(srv.Type) {
			util.LogInfo("cleaning up %s service entry for project %s/%s", srv.Type, domain.Name, project.Name)
			_, err := tx.Delete(&srv)
			if err != nil {
				return nil, err
			}
			continue
		}

		//valid service -> check whether the existing quota values violate any constraints
		compliant, err := checkProjectResourcesAgainstConstraint(tx, cluster, domain, project, srv, constraints[srv.Type])
		if err != nil {
			return nil, err
		}
		if !compliant {
			//Do not attempt to rectify these quota values right now; that's a ton of
			//logic that would need to be duplicated from Scrape(). Instead, set the
			//`stale` flag on the project_service which will cause Scrape() to pick
			//up this project_service at the next opportunity and do the work for us.
			srv.Stale = true
			onlyStale := func(c *gorp.ColumnMap) bool {
				return c.ColumnName == "stale"
			}
			_, err = tx.UpdateColumns(onlyStale, &srv)
			if err != nil {
				return nil, err
			}
		}
	}

	//create missing service entries
	for _, serviceType := range cluster.ServiceTypes {
		if seen[serviceType] {
			continue
		}
		util.LogInfo("creating %s service entry for project %s/%s", serviceType, domain.Name, project.Name)
		srv := db.ProjectService{
			ProjectID: project.ID,
			Type:      serviceType,
		}
		err := tx.Insert(&srv)
		if err != nil {
			return nil, err
		}
		services = append(services, srv)

		//initialize project quotas from constraints, if there is one
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
				err := tx.Insert(&db.ProjectResource{
					ServiceID: srv.ID,
					Name:      resourceName,
					Quota:     serviceConstraints[resourceName].InitialQuotaValue(),
					//Scrape() will fill in the remaining backend attributes, and it will
					//also write the quotas into the backend.
				})
				if err != nil {
					return nil, err
				}
			}
		}
	}

	return services, nil
}

func checkProjectResourcesAgainstConstraint(tx *gorp.Transaction, cluster *limes.Cluster, domain db.Domain, project db.Project, srv db.ProjectService, serviceConstraints map[string]limes.QuotaConstraint) (ok bool, err error) {
	//do not hit the database if there are no constraints to check
	if len(serviceConstraints) == 0 {
		return true, nil
	}

	var resources []db.ProjectResource
	_, err = tx.Select(&resources,
		`SELECT * FROM project_resources WHERE service_id = $1 ORDER BY name`, srv.ID)
	if err != nil {
		return false, err
	}

	ok = true
	for _, res := range resources {
		constraint := serviceConstraints[res.Name]
		if !constraint.Allows(res.Quota) {
			ok = false
		}

		if constraint.Expected != nil && *constraint.Expected != res.Quota {
			unit := cluster.InfoForResource(srv.Type, res.Name).Unit
			util.LogError(`expectation mismatch: %s/%s quota for project %s/%s should be %s, but is %s`,
				srv.Type, res.Name, domain.Name, project.Name,
				limes.ValueWithUnit{Value: *constraint.Expected, Unit: unit},
				limes.ValueWithUnit{Value: res.Quota, Unit: unit},
			)
		}
	}
	return ok, nil
}
