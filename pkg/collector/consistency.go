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

	"github.com/sapcc/limes/pkg/datamodel"
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/util"
)

var consistencyCheckInterval = 1 * time.Hour

//CheckConsistency ensures that all active domains and projects in this cluster
//have a service entry for this plugin's service type.
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
	_, err := db.DB.Select(&services, `SELECT * FROM cluster_services WHERE cluster_id IN ($1,'shared')`, c.Cluster.ID)
	if err != nil {
		c.LogError(err.Error())
		return
	}

	//cleanup entries for services that have been disabled
	sharedSeen := make(map[string]bool)
	unsharedSeen := make(map[string]bool)
	for _, service := range services {
		if service.ClusterID == "shared" {
			sharedSeen[service.Type] = true
			//cannot cleanup entries for shared services since we're only looking at
			//one cluster and thus cannot know if a shared service is disabled in all
			//clusters
			continue
		} else {
			unsharedSeen[service.Type] = true
		}

		if !c.Cluster.HasService(service.Type) || c.Cluster.IsServiceShared[service.Type] {
			util.LogInfo("cleaning up %s service entry for domain %s", service.Type, c.Cluster.ID)
			_, err := db.DB.Delete(&service)
			if err != nil {
				c.LogError(err.Error())
			}
		}
	}

	//create missing service entries
	for _, serviceType := range c.Cluster.ServiceTypes {
		shared := c.Cluster.IsServiceShared[serviceType]
		if shared {
			if sharedSeen[serviceType] {
				continue
			}
		} else {
			if unsharedSeen[serviceType] {
				continue
			}
		}

		util.LogInfo("creating missing %s service entry for cluster %s", serviceType, c.Cluster.ID)
		service := &db.ClusterService{
			ClusterID: c.Cluster.ID,
			Type:      serviceType,
			ScrapedAt: &now,
		}
		if shared {
			service.ClusterID = "shared"
		}
		err := db.DB.Insert(service)
		if err != nil {
			c.LogError(err.Error())
		}
	}

	//recurse into domains
	var domains []db.Domain
	_, err = db.DB.Select(&domains, `SELECT * FROM domains WHERE cluster_id = $1`, c.Cluster.ID)
	if err != nil {
		c.LogError(err.Error())
		return
	}

	for _, domain := range domains {
		c.checkConsistencyDomain(domain)
	}
}

func (c *Collector) checkConsistencyDomain(domain db.Domain) {
	tx, err := db.DB.Begin()
	if err != nil {
		c.LogError(err.Error())
		return
	}
	defer db.RollbackUnlessCommitted(tx)

	//validate domain_services entries
	_, err = datamodel.ValidateDomainServices(tx, c.Cluster, domain)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		c.LogError(err.Error())
		return
	}

	//recurse into projects
	var projects []db.Project
	_, err = db.DB.Select(&projects, `SELECT * FROM projects WHERE domain_id = $1`, domain.ID)
	if err != nil {
		c.LogError(err.Error())
		return
	}

	for _, project := range projects {
		err := c.checkConsistencyProject(project, domain)
		if err != nil {
			c.LogError(err.Error())
		}
	}
}

func (c *Collector) checkConsistencyProject(project db.Project, domain db.Domain) error {
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer db.RollbackUnlessCommitted(tx)

	//validate project_services entries
	_, err = datamodel.ValidateProjectServices(tx, c.Cluster, domain, project)
	if err == nil {
		err = tx.Commit()
	}
	return err
}
