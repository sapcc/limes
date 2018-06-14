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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/limes/pkg/datamodel"
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

//This extends ListDomains() by handling of the {Include,Exclude}DomainRx. It's
//a separate function for unit test accessibility.
func listDomainsFiltered(cluster *limes.Cluster) ([]limes.KeystoneDomain, error) {
	domains, err := cluster.DiscoveryPlugin.ListDomains(cluster.ProviderClient())
	if err != nil {
		return nil, err
	}

	result := make([]limes.KeystoneDomain, 0, len(domains))
	discovery := cluster.Config.Discovery

	for _, domain := range domains {
		if discovery.ExcludeDomainRx != nil {
			if discovery.ExcludeDomainRx.MatchString(domain.Name) {
				continue
			}
		}
		if discovery.IncludeDomainRx != nil {
			if !discovery.IncludeDomainRx.MatchString(domain.Name) {
				continue
			}
		}
		result = append(result, domain)
	}

	return result, nil
}

//ScanDomains queries Keystone to discover new domains, and returns a
//list of UUIDs for the newly discovered domains.
func ScanDomains(cluster *limes.Cluster, opts ScanDomainsOpts) (result []string, resultErr error) {
	//make sure that the counters are reported
	labels := prometheus.Labels{
		"os_cluster": cluster.ID,
	}
	domainDiscoverySuccessCounter.With(labels).Add(0)
	domainDiscoveryFailedCounter.With(labels).Add(0)
	//report either success or failure when the method exists
	defer func() {
		if resultErr == nil {
			domainDiscoverySuccessCounter.With(labels).Inc()
		} else {
			domainDiscoveryFailedCounter.With(labels).Inc()
		}
	}()

	//list domains in Keystone
	domains, err := listDomainsFiltered(cluster)
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
	existingDomainsByUUID := make(map[string]*db.Domain)
	var dbDomains []*db.Domain
	_, err = db.DB.Select(&dbDomains, `SELECT * FROM domains WHERE cluster_id = $1`, cluster.ID)
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
		existingDomainsByUUID[dbDomain.UUID] = dbDomain
	}

	//when a domain has been created in Keystone, create the corresponding record
	//in our DB and scan its projects immediately
	for _, domain := range domains {
		dbDomain, exists := existingDomainsByUUID[domain.UUID]
		if exists {
			//check if the name was updated in Keystone
			if dbDomain.Name != domain.Name {
				util.LogInfo("discovered Keystone domain name change: %s -> %s", dbDomain.Name, domain.Name)
				dbDomain.Name = domain.Name
				_, err := db.DB.Update(dbDomain)
				if err != nil {
					return result, err
				}
			}
			continue
		}

		util.LogInfo("discovered new Keystone domain: %s", domain.Name)
		dbDomain, err := initDomain(cluster, domain)
		if err != nil {
			return result, err
		}
		result = append(result, domain.UUID)

		//with ScanAllProjects = true, we will scan projects in the next step, so skip now
		if opts.ScanAllProjects {
			dbDomains = append(dbDomains, dbDomain)
		} else {
			_, err = ScanProjects(cluster, dbDomain)
			if err != nil {
				return result, err
			}
		}
	}

	//recurse into ScanProjects if requested
	if opts.ScanAllProjects {
		for _, dbDomain := range dbDomains {
			_, err = ScanProjects(cluster, dbDomain)
			if err != nil {
				return result, err
			}
		}
	}

	return result, nil
}

func initDomain(cluster *limes.Cluster, domain limes.KeystoneDomain) (*db.Domain, error) {
	//do this in a transaction to avoid half-initialized domains
	tx, err := db.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer db.RollbackUnlessCommitted(tx)

	//add record to `domains` table
	dbDomain := &db.Domain{
		ClusterID: cluster.ID,
		Name:      domain.Name,
		UUID:      domain.UUID,
	}
	err = db.DB.Insert(dbDomain)
	if err != nil {
		return nil, err
	}

	_, err = datamodel.ValidateDomainServices(tx, cluster, *dbDomain)
	if err != nil {
		return nil, err
	}

	return dbDomain, tx.Commit()
}

//ScanProjects queries Keystone to discover new projects in the given domain.
func ScanProjects(cluster *limes.Cluster, domain *db.Domain) (result []string, resultErr error) {
	//make sure that the counters are reported
	labels := prometheus.Labels{
		"os_cluster": cluster.ID,
		"domain":     domain.Name,
		"domain_id":  domain.UUID,
	}
	projectDiscoverySuccessCounter.With(labels).Add(0)
	projectDiscoveryFailedCounter.With(labels).Add(0)
	//report either success or failure when the method exists
	defer func() {
		if resultErr == nil {
			projectDiscoverySuccessCounter.With(labels).Inc()
		} else {
			projectDiscoveryFailedCounter.With(labels).Inc()
		}
	}()

	//list projects in Keystone
	projects, err := cluster.DiscoveryPlugin.ListProjects(cluster.ProviderClient(), domain.UUID)
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
	existingProjectsByUUID := make(map[string]*db.Project)
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
		existingProjectsByUUID[dbProject.UUID] = dbProject
	}

	//when a project has been created in Keystone, create the corresponding
	//record in our DB
	for _, project := range projects {
		dbProject, exists := existingProjectsByUUID[project.UUID]
		if exists {
			//check if the name was updated in Keystone
			needToSave := false
			if dbProject.Name != project.Name {
				util.LogInfo("discovered Keystone project name change: %s/%s -> %s", domain.Name, dbProject.Name, project.Name)
				dbProject.Name = project.Name
				needToSave = true
			}
			if dbProject.ParentUUID != project.ParentUUID {
				util.LogInfo("discovered Keystone project parent change for %s/%s: %s -> %s",
					domain.Name, dbProject.Name, dbProject.ParentUUID, project.ParentUUID,
				)
				dbProject.ParentUUID = project.ParentUUID
				needToSave = true
			}
			if needToSave {
				_, err := db.DB.Update(dbProject)
				if err != nil {
					return result, err
				}
			}
			continue
		}

		util.LogInfo("discovered new Keystone project: %s/%s", domain.Name, project.Name)
		err := initProject(cluster, domain, project)
		if err != nil {
			return result, err
		}
		result = append(result, project.UUID)
	}

	return result, nil
}

//Initialize all the database records for a project (in both `projects` and
//`project_services`).
func initProject(cluster *limes.Cluster, domain *db.Domain, project limes.KeystoneProject) error {
	//do this in a transaction to avoid half-initialized projects
	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer db.RollbackUnlessCommitted(tx)

	//add record to `projects` table
	dbProject := &db.Project{
		DomainID:   domain.ID,
		Name:       project.Name,
		UUID:       project.UUID,
		ParentUUID: project.ParentUUID,
	}
	err = db.DB.Insert(dbProject)
	if err != nil {
		return err
	}

	//add records to `project_services` table
	_, err = datamodel.ValidateProjectServices(tx, cluster, *domain, *dbProject)
	if err != nil {
		return err
	}

	return tx.Commit()
}
