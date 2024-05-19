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
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// ValidateProjectServices ensures that all required ProjectService records for
// this project exist (and none other).
func ValidateProjectServices(dbi db.Interface, cluster *core.Cluster, domain db.Domain, project db.Project, now time.Time) error {
	// list existing records
	seen := make(map[limes.ServiceType]bool)
	var services []db.ProjectService
	_, err := dbi.Select(&services,
		`SELECT * FROM project_services WHERE project_id = $1 ORDER BY type`, project.ID)
	if err != nil {
		return err
	}
	logg.Debug("checking consistency for %d project services in project %s...", len(services), project.UUID)

	// cleanup entries for services that have been removed from the configuration
	for _, srv := range services {
		seen[srv.Type] = true
		if !cluster.HasService(srv.Type) {
			logg.Info("cleaning up %s service entry for project %s/%s", srv.Type, domain.Name, project.Name)
			_, err := dbi.Delete(&srv)
			if err != nil {
				return err
			}
			continue
		}
	}

	// create missing service entries
	for _, serviceType := range cluster.ServiceTypesInAlphabeticalOrder() {
		if seen[serviceType] {
			continue
		}

		logg.Info("creating %s service entry for project %s/%s", serviceType, domain.Name, project.Name)
		err := dbi.Insert(&db.ProjectService{
			ProjectID:         project.ID,
			Type:              serviceType,
			NextScrapeAt:      now,
			RatesNextScrapeAt: now,
		})
		if err != nil {
			return err
		}
	}

	return nil
}
