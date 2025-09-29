// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
)

// ApplyQuotaOverridesJob is a jobloop.CronJob.
//
// It loads quota overrides from the respective config file and updates the
// `project_resources.override_quota_from_config` column to match the configured values.
func (c *Collector) ApplyQuotaOverridesJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.CronJob{
		Metadata: jobloop.JobMetadata{
			ReadableName: "apply quota overrides",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_quota_override_syncs",
				Help: "Counts syncs of the quota override config into the DB.",
			},
		},
		Interval:     3 * time.Minute,
		InitialDelay: 5 * time.Second, // apply new configs very eagerly on startup
		Task:         c.applyQuotaOverrides,
	}).Setup(registerer)
}

// ApplyQuotaOverrides is called once on startup of limes-collect.
// It persists the contents of the quota overrides config file into the DB.
func (c *Collector) applyQuotaOverrides(_ context.Context, _ prometheus.Labels) error {
	overrides, errs := datamodel.LoadQuotaOverrides(c.Cluster)
	if !errs.IsEmpty() {
		return errors.New(errs.Join(", "))
	}

	// write configured quota overrides
	for domainName, domainOverrides := range overrides {
		for projectName, projectOverrides := range domainOverrides {
			for serviceType, serviceOverrides := range projectOverrides {
				err := c.aqoUpdateOneProjectService(domainName, projectName, serviceType, serviceOverrides)
				if err != nil {
					return err
				}
			}
		}
	}

	// enumerate all existing quota overrides and clear away those that have been removed from the config
	err := sqlext.ForeachRow(c.DB, aqoListOverridesQuery, nil, func(rows *sql.Rows) error {
		var (
			resourceID   db.ProjectResourceID
			domainName   string
			projectName  string
			serviceType  db.ServiceType
			resourceName liquid.ResourceName
		)
		err := rows.Scan(&resourceID, &domainName, &projectName, &serviceType, &resourceName)
		if err != nil {
			return err
		}
		_, exists := overrides[domainName][projectName][serviceType][resourceName]
		if exists {
			return nil // nothing to do in this loop iteration
		}
		_, err = c.DB.Exec(aqoClearOverrideQuery, resourceID)
		return err
	})
	if err != nil {
		return fmt.Errorf("while clearing outdated quota overrides: %w", err)
	}
	return nil
}

// prefix "aqo" = "apply quota overrides"
var (
	aqoSelectServiceQuery = sqlext.SimplifyWhitespace(`
		SELECT ps.id
		  FROM domains d
		  JOIN projects p ON p.domain_id = d.id
		  JOIN project_services ps ON ps.project_id = p.id
		  JOIN services s ON s.id = ps.service_id
		 WHERE d.name = $1 AND p.name = $2 AND s.type = $3
	`)
	aqoUpdateOverrideQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_resources pr
		   SET override_quota_from_config = $1
		 FROM resources r
		 JOIN project_services ps ON ps.service_id = r.service_id
		 WHERE r.id = pr.resource_id AND ps.project_id = pr.project_id
		 AND ps.id = $2 AND r.name = $3
	`)
	aqoListOverridesQuery = sqlext.SimplifyWhitespace(`
		SELECT pr.id, d.name, p.name, s.type, r.name
		  FROM domains d
		  JOIN projects p ON p.domain_id = d.id
		  JOIN project_resources pr ON pr.project_id = p.id
		  JOIN resources r ON r.id = pr.resource_id
		  JOIN services s ON s.id = r.service_id
		 WHERE pr.override_quota_from_config IS NOT NULL
	`)
	aqoClearOverrideQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_resources
		   SET override_quota_from_config = NULL
		 WHERE id = $1
	`)
)

func (c *Collector) aqoUpdateOneProjectService(domainName, projectName string, serviceType db.ServiceType, overrides map[liquid.ResourceName]uint64) error {
	// find project service
	var serviceID db.ProjectServiceID
	err := c.DB.QueryRow(aqoSelectServiceQuery, domainName, projectName, serviceType).Scan(&serviceID)
	if err != nil {
		if err == sql.ErrNoRows {
			// it is not an error for a project named in the quota overrides file to not exist (yet)
			return nil
		}
		return fmt.Errorf("while locating project service %s/%s/%s: %w", domainName, projectName, serviceType, err)
	}

	// write quota overrides
	for resourceName, overrideQuota := range overrides {
		_, err := c.DB.Exec(aqoUpdateOverrideQuery, overrideQuota, serviceID, resourceName)
		if err != nil {
			return fmt.Errorf("while writing %s override in project service %d: %w", resourceName, serviceID, err)
		}
	}
	return nil
}
