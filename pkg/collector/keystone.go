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
	"github.com/sapcc/limes/pkg/db"
	"github.com/sapcc/limes/pkg/limes"
	"github.com/sapcc/limes/pkg/util"
)

//ScanDomainsOpts contains additional options for ScanDomains().
type ScanDomainsOpts struct {
	//Recurse into ScanProjects for all domains in the selected cluster,
	//rather than just for new domains.
	ScanAllProjects bool
}

//ScanDomains queries Keystone to discover new domains, and returns a
//list of UUIDs for the newly discovered domains.
func ScanDomains(driver limes.Driver, opts ScanDomainsOpts) ([]string, error) {
	clusterID := driver.Cluster().ID

	//list domains in Keystone
	domains, err := driver.ListDomains()
	if err != nil {
		return nil, err
	}
	isDomainUUID := make(map[string]bool)
	for _, domain := range domains {
		isDomainUUID[domain.UUID] = true
	}

	//when a domain has been deleted in Keystone, remove it from our database,
	//too (the deletion from the `domains` table includes all projects in that
	//domain and to all related resource records through `ON DELETE CASCADE`)
	isDomainUUIDinDB := make(map[string]bool)
	var dbDomains []*db.Domain
	_, err = db.DB.Select(&dbDomains, `SELECT * FROM domains WHERE cluster_id = $1`, clusterID)
	if err != nil {
		return nil, err
	}
	for _, dbDomain := range dbDomains {
		if !isDomainUUID[dbDomain.UUID] {
			util.LogInfo("removing deleted Keystone domain from our database: %s", dbDomain.Name)
			_, err := db.DB.Delete(dbDomain)
			if err != nil {
				return nil, err
			}
			continue
		}
		isDomainUUIDinDB[dbDomain.UUID] = true
	}

	//when a domain has been created in Keystone, create the corresponding record
	//in our DB and scan its projects immediately
	var result []string
	for _, domain := range domains {
		if isDomainUUIDinDB[domain.UUID] {
			continue
		}

		//TODO: create domain_service and domain_resource entries
		util.LogInfo("discovered new Keystone domain: %s", domain.Name)
		dbDomain, err := initDomain(driver, domain)
		if err != nil {
			return result, err
		}
		result = append(result, domain.UUID)

		//with ScanAllProjects = true, we will scan projects in the next step, so skip now
		if opts.ScanAllProjects {
			dbDomains = append(dbDomains, dbDomain)
		} else {
			_, err = ScanProjects(driver, dbDomain)
			if err != nil {
				return result, err
			}
		}
	}

	//recurse into ScanProjects if requested
	if opts.ScanAllProjects {
		for _, dbDomain := range dbDomains {
			_, err = ScanProjects(driver, dbDomain)
			if err != nil {
				return result, err
			}
		}
	}

	return result, nil
}

func initDomain(driver limes.Driver, domain limes.KeystoneDomain) (*db.Domain, error) {
	//do this in a transaction to avoid half-initialized projects
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer db.RollbackUnlessCommitted(tx)

	//add record to `domains` table
	dbDomain := &db.Domain{
		ClusterID: driver.Cluster().ID,
		Name:      domain.Name,
		UUID:      domain.UUID,
	}
	err = db.DB.Insert(dbDomain)
	if err != nil {
		return nil, err
	}

	//add records for all cluster services to the `project_services` table
	for _, srv := range driver.Cluster().Services {
		err := tx.Insert(&db.DomainService{DomainID: dbDomain.ID, Type: srv.Type})
		if err != nil {
			return nil, err
		}
	}

	return dbDomain, tx.Commit()
}

//ScanProjects queries Keystone to discover new projects in the given domain.
func ScanProjects(driver limes.Driver, domain *db.Domain) ([]string, error) {
	//list projects in Keystone
	projects, err := driver.ListProjects(domain.UUID)
	if err != nil {
		return nil, err
	}
	isProjectUUID := make(map[string]bool)
	for _, project := range projects {
		isProjectUUID[project.UUID] = true
	}

	//when a project has been deleted in Keystone, remove it from our database,
	//too (the deletion from the `projects` table includes the projects' resource
	//records through `ON DELETE CASCADE`)
	isProjectUUIDinDB := make(map[string]bool)
	var dbProjects []*db.Project
	_, err = db.DB.Select(&dbProjects, `SELECT * FROM projects WHERE domain_id = $1`, domain.ID)
	if err != nil {
		return nil, err
	}
	for _, dbProject := range dbProjects {
		if !isProjectUUID[dbProject.UUID] {
			util.LogInfo("removing deleted Keystone project from our database: %s/%s", domain.Name, dbProject.Name)
			_, err := db.DB.Delete(dbProject)
			if err != nil {
				return nil, err
			}
			continue
		}
		isProjectUUIDinDB[dbProject.UUID] = true
	}

	//when a project has been created in Keystone, create the corresponding
	//record in our DB
	var result []string
	for _, project := range projects {
		if isProjectUUIDinDB[project.UUID] {
			continue
		}

		util.LogInfo("discovered new Keystone project: %s/%s", domain.Name, project.Name)
		err := initProject(driver, domain, project)
		if err != nil {
			return result, err
		}
		result = append(result, project.UUID)
	}

	return result, nil
}

//Initialize all the database records for a project (in both `projects` and
//`project_services`).
func initProject(driver limes.Driver, domain *db.Domain, project limes.KeystoneProject) error {
	//do this in a transaction to avoid half-initialized projects
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer db.RollbackUnlessCommitted(tx)

	//add record to `projects` table
	dbProject := &db.Project{
		DomainID: domain.ID,
		Name:     project.Name,
		UUID:     project.UUID,
	}
	err = db.DB.Insert(dbProject)
	if err != nil {
		return err
	}

	//add records for all cluster services to the `project_services` table, with
	//default `scraped_at = NULL` to force the scraping jobs to scrape the
	//project resources
	for _, srv := range driver.Cluster().Services {
		err := tx.Insert(&db.ProjectService{ProjectID: dbProject.ID, Type: srv.Type})
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}
