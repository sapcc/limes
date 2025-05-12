// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"

	"github.com/sapcc/limes/internal/datamodel"
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
		// LiquidConnection.ReconcileLiquidConnection() or whenever the collect job is started. Project level resources
		// and az_resources are created by the scraping job, which picks up the created project_services.
		InitialDelay: 10 * time.Second,
		Task:         c.CheckConsistencyAllDomains,
	}).Setup(registerer)
}

func (c *Collector) CheckConsistencyAllDomains(_ context.Context, _ prometheus.Labels) error {
	// recurse into domains (with deterministic ordering for the unit test's sake;
	// the DESC ordering is because I was too lazy to change the fixtures)
	var domains []db.Domain
	_, err := c.DB.Select(&domains, `SELECT * FROM domains ORDER BY name DESC`)
	if err != nil {
		return err
	}

	for _, domain := range domains {
		err := c.checkConsistencyDomain(domain)
		if err != nil {
			c.LogError(err.Error())
		}
	}

	return nil
}

func (c *Collector) checkConsistencyDomain(domain db.Domain) error {
	// recurse into projects (with deterministic ordering for the unit test's sake)
	var projects []db.Project
	_, err := c.DB.Select(&projects, `SELECT * FROM projects WHERE domain_id = $1 ORDER BY NAME`, domain.ID)
	if err != nil {
		return err
	}
	logg.Info("checking consistency for %d projects in domain %q...", len(projects), domain.Name)

	now := c.MeasureTime()
	for _, project := range projects {
		// ValidateProjectServices usually does nothing or does maybe one DELETE or
		// INSERT, so it does not need to be in a transaction
		err := datamodel.ValidateProjectServices(c.DB, c.Cluster, domain, project, now)
		if err != nil {
			c.LogError(err.Error())
		}
	}

	return nil
}
