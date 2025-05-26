// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"maps"
	"slices"
	"time"

	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// ValidateProjectServices ensures that all required ProjectService records for
// this project exist (and none other).
func ValidateProjectServices(dbi db.Interface, cluster *core.Cluster, domain db.Domain, project db.Project, now time.Time) error {
	// list existing records
	seen := make(map[db.ServiceType]bool)
	var services []db.ProjectService
	_, err := dbi.Select(&services,
		`SELECT * FROM project_services WHERE project_id = $1 ORDER BY type`, project.ID)
	if err != nil {
		return err
	}
	logg.Debug("checking consistency for %d project services in project %s...", len(services), project.UUID)

	// as this is called from the collect task, this is no database access
	serviceInfos, err := cluster.AllServiceInfos()
	if err != nil {
		return err
	}

	// cleanup entries for services that have been removed from the configuration
	for _, srv := range services {
		seen[srv.Type] = true
		if !core.HasService(serviceInfos, srv.Type) {
			logg.Info("cleaning up %s service entry for project %s/%s", srv.Type, domain.Name, project.Name)
			_, err := dbi.Delete(&srv)
			if err != nil {
				return err
			}
			continue
		}
	}

	// create missing service entries
	for _, serviceType := range slices.Sorted(maps.Keys(serviceInfos)) {
		if seen[serviceType] {
			continue
		}

		logg.Info("creating %s service entry for project %s/%s", serviceType, domain.Name, project.Name)
		err := dbi.Insert(&db.ProjectService{
			ProjectID: project.ID,
			Type:      serviceType,
			// immediate scraping of this new service is required to create `project_resources`
			// and `project_az_resources` entries and thus make the project service fully functional
			NextScrapeAt: now,
			// setting the stale flags prioritizes scraping of this service over the
			// existing backlog of routine scrapes, even if the backlog is very long
			Stale: true,
		})
		if err != nil {
			return err
		}
	}

	return nil
}
