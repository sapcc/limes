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
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/pkg/core"
	"github.com/sapcc/limes/pkg/db"
)

// ValidateProjectServices ensures that all required ProjectService records for
// this project exist (and none other). It also marks all services as stale
// where quota values contradict the project's quota constraints.
//
// It returns the full set of project services.
func ValidateProjectServices(tx *gorp.Transaction, cluster *core.Cluster, domain db.Domain, project db.Project) ([]db.ProjectService, error) {
	//list existing records
	seen := make(map[string]bool)
	var services []db.ProjectService
	_, err := tx.Select(&services,
		`SELECT * FROM project_services WHERE project_id = $1 ORDER BY type`, project.ID)
	if err != nil {
		return nil, err
	}
	logg.Debug("checking consistency for %d project services in project %s...", len(services), project.UUID)

	var constraints core.QuotaConstraints
	if cluster.QuotaConstraints != nil {
		constraints = cluster.QuotaConstraints.Projects[domain.Name][project.Name]
	}

	for _, srv := range services {
		//cleanup entries for services that have been removed from the configuration
		seen[srv.Type] = true
		if !cluster.HasService(srv.Type) {
			logg.Info("cleaning up %s service entry for project %s/%s", srv.Type, domain.Name, project.Name)
			_, err := tx.Delete(&srv) //nolint:gosec // Delete is not holding onto the pointer after it returns
			if err != nil {
				return nil, err
			}
			continue
		}

		//valid service -> check whether the existing quota values violate any constraints
		compliant, err := checkProjectResourcesAgainstConstraint(tx, cluster, srv, constraints[srv.Type])
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
			_, err = tx.UpdateColumns(onlyStale, &srv) //nolint:gosec // UpdateColumns is not holding onto the pointer after it returns
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

		plugin := cluster.QuotaPlugins[serviceType]
		if plugin == nil {
			//defense in depth: cluster.ServiceTypes should be consistent with cluster.QuotaPlugins
			return nil, fmt.Errorf("no quota plugin registered for service type %s", serviceType)
		}

		logg.Info("creating %s service entry for project %s/%s", serviceType, domain.Name, project.Name)
		srv := db.ProjectService{
			ProjectID: project.ID,
			Type:      serviceType,
		}
		err := tx.Insert(&srv)
		if err != nil {
			return nil, err
		}
		services = append(services, srv)

		//ensure deterministic ordering of resources (useful for tests)
		resources := plugin.Resources()
		sort.Slice(resources, func(i, j int) bool {
			return resources[i].Name < resources[j].Name
		})

		//initialize project quotas from constraints or quota distribution config if necessary
		for _, resInfo := range resources {
			if resInfo.NoQuota {
				continue
			}

			qdConfig := cluster.QuotaDistributionConfigForResource(serviceType, resInfo.Name)
			constraint := constraints[serviceType][resInfo.Name]
			initialQuota := constraint.ApplyTo(qdConfig.InitialProjectQuota())
			if initialQuota == 0 {
				continue
			}

			res := db.ProjectResource{
				ServiceID: srv.ID,
				Name:      resInfo.Name,
				Quota:     &initialQuota,
				//Scrape() will fill in the remaining backend attributes, and it will
				//also write the quotas into the backend.
			}
			zeroBackendQuota := int64(0)
			res.BackendQuota = &zeroBackendQuota
			if project.HasBursting {
				behavior := cluster.BehaviorForResource(serviceType, resInfo.Name, domain.Name+"/"+project.Name)
				desiredBackendQuota := behavior.MaxBurstMultiplier.ApplyTo(*res.Quota, qdConfig.Model)
				res.DesiredBackendQuota = &desiredBackendQuota
			} else {
				res.DesiredBackendQuota = res.Quota
			}

			err := tx.Insert(&res)
			if err != nil {
				return nil, err
			}
		}
	}

	return services, nil
}

func checkProjectResourcesAgainstConstraint(tx *gorp.Transaction, cluster *core.Cluster, srv db.ProjectService, serviceConstraints map[string]core.QuotaConstraint) (ok bool, err error) {
	//do not hit the database if there are no constraints to check
	if len(serviceConstraints) == 0 && len(cluster.Config.QuotaDistributionConfigs) == 0 {
		return true, nil
	}

	//ensure deterministic ordering of resources (useful for tests)
	plugin := cluster.QuotaPlugins[srv.Type]
	if plugin == nil {
		return false, fmt.Errorf("no quota plugin registered for service type %s", srv.Type)
	}
	resInfos := plugin.Resources()
	sort.Slice(resInfos, func(i, j int) bool {
		return resInfos[i].Name < resInfos[j].Name
	})

	var resources []db.ProjectResource
	_, err = tx.Select(&resources,
		`SELECT * FROM project_resources WHERE service_id = $1 ORDER BY name`, srv.ID)
	if err != nil {
		return false, err
	}
	quotaForResource := make(map[string]*uint64)
	for _, res := range resources {
		quotaForResource[res.Name] = res.Quota
	}

	for _, resInfo := range resInfos {
		if resInfo.NoQuota {
			continue
		}
		currentQuota := uint64(0)
		if quota := quotaForResource[resInfo.Name]; quota != nil {
			currentQuota = *quota
		}

		constraint := serviceConstraints[resInfo.Name]
		if constraint.Validate(currentQuota) != nil {
			return false, nil
		}

		qdConfig := cluster.QuotaDistributionConfigForResource(srv.Type, resInfo.Name)
		if qdConfig.Model == limesresources.CentralizedQuotaDistribution && currentQuota == 0 {
			//we did not get the initial default quota allocation
			return false, nil
		}
	}
	return true, nil
}
