/*******************************************************************************
*
* Copyright 2017 SAP SE
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

package collector

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
)

func (c *Collector) CheckConsistencyJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.CronJob{
		Metadata: jobloop.JobMetadata{
			ReadableName: "ensure that all active domains and projects in this cluster have a service entry for this plugin's service type",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_cron_consistency_runs",
				Help: "Counter for consistency checks runs",
			},
		},
		Interval: 1 * time.Hour,
		// When new services or resources are added, we need this job to create the respective DB entries immediately upon deployment.
		InitialDelay: 10 * time.Second,
		Task: func(ctx context.Context, _ prometheus.Labels) error {
			return c.CheckConsistencyCluster(ctx)
		},
	}).Setup(registerer)
}

func (c *Collector) CheckConsistencyCluster(_ context.Context) error {
	//check cluster_services entries
	var services []db.ClusterService
	_, err := c.DB.Select(&services, `SELECT * FROM cluster_services`)
	if err != nil {
		return err
	}
	logg.Info("checking consistency for %d cluster services...", len(services))

	//cleanup entries for services that have been disabled
	seen := make(map[string]bool)
	for _, service := range services {
		seen[service.Type] = true

		if !c.Cluster.HasService(service.Type) {
			logg.Info("cleaning up %s cluster service entry", service.Type)
			_, err := c.DB.Delete(&service) //nolint:gosec // Delete is not holding onto the pointer after it returns
			if err != nil {
				c.LogError(err.Error())
			}
		}
	}

	now := c.TimeNow()
	//create missing service entries
	for _, serviceType := range c.Cluster.ServiceTypesInAlphabeticalOrder() {
		if seen[serviceType] {
			continue
		}

		logg.Info("creating missing %s cluster service entry", serviceType)
		err := c.DB.Insert(&db.ClusterService{
			Type:      serviceType,
			ScrapedAt: &now,
		})
		if err != nil {
			c.LogError(err.Error())
		}
	}

	//recurse into domains (with deterministic ordering for the unit test's sake;
	//the DESC ordering is because I was too lazy to change the fixtures)
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
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	//validate domain_services entries
	err = datamodel.ValidateDomainServices(tx, c.Cluster, domain)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return err
	}

	//recurse into projects (with deterministic ordering for the unit test's sake)
	var projects []db.Project
	_, err = c.DB.Select(&projects, `SELECT * FROM projects WHERE domain_id = $1 ORDER BY NAME`, domain.ID)
	if err != nil {
		return err
	}
	logg.Info("checking consistency for %d projects in domain %q...", len(projects), domain.Name)

	now := c.TimeNow()
	for _, project := range projects {
		//ValidateProjectServices usually does nothing or does maybe one DELETE or
		//INSERT, so it does not need to be in a transaction
		err := datamodel.ValidateProjectServices(c.DB, c.Cluster, domain, project, now)
		if err != nil {
			c.LogError(err.Error())
		}
	}

	return nil
}
