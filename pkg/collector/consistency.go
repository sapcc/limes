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
	"time"

	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/pkg/datamodel"
	"github.com/sapcc/limes/pkg/db"
)

var consistencyCheckInterval = 1 * time.Hour

// CheckConsistency ensures that all active domains and projects in this cluster
// have a service entry for this plugin's service type.
func (c *Collector) CheckConsistency() {
	for {
		c.checkConsistencyCluster()

		if c.Once {
			return
		}
		time.Sleep(consistencyCheckInterval)
	}
}

//TODO: code duplication

func (c *Collector) checkConsistencyCluster() {
	now := c.TimeNow()

	//check cluster_services entries
	var services []db.ClusterService
	_, err := c.DB.Select(&services, `SELECT * FROM cluster_services WHERE cluster_id = $1`, c.Cluster.ID)
	if err != nil {
		c.LogError(err.Error())
		return
	}
	logg.Info("checking consistency for %d cluster services...", len(services))

	//cleanup entries for services that have been disabled
	seen := make(map[string]bool)
	for _, service := range services {
		seen[service.Type] = true

		if !c.Cluster.HasService(service.Type) {
			logg.Info("cleaning up %s service entry for domain %s", service.Type, c.Cluster.ID)
			_, err := c.DB.Delete(&service) //nolint:gosec // Delete is not holding onto the pointer after it returns
			if err != nil {
				c.LogError(err.Error())
			}
		}
	}

	//create missing service entries
	for _, serviceType := range c.Cluster.ServiceTypes {
		if seen[serviceType] {
			continue
		}

		logg.Info("creating missing %s service entry for cluster %s", serviceType, c.Cluster.ID)
		err := c.DB.Insert(&db.ClusterService{
			ClusterID: c.Cluster.ID,
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
	_, err = c.DB.Select(&domains, `SELECT * FROM domains WHERE cluster_id = $1 ORDER BY name DESC`, c.Cluster.ID)
	if err != nil {
		c.LogError(err.Error())
		return
	}

	for _, domain := range domains {
		c.checkConsistencyDomain(domain)
	}
}

func (c *Collector) checkConsistencyDomain(domain db.Domain) {
	tx, err := c.DB.Begin()
	if err != nil {
		c.LogError(err.Error())
		return
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	//validate domain_services entries
	_, err = datamodel.ValidateDomainServices(tx, c.Cluster, domain)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		c.LogError(err.Error())
		return
	}

	//recurse into projects (with deterministic ordering for the unit test's sake)
	var projects []db.Project
	_, err = c.DB.Select(&projects, `SELECT * FROM projects WHERE domain_id = $1 ORDER BY NAME`, domain.ID)
	if err != nil {
		c.LogError(err.Error())
		return
	}
	logg.Info("checking consistency for %d projects in domain %q...", len(projects), domain.Name)

	for _, project := range projects {
		err := c.checkConsistencyProject(project, domain)
		if err != nil {
			c.LogError(err.Error())
		}
	}
}

func (c *Collector) checkConsistencyProject(project db.Project, domain db.Domain) error {
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	//validate project_services entries
	_, err = datamodel.ValidateProjectServices(tx, c.Cluster, domain, project)
	if err == nil {
		err = tx.Commit()
	}
	return err
}
