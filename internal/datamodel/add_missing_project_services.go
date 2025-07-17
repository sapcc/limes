// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package datamodel

import (
	"time"

	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// AddMissingProjectServices ensures that all required ProjectService records for
// this project exist (cleanup is done automatically with referential integrity).
// Note: This function checks against the cluster config, so unconfigured services are skipped.
// It is not possible to derive this information from the serviceInfos, as they might be sourced from the database.
func AddMissingProjectServices(dbi db.Interface, cluster *core.Cluster, domain db.Domain, project db.Project, now time.Time) error {
	var missingAsProjectServices []db.ClusterService
	_, err := dbi.Select(&missingAsProjectServices, `
		SELECT cs.*
		FROM cluster_services cs
		LEFT JOIN project_services_v2 ps ON cs.id = ps.service_id AND ps.project_id = $1
		WHERE ps.id IS NULL
		ORDER BY cs.type
	`, project.ID)
	if err != nil {
		return err
	}
	logg.Debug("potentially adding %d project_services in project %s/%s...", len(missingAsProjectServices), domain.Name, project.UUID)

	for _, missingAsProjectService := range missingAsProjectServices {
		_, exists := cluster.Config.Liquids[missingAsProjectService.Type]
		if !exists {
			logg.Debug("skipping service_type %d for project %s/%s...", missingAsProjectService.Type, domain.Name, project.UUID)
			continue
		}
		// create entry
		logg.Info("creating project_services entry with type %s for project %s/%s", missingAsProjectService.Type, domain.Name, project.Name)
		err = dbi.Insert(&db.ProjectServiceV2{
			ProjectID: project.ID,
			ServiceID: missingAsProjectService.ID,
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
