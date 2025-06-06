// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// ScanDomainsAndProjectsJob is a jobloop.CronJob.
// It syncs domains and projects from Keystone into the Limes database.
func (c *Collector) ScanDomainsAndProjectsJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.CronJob{
		Metadata: jobloop.JobMetadata{
			ReadableName: "sync domains and projects from Keystone",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_keystone_syncs",
				Help: "Counts syncs of domains and projects from Keystone",
			},
		},
		Interval: 3 * time.Minute,
		Task: func(ctx context.Context, _ prometheus.Labels) error {
			_, err := c.ScanDomains(ctx, ScanDomainsOpts{ScanAllProjects: true})
			return err
		},
	}).Setup(registerer)
}

// ScanDomainsOpts contains additional options for ScanDomains().
type ScanDomainsOpts struct {
	// Recurse into ScanProjects for all domains in the selected cluster,
	// rather than just for new domains.
	ScanAllProjects bool
}

// ScanDomains queries Keystone to discover new domains, and returns a
// list of UUIDs for the newly discovered domains.
func (c *Collector) ScanDomains(ctx context.Context, opts ScanDomainsOpts) (result []string, resultErr error) {
	// list domains in Keystone
	allDomains, err := c.Cluster.DiscoveryPlugin.ListDomains(ctx)
	if err != nil {
		return nil, fmt.Errorf("while listing domains: %w", util.UnpackError(err))
	}
	domains := c.Cluster.Config.Discovery.FilterDomains(allDomains)
	isDomainUUID := make(map[string]bool)
	for _, domain := range domains {
		isDomainUUID[domain.UUID] = true
	}

	// when a domain has been deleted in Keystone, remove it from our database,
	// too (the deletion from the `domains` table includes all projects in that
	// domain and to all related resource records through `ON DELETE CASCADE`)
	existingDomainsByUUID := make(map[string]*db.Domain)
	var dbDomains []*db.Domain
	_, err = c.DB.Select(&dbDomains, `SELECT * FROM domains`)
	if err != nil {
		return nil, err
	}
	for _, dbDomain := range dbDomains {
		if !isDomainUUID[dbDomain.UUID] {
			logg.Info("removing deleted Keystone domain from our database: %s", dbDomain.Name)
			_, err := c.DB.Delete(dbDomain)
			if err != nil {
				return nil, err
			}
			continue
		}
		existingDomainsByUUID[dbDomain.UUID] = dbDomain
	}

	// when a domain has been created in Keystone, create the corresponding record
	// in our DB and scan its projects immediately
	for _, domain := range domains {
		dbDomain, exists := existingDomainsByUUID[domain.UUID]
		if exists {
			// check if the name was updated in Keystone
			if dbDomain.Name != domain.Name {
				logg.Info("discovered Keystone domain name change: %s -> %s", dbDomain.Name, domain.Name)
				dbDomain.Name = domain.Name
				_, err := c.DB.Update(dbDomain)
				if err != nil {
					return result, err
				}
			}
			continue
		}

		logg.Info("discovered new Keystone domain: %s", domain.Name)
		dbDomain = &db.Domain{
			Name: domain.Name,
			UUID: domain.UUID,
		}
		err = c.DB.Insert(dbDomain)
		if err != nil {
			return result, err
		}
		result = append(result, domain.UUID)

		// with ScanAllProjects = true, we will scan projects in the next step, so skip now
		if opts.ScanAllProjects {
			dbDomains = append(dbDomains, dbDomain)
		} else {
			_, err = c.ScanProjects(ctx, dbDomain)
			if err != nil {
				return result, err
			}
		}
	}

	// recurse into ScanProjects if requested
	if opts.ScanAllProjects {
		for _, dbDomain := range dbDomains {
			_, err = c.ScanProjects(ctx, dbDomain)
			if err != nil {
				return result, err
			}
		}
	}

	return result, nil
}

// ScanProjects queries Keystone to discover new projects in the given domain.
func (c *Collector) ScanProjects(ctx context.Context, domain *db.Domain) (result []string, resultErr error) {
	// list projects in Keystone
	projects, err := c.Cluster.DiscoveryPlugin.ListProjects(ctx, core.KeystoneDomainFromDB(*domain))
	if err != nil {
		return nil, fmt.Errorf("while listing projects in domain %q: %w", domain.Name, util.UnpackError(err))
	}
	isProjectUUID := make(map[string]bool)
	for _, project := range projects {
		isProjectUUID[project.UUID] = true
	}

	// when a project has been deleted in Keystone, remove it from our database,
	// too (the deletion from the `projects` table includes the projects' resource
	// records through `ON DELETE CASCADE`)
	existingProjectsByUUID := make(map[string]*db.Project)
	var dbProjects []*db.Project
	_, err = c.DB.Select(&dbProjects, `SELECT * FROM projects WHERE domain_id = $1`, domain.ID)
	if err != nil {
		return nil, err
	}
	for _, dbProject := range dbProjects {
		if !isProjectUUID[dbProject.UUID] {
			logg.Info("removing deleted Keystone project from our database: %s/%s", domain.Name, dbProject.Name)
			err = c.deleteProject(dbProject)
			if err != nil {
				return nil, fmt.Errorf("while removing deleted Keystone project %s/%s from our database: %w", domain.Name, dbProject.Name, err)
			}
			continue
		}
		existingProjectsByUUID[dbProject.UUID] = dbProject
	}

	// when a project has been created in Keystone, create the corresponding
	// record in our DB
	for _, project := range projects {
		dbProject, exists := existingProjectsByUUID[project.UUID]
		if exists {
			// check if the name was updated in Keystone
			needToSave := false
			if dbProject.Name != project.Name {
				logg.Info("discovered Keystone project name change: %s/%s -> %s", domain.Name, dbProject.Name, project.Name)
				dbProject.Name = project.Name
				needToSave = true
			}
			if dbProject.ParentUUID != project.ParentUUID {
				logg.Info("discovered Keystone project parent change for %s/%s: %s -> %s",
					domain.Name, dbProject.Name, dbProject.ParentUUID, project.ParentUUID,
				)
				dbProject.ParentUUID = project.ParentUUID
				needToSave = true
			}
			if needToSave {
				_, err := c.DB.Update(dbProject)
				if err != nil {
					return result, err
				}
			}
			continue
		}

		logg.Info("discovered new Keystone project: %s/%s", domain.Name, project.Name)
		err := c.initProject(domain, project)
		if err != nil {
			return result, err
		}
		result = append(result, project.UUID)
	}

	return result, nil
}

// Initialize all the database records for a project (in both `projects` and
// `project_services`).
func (c *Collector) initProject(domain *db.Domain, project core.KeystoneProject) error {
	// do this in a transaction to avoid half-initialized projects
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// add record to `projects` table
	dbProject := &db.Project{
		DomainID:   domain.ID,
		Name:       project.Name,
		UUID:       project.UUID,
		ParentUUID: project.ParentUUID,
	}
	err = tx.Insert(dbProject)
	if err != nil {
		return err
	}

	// add records to `project_services` table
	err = datamodel.ValidateProjectServices(tx, c.Cluster, *domain, *dbProject, c.MeasureTime())
	if err != nil {
		return err
	}

	return tx.Commit()
}

var deleteCommitmentsInProjectQuery = sqlext.SimplifyWhitespace(`
	DELETE FROM project_commitments WHERE id IN (
		SELECT pc.id
		  FROM projects p
		  JOIN project_services ps ON ps.project_id = p.id
		  JOIN project_resources pr ON pr.service_id = ps.id
		  JOIN project_az_resources par ON par.resource_id = pr.id
		  JOIN project_commitments pc ON pc.az_resource_id = par.id
		 WHERE p.id = $1 AND pc.state IN ($2, $3)
	)
`)

// Deletes a project from the DB after it was deleted in Keystone.
// This requires special care because some constraints are "ON DELETE RESTRICT".
func (c *Collector) deleteProject(project *db.Project) error {
	// do this in a transaction to avoid commitment deletions going through unless actually necessary
	tx, err := c.DB.Begin()
	if err != nil {
		return err
	}
	defer sqlext.RollbackUnlessCommitted(tx)

	// it is fine to delete a project that only has superseded and expired commitments on it
	// (if there are commitments in any other state, the `DELETE FROM projects` below will fail
	// and rollback the full transaction)
	_, err = tx.Exec(deleteCommitmentsInProjectQuery, project.ID, db.CommitmentStateSuperseded, db.CommitmentStateExpired)
	if err != nil {
		return err
	}

	_, err = tx.Delete(project)
	if err != nil {
		return err
	}

	return tx.Commit()
}
