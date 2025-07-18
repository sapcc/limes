// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/db"
)

func (c *Collector) CheckConsistencyJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.CronJob{
		Metadata: jobloop.JobMetadata{
			ReadableName: "ensure that all active domains and projects in this cluster have a service entry for the liquid's service types",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_cron_consistency_runs",
				Help: "Counter for consistency checks runs",
			},
		},
		Interval: 1 * time.Hour,
		// When new services or resources are added, we need this job to populate the project level services densely.
		// It does not take care of cluster level services, resources, or rates - they are added on demand from
		// LiquidConnection.reconcileLiquidConnection() or whenever the collect job is started. Project level resources
		// and az_resources are created by the scraping job, which picks up the created project_services.
		InitialDelay: 10 * time.Second,
		Task:         c.checkConsistency,
	}).Setup(registerer)
}

var (
	deleteSuperfluousClusterServicesQuery = sqlext.SimplifyWhitespace(`
		DELETE FROM cluster_services WHERE type != ALL($1::TEXT[])
		RETURNING type
	`)

	// See `initProjectServicesQuery` for rationale regarding `next_scrape_at = NOW(), stale = TRUE`.
	insertMissingProjectServicesQuery = sqlext.SimplifyWhitespace(`
		INSERT INTO project_services_v2 (project_id, service_id, next_scrape_at, stale)
		SELECT p.id, cs.id, $1::TIMESTAMPTZ, TRUE
		  FROM cluster_services cs
		  JOIN projects p ON TRUE -- this is intentionally a full cross join between "cs" and "p"
		  LEFT OUTER JOIN project_services_v2 ps ON ps.project_id = p.id AND ps.service_id = cs.id
		 WHERE ps.id IS NULL
		RETURNING project_id, service_id
	`)
)

func (c *Collector) checkConsistency(_ context.Context, _ prometheus.Labels) error {
	// cleanup entries for services that have been removed from the configuration
	// (this is also done by core.SaveServiceInfoToDB() on startup, so this is
	// only defense in depth against garbage entries entering the DB somehow)
	knownServiceTypes := slices.Sorted(maps.Keys(c.Cluster.Config.Liquids))
	err := sqlext.ForeachRow(c.DB, deleteSuperfluousClusterServicesQuery, []any{pq.Array(knownServiceTypes)}, func(rows *sql.Rows) error {
		var serviceType db.ServiceType
		err := rows.Scan(&serviceType)
		if err == nil {
			logg.Info("cleaned up cluster_services entry with type = %q (no such type configured)", serviceType)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("while cleaning up cluster_services: %w", err)
	}

	// ensure that `project_services` matches the fully populated cross product of `projects` and `cluster_services`
	// (this is usually only relevant when core.SaveServiceInfoToDB() created a new `cluster_services` entry;
	// for new `projects` entries, initProject() will already have created the respective `project_services` records)
	err = sqlext.ForeachRow(c.DB, insertMissingProjectServicesQuery, []any{c.MeasureTime()}, func(rows *sql.Rows) error {
		var (
			projectID db.ProjectID
			serviceID db.ClusterServiceID
		)
		err := rows.Scan(&projectID, &serviceID)
		if err == nil {
			logg.Info("created missing project_services entry with project_id = %d, service_id = %d", projectID, serviceID)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("while populating missing project_services: %w", err)
	}

	return nil
}
