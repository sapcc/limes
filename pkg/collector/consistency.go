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

	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/util"
)

var consistencyCheckInterval = 1 * time.Hour

//CheckConsistency ensures that all active domains and projects in this cluster
//have a service entry for this plugin's service type.
func (c *Collector) CheckConsistency() {
	for {
		var domains []db.Domain
		_, err := db.DB.Select(&domains, `SELECT * FROM domains WHERE cluster_id = $1`, c.Driver.Cluster().ID)
		if err != nil {
			c.LogError(err.Error())
			if c.Once {
				return
			}
			time.Sleep(consistencyCheckInterval)
			continue
		}

		for _, domain := range domains {
			c.checkConsistencyDomain(domain)
		}

		if c.Once {
			return
		}
		time.Sleep(consistencyCheckInterval)
	}
}

//TODO: code duplication

func (c *Collector) checkConsistencyDomain(domain db.Domain) {
	cluster := c.Driver.Cluster()

	//check domain_services entries
	seen := make(map[string]bool)
	var services []db.DomainService
	_, err := db.DB.Select(&services, `SELECT * FROM domain_services WHERE domain_id = $1`, domain.ID)
	if err != nil {
		c.LogError(err.Error())
		return
	}

	//cleanup entries for services that have been disabled
	for _, service := range services {
		seen[service.Type] = true
		if cluster.HasService(service.Type) {
			continue
		}
		util.LogInfo("cleaning up %s service entry for domain %s", service.Type, domain.Name)
		_, err := db.DB.Delete(&service)
		if err != nil {
			c.LogError(err.Error())
		}
	}

	//create missing service entries
	for _, serviceType := range cluster.ServiceTypes {
		if seen[serviceType] {
			continue
		}
		util.LogInfo("creating missing %s service entry for domain %s", serviceType, domain.Name)
		err := db.DB.Insert(&db.DomainService{
			DomainID: domain.ID,
			Type:     serviceType,
		})
		if err != nil {
			c.LogError(err.Error())
		}
	}

	//recurse into projects
	var projects []db.Project
	_, err = db.DB.Select(&projects, `SELECT * FROM projects WHERE domain_id = $1`, domain.ID)
	if err != nil {
		c.LogError(err.Error())
		return
	}

	for _, project := range projects {
		c.checkConsistencyProject(project, domain)
	}
}

func (c *Collector) checkConsistencyProject(project db.Project, domain db.Domain) {
	cluster := c.Driver.Cluster()

	//check project_services entries
	seen := make(map[string]bool)
	var services []db.ProjectService
	_, err := db.DB.Select(&services, `SELECT * FROM project_services WHERE project_id = $1`, project.ID)
	if err != nil {
		c.LogError(err.Error())
		return
	}

	//cleanup entries for services that have been disabled
	for _, service := range services {
		seen[service.Type] = true
		if cluster.HasService(service.Type) {
			continue
		}
		util.LogInfo("cleaning up %s service entry for project %s/%s", service.Type, domain.Name, project.Name)
		_, err := db.DB.Delete(&service)
		if err != nil {
			c.LogError(err.Error())
		}
	}

	//create missing service entries
	for _, serviceType := range cluster.ServiceTypes {
		if seen[serviceType] {
			continue
		}
		util.LogInfo("creating missing %s service entry for project %s/%s", serviceType, domain.Name, project.Name)
		err := db.DB.Insert(&db.ProjectService{
			ProjectID: project.ID,
			Type:      serviceType,
		})
		if err != nil {
			c.LogError(err.Error())
		}
	}
}
