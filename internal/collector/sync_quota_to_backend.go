/*******************************************************************************
*
* Copyright 2024 SAP SE
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
	"database/sql"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// SyncQuotaToBackendJob looks for project services that need to have their
// quota applied to the backend, and runs SetQuota for those services.
func (c *Collector) SyncQuotaToBackendJob(registerer prometheus.Registerer) jobloop.Job {
	return (&jobloop.ProducerConsumerJob[db.ProjectService]{
		Metadata: jobloop.JobMetadata{
			ReadableName: "sync project quota to backend",
			CounterOpts: prometheus.CounterOpts{
				Name: "limes_resource_quota_syncs",
				Help: "Counter for syncs of quota to backend per project service.",
			},
			CounterLabels: []string{"service_type", "service_name"},
		},
		DiscoverTask: c.discoverQuotaSyncTask,
		ProcessTask:  c.processQuotaSyncTask,
	}).Setup(registerer)
}

var quotaSyncDiscoverQuery = sqlext.SimplifyWhitespace(`
	SELECT * FROM project_services
	 WHERE quota_desynced_at IS NOT NULL
	 -- order by priority (oldest requests first), then by ID for deterministic test behavior
	 ORDER BY quota_desynced_at ASC, id ASC
	 LIMIT 1
`)

func (c *Collector) discoverQuotaSyncTask(ctx context.Context, labels prometheus.Labels) (srv db.ProjectService, err error) {
	err = c.DB.SelectOne(&srv, quotaSyncDiscoverQuery)
	if err == nil {
		labels["service_type"] = string(srv.Type)
		labels["service_name"] = c.Cluster.InfoForService(srv.Type).ProductName
	}
	return
}

func (c *Collector) processQuotaSyncTask(_ context.Context, srv db.ProjectService, labels prometheus.Labels) error {
	dbProject, dbDomain, project, err := c.identifyProjectBeingScraped(srv)
	if err != nil {
		return err
	}
	logg.Debug("syncing %s quotas for project %s/%s...", srv.Type, dbDomain.Name, dbProject.Name)
	err = c.performQuotaSync(srv, dbProject, project.Domain)
	if err != nil {
		return fmt.Errorf("could not sync %s quotas for project %s/%s: %w", srv.Type, dbDomain.Name, dbProject.Name, err)
	}
	return nil
}

var (
	quotaSyncSelectQuery = sqlext.SimplifyWhitespace(`
		SELECT name, backend_quota, quota
		  FROM project_resources
		 WHERE service_id = $1 AND quota IS NOT NULL
	`)
	quotaSyncMarkResourcesAsAppliedQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_resources
		   SET backend_quota = quota
		 WHERE service_id = $1
	`)
	quotaSyncMarkServiceAsAppliedQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_services
		   SET quota_desynced_at = NULL, quota_sync_duration_secs = $2
		 WHERE id = $1
	`)
	quotaSyncRetryWithDelayQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_services
		   SET quota_desynced_at = $2, quota_sync_duration_secs = $3
		 WHERE id = $1
	`)
)

func (c *Collector) performQuotaSync(srv db.ProjectService, project db.Project, domain core.KeystoneDomain) error {
	plugin := c.Cluster.QuotaPlugins[srv.Type]
	if plugin == nil {
		return fmt.Errorf("no quota plugin registered for service type %s", srv.Type)
	}
	startedAt := c.MeasureTime()

	// collect backend quota values that we want to apply
	targetQuotasInDB := make(map[limesresources.ResourceName]uint64)
	needsApply := false
	err := sqlext.ForeachRow(c.DB, quotaSyncSelectQuery, []any{srv.ID}, func(rows *sql.Rows) error {
		var (
			resourceName limesresources.ResourceName
			currentQuota *int64
			targetQuota  uint64
		)
		err := rows.Scan(&resourceName, &currentQuota, &targetQuota)
		if err != nil {
			return err
		}
		targetQuotasInDB[resourceName] = targetQuota
		if currentQuota == nil || *currentQuota < 0 || uint64(*currentQuota) != targetQuota {
			needsApply = true
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("while collecting target quota values for %s backend: %w", srv.Type, err)
	}

	if needsApply {
		// double-check that we only include quota values for resources that the backend currently knows about
		targetQuotasForBackend := make(map[limesresources.ResourceName]uint64)
		for _, res := range plugin.Resources() {
			if res.NoQuota {
				continue
			}
			//NOTE: If `targetQuotasInDB` does not have an entry for this resource, we will write 0 into the backend.
			targetQuotasForBackend[res.Name] = targetQuotasInDB[res.Name]
		}

		// apply quotas in backend
		err = plugin.SetQuota(core.KeystoneProjectFromDB(project, domain), targetQuotasForBackend)
		if err != nil {
			// if SetQuota fails, do not retry immediately; try to sync other projects first
			finishedAt := c.MeasureTimeAtEnd()
			durationSecs := finishedAt.Sub(startedAt).Seconds()
			_, err2 := c.DB.Exec(quotaSyncRetryWithDelayQuery, srv.ID, finishedAt, durationSecs)
			if err2 != nil {
				return fmt.Errorf("%w (additional error when delaying retry: %s)", err, err2.Error())
			}
			return err
		}
		_, err = c.DB.Exec(quotaSyncMarkResourcesAsAppliedQuery, srv.ID)
		if err != nil {
			return err
		}
	}

	finishedAt := c.MeasureTimeAtEnd()
	durationSecs := finishedAt.Sub(startedAt).Seconds()
	_, err = c.DB.Exec(quotaSyncMarkServiceAsAppliedQuery, srv.ID, durationSecs)
	return err
}
