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
	var services []db.ProjectServiceV2
	_, err := dbi.Select(&services,
		`SELECT * FROM project_services_v2 WHERE project_id = $1`, project.ID)
	if err != nil {
		return err
	}
	logg.Debug("checking consistency for %d project services in project %s...", len(services), project.UUID)

	// get translation for service types
	clusterServiceByID, err := db.BuildIndexOfDBResult(
		dbi,
		func(service db.ClusterService) db.ClusterServiceID { return service.ID },
		`SELECT * FROM cluster_services ORDER BY type`,
	)
	if err != nil {
		return err
	}
	clusterServiceByType := make(map[db.ServiceType]db.ClusterService, len(clusterServiceByID))
	for _, service := range clusterServiceByID {
		clusterServiceByType[service.Type] = service
	}

	// as this is called from the collect task, this is no database access
	serviceInfos, err := cluster.AllServiceInfos()
	if err != nil {
		return err
	}

	// cleanup entries for services that have been removed from the configuration
	for _, srv := range services {
		srvType := clusterServiceByID[srv.ServiceID].Type
		seen[srvType] = true
		if !core.HasService(serviceInfos, srvType) {
			logg.Info("cleaning up %s service entry for project %s/%s", srvType, domain.Name, project.Name)
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
		clusterService, exists := clusterServiceByType[serviceType]
		// defense in depth: a cluster service can theoretically not get deleted during runtime, but we better make sure
		if !exists {
			continue
		}
		err := dbi.Insert(&db.ProjectServiceV2{
			ProjectID: project.ID,
			ServiceID: clusterService.ID,
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
