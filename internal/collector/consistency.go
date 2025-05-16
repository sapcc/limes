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
			ReadableName: "ensure that all active domains and projects in this cluster have a service entry for this liquid's service type",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_cron_consistency_runs",
				Help: "Counter for consistency checks runs",
			},
		},
		Interval: 1 * time.Hour,
		// When new services or resources are added, we need this job to create the respective DB entries immediately upon deployment.
		InitialDelay: 10 * time.Second,
		Task:         c.checkConsistencyCluster,
	}).Setup(registerer)
}

func (c *Collector) checkConsistencyCluster(_ context.Context, _ prometheus.Labels) error {
	// check cluster_services entries
	var services []db.ClusterService
	_, err := c.DB.Select(&services, `SELECT * FROM cluster_services`)
	if err != nil {
		return err
	}
	logg.Info("checking consistency for %d cluster services...", len(services))

	// cleanup entries for services that have been disabled
	seen := make(map[db.ServiceType]bool)
	for _, service := range services {
		seen[service.Type] = true

		if !c.Cluster.HasService(service.Type) {
			logg.Info("cleaning up %s cluster service entry", service.Type)
			_, err := c.DB.Delete(&service)
			if err != nil {
				c.LogError(err.Error())
			}
		}
	}

	// create missing service entries
	for _, serviceType := range c.Cluster.ServiceTypesInAlphabeticalOrder() {
		if seen[serviceType] {
			continue
		}

		logg.Info("creating missing %s cluster service entry", serviceType)
		err := c.DB.Insert(&db.ClusterService{Type: serviceType, NextScrapeAt: c.MeasureTime()})
		if err != nil {
			c.LogError(err.Error())
		}
	}

	// recurse into domains (with deterministic ordering for the unit test's sake;
	// the DESC ordering is because I was too lazy to change the fixtures)
	var domains []db.Domain
	_, err = c.DB.Select(&domains, `SELECT * FROM domains ORDER BY name DESC`)
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
