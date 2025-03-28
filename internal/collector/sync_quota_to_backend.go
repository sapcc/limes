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
	"time"

	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

// SyncQuotaToBackendJob looks for project services that need to have their
// quota applied to the backend, and runs SetQuota for those services.
//
// This job is not ConcurrencySafe, but multiple instances can safely be run in
// parallel if they act on separate service types. The job can only be run if
// a target service type is specified using the
// `jobloop.WithLabel("service_type", serviceType)` option.
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
	 WHERE type = $1 AND quota_desynced_at IS NOT NULL
	 -- order by priority (oldest requests first), then by ID for deterministic test behavior
	 ORDER BY quota_desynced_at ASC, id ASC
	 LIMIT 1
`)

func (c *Collector) discoverQuotaSyncTask(ctx context.Context, labels prometheus.Labels) (srv db.ProjectService, err error) {
	serviceType := db.ServiceType(labels["service_type"])
	if !c.Cluster.HasService(serviceType) {
		return db.ProjectService{}, fmt.Errorf("no such service type: %q", serviceType)
	}

	err = c.DB.SelectOne(&srv, quotaSyncDiscoverQuery, serviceType)
	if err == nil {
		labels["service_name"] = c.Cluster.InfoForService(srv.Type).ProductName
	}
	return
}

func (c *Collector) processQuotaSyncTask(ctx context.Context, srv db.ProjectService, labels prometheus.Labels) error {
	dbProject, dbDomain, project, err := c.identifyProjectBeingScraped(srv)
	if err != nil {
		return err
	}
	logg.Debug("syncing %s quotas for project %s/%s...", srv.Type, dbDomain.Name, dbProject.Name)
	err = c.performQuotaSync(ctx, srv, dbProject, project.Domain)
	if err != nil {
		return fmt.Errorf("could not sync %s quotas for project %s/%s: %w", srv.Type, dbDomain.Name, dbProject.Name, err)
	}
	return nil
}

var (
	// NOTE: This query does not use `AND quota IS NOT NULL` to filter out NoQuota resources
	// because it would also filter out resources with AZSeparatedTopology.
	quotaSyncSelectQuery = sqlext.SimplifyWhitespace(`
		SELECT id, name, backend_quota, quota
		  FROM project_resources
		 WHERE service_id = $1
	`)
	azQuotaSyncSelectQuery = sqlext.SimplifyWhitespace(`
		SELECT az, backend_quota, quota
		  FROM project_az_resources
		 WHERE resource_id = $1 AND quota IS NOT NULL
	`)
	quotaSyncMarkResourcesAsAppliedQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_resources
		   SET backend_quota = quota
		 WHERE service_id = $1
	`)
	azQuotaSyncMarkResourcesAsAppliedQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_az_resources
		   SET backend_quota = quota
		 WHERE resource_id = ANY($1)
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

func (c *Collector) performQuotaSync(ctx context.Context, srv db.ProjectService, project db.Project, domain core.KeystoneDomain) error {
	plugin := c.Cluster.QuotaPlugins[srv.Type]
	if plugin == nil {
		return fmt.Errorf("no quota plugin registered for service type %s", srv.Type)
	}
	startedAt := c.MeasureTime()

	// collect backend quota values that we want to apply
	targetQuotasInDB := make(map[liquid.ResourceName]uint64)
	targetAZQuotasInDB := make(map[liquid.ResourceName]map[liquid.AvailabilityZone]liquid.AZResourceQuotaRequest)
	needsApply := false
	azSeparatedNeedsApply := false
	var azSeparatedResourceIDs []db.ProjectResourceID
	err := sqlext.ForeachRow(c.DB, quotaSyncSelectQuery, []any{srv.ID}, func(rows *sql.Rows) error {
		var (
			resourceID     db.ProjectResourceID
			resourceName   liquid.ResourceName
			currentQuota   *int64
			targetQuotaPtr *uint64
		)
		err := rows.Scan(&resourceID, &resourceName, &currentQuota, &targetQuotaPtr)
		if err != nil {
			return err
		}

		resInfo := c.Cluster.InfoForResource(srv.Type, resourceName)
		if !resInfo.HasQuota {
			return nil
		}

		var targetQuota uint64
		if resInfo.Topology == liquid.AZSeparatedTopology {
			// for AZSeparatedTopology, project_resources.quota is effectively empty (always set to zero)
			// and `targetQuota` needs to be computed by summing over project_az_resources.quota
			targetQuota = 0
			err = sqlext.ForeachRow(c.DB, azQuotaSyncSelectQuery, []any{resourceID}, func(rows *sql.Rows) error {
				var (
					availabilityZone liquid.AvailabilityZone
					currentAZQuota   *int64
					targetAZQuota    uint64
				)
				err := rows.Scan(&availabilityZone, &currentAZQuota, &targetAZQuota)
				if err != nil {
					return err
				}
				// defense in depth: configured backend_quota for AZ any or unknown are not valid for the azSeparatedQuota topology.
				if (availabilityZone == liquid.AvailabilityZoneAny || availabilityZone == liquid.AvailabilityZoneUnknown) && currentAZQuota != nil {
					return fmt.Errorf("detected invalid AZ: %s for resource: %s with topology: %s has backend_quota: %v", availabilityZone, resourceName, resInfo.Topology, currentAZQuota)
				}
				azSeparatedResourceIDs = append(azSeparatedResourceIDs, resourceID)
				if targetAZQuotasInDB[resourceName] == nil {
					targetAZQuotasInDB[resourceName] = make(map[liquid.AvailabilityZone]liquid.AZResourceQuotaRequest)
				}
				targetAZQuotasInDB[resourceName][availabilityZone] = liquid.AZResourceQuotaRequest{Quota: targetAZQuota}
				targetQuota += targetAZQuota
				if currentAZQuota == nil || *currentAZQuota < 0 || uint64(*currentAZQuota) != targetAZQuota {
					azSeparatedNeedsApply = true
				}
				return nil
			})
			if err != nil {
				return err
			}
		} else {
			// for anything except AZSeparatedTopology, the total target quota is just what we have in project_resources.quota
			if targetQuotaPtr == nil {
				return fmt.Errorf("found unexpected NULL value in project_resources.quota for id = %d", resourceID)
			}
			targetQuota = *targetQuotaPtr
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

	if needsApply || azSeparatedNeedsApply {
		// double-check that we only include quota values for resources that the backend currently knows about
		targetQuotasForBackend := make(map[liquid.ResourceName]liquid.ResourceQuotaRequest)
		for resName, resInfo := range plugin.Resources() {
			if !resInfo.HasQuota {
				continue
			}
			//NOTE: If `targetQuotasInDB` does not have an entry for this resource, we will write 0 into the backend.
			targetQuotasForBackend[resName] = liquid.ResourceQuotaRequest{Quota: targetQuotasInDB[resName], PerAZ: targetAZQuotasInDB[resName]}
		}

		// apply quotas in backend
		err = plugin.SetQuota(ctx, core.KeystoneProjectFromDB(project, domain), targetQuotasForBackend)
		if err != nil {
			// if SetQuota fails, do not retry immediately;
			// try to sync other projects first, then retry in 30 seconds from now at the earliest
			finishedAt := c.MeasureTimeAtEnd()
			durationSecs := finishedAt.Sub(startedAt).Seconds()
			_, err2 := c.DB.Exec(quotaSyncRetryWithDelayQuery, srv.ID, finishedAt.Add(30*time.Second), durationSecs)
			if err2 != nil {
				return fmt.Errorf("%w (additional error when delaying retry: %s)", err, err2.Error())
			}
			return err
		}
		_, err = c.DB.Exec(quotaSyncMarkResourcesAsAppliedQuery, srv.ID)
		if err != nil {
			return err
		}
		if azSeparatedNeedsApply {
			_, err = c.DB.Exec(azQuotaSyncMarkResourcesAsAppliedQuery, pq.Array(azSeparatedResourceIDs))
			if err != nil {
				return err
			}
		}
	}

	finishedAt := c.MeasureTimeAtEnd()
	durationSecs := finishedAt.Sub(startedAt).Seconds()
	_, err = c.DB.Exec(quotaSyncMarkServiceAsAppliedQuery, srv.ID, durationSecs)
	return err
}
