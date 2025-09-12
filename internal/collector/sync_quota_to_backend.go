// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lib/pq"
	. "github.com/majewsky/gg/option"
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
	SELECT ps.* FROM project_services ps
	JOIN services s ON ps.service_id = s.id
	WHERE s.type = $1 AND ps.quota_desynced_at IS NOT NULL
	-- order by priority (oldest requests first), then by ID for deterministic test behavior
	ORDER BY ps.quota_desynced_at ASC, ps.id ASC
	LIMIT 1
`)

func (c *Collector) discoverQuotaSyncTask(_ context.Context, labels prometheus.Labels) (srv db.ProjectService, err error) {
	serviceType := db.ServiceType(labels["service_type"])

	maybeServiceInfo, err := c.Cluster.InfoForService(serviceType)
	if err != nil {
		return db.ProjectService{}, err
	}

	_, ok := maybeServiceInfo.Unpack()
	if !ok {
		return db.ProjectService{}, fmt.Errorf("no such service type: %q", serviceType)
	}
	labels["service_name"] = labels["service_type"] // for backwards compatibility only (TODO: remove usage from alert definitions, then remove this label)

	err = c.DB.SelectOne(&srv, quotaSyncDiscoverQuery, serviceType)
	return
}

func (c *Collector) processQuotaSyncTask(ctx context.Context, srv db.ProjectService, labels prometheus.Labels) error {
	serviceType := db.ServiceType(labels["service_type"])

	dbProject, dbDomain, project, err := c.identifyProjectBeingScraped(srv)
	if err != nil {
		return err
	}
	logg.Debug("syncing %s quotas for project %s/%s...", serviceType, dbDomain.Name, dbProject.Name)
	err = c.performQuotaSync(ctx, srv, dbProject, project.Domain, serviceType)
	if err != nil {
		return fmt.Errorf("could not sync %s quotas for project %s/%s: %w", serviceType, dbDomain.Name, dbProject.Name, err)
	}
	return nil
}

var (
	// NOTE: This query does not use `AND quota IS NOT NULL` to filter out NoQuota resources
	// because it would also filter out resources with AZSeparatedTopology.
	quotaSyncSelectQuery = sqlext.SimplifyWhitespace(`
		SELECT pr.id, pr.resource_id, r.name, pr.backend_quota, pr.quota, pr.forbidden
		FROM project_resources pr
		JOIN resources r ON pr.resource_id = r.id
		WHERE r.service_id = $1 AND pr.project_id = $2
	`)
	azQuotaSyncSelectQuery = sqlext.SimplifyWhitespace(`
		SELECT azr.az, pazr.backend_quota, pazr.quota
		FROM project_az_resources pazr
		JOIN az_resources azr ON pazr.az_resource_id = azr.id
		WHERE azr.resource_id = $1 AND pazr.project_id = $2 AND pazr.quota IS NOT NULL
	`)
	quotaSyncMarkResourcesAsAppliedQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_resources pr
		SET backend_quota = quota
		FROM resources r
		WHERE pr.resource_id = r.id
		AND r.service_id = $1
		AND pr.project_id = $2
	`)
	azQuotaSyncMarkResourcesAsAppliedQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_az_resources pazr
		SET backend_quota = quota
		FROM az_resources azr
		WHERE pazr.az_resource_id = azr.id
		AND azr.resource_id = ANY($1)
		AND pazr.project_id = $2
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

func (c *Collector) performQuotaSync(ctx context.Context, srv db.ProjectService, project db.Project, domain core.KeystoneDomain, serviceType db.ServiceType) error {
	connection := c.Cluster.LiquidConnections[serviceType]
	if connection == nil {
		return fmt.Errorf("no quota connection registered for service type %s", serviceType)
	}
	startedAt := c.MeasureTime()

	maybeServiceInfo, err := c.Cluster.InfoForService(serviceType)
	if err != nil {
		return err
	}
	serviceInfo, ok := maybeServiceInfo.Unpack()
	if !ok {
		return fmt.Errorf("no such service type: %s", serviceType)
	}

	// collect backend quota values that we want to apply
	targetQuotasInDB := make(map[liquid.ResourceName]uint64)
	targetAZQuotasInDB := make(map[liquid.ResourceName]map[liquid.AvailabilityZone]liquid.AZResourceQuotaRequest)
	needsApply := false
	azSeparatedNeedsApply := false
	var azSeparatedResourceIDs []db.ResourceID
	err = sqlext.ForeachRow(c.DB, quotaSyncSelectQuery, []any{srv.ServiceID, project.ID}, func(rows *sql.Rows) error {
		var (
			projectResourceID db.ProjectResourceID
			resourceID        db.ResourceID
			resourceName      liquid.ResourceName
			currentQuotaPtr   Option[int64]
			targetQuotaPtr    Option[uint64]
			forbidden         bool
		)
		err := rows.Scan(&projectResourceID, &resourceID, &resourceName, &currentQuotaPtr, &targetQuotaPtr, &forbidden)
		if err != nil {
			return err
		}

		resInfo := core.InfoForResource(serviceInfo, resourceName)
		if !resInfo.HasQuota {
			return nil
		}

		if forbidden && currentQuotaPtr.UnwrapOr(0) != 0 {
			return nil
		}

		var targetQuota uint64
		if resInfo.Topology == liquid.AZSeparatedTopology {
			// for AZSeparatedTopology, project_resources.quota is effectively empty (always set to zero)
			// and `targetQuota` needs to be computed by summing over project_az_resources.quota
			targetQuota = 0
			err = sqlext.ForeachRow(c.DB, azQuotaSyncSelectQuery, []any{resourceID, project.ID}, func(rows *sql.Rows) error {
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
			var ok bool
			targetQuota, ok = targetQuotaPtr.Unpack()
			if !ok {
				return fmt.Errorf("found unexpected NULL value in project_resources.quota for id = %d", projectResourceID)
			}
		}

		targetQuotasInDB[resourceName] = targetQuota
		if val, isSome := currentQuotaPtr.Unpack(); !isSome || val < 0 || uint64(val) != targetQuota {
			needsApply = true
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("while collecting target quota values for %s backend: %w", serviceType, err)
	}

	if needsApply || azSeparatedNeedsApply {
		// double-check that we only include quota values for resources that the backend currently knows about
		targetQuotasForBackend := make(map[liquid.ResourceName]liquid.ResourceQuotaRequest)
		for resName, resInfo := range connection.ServiceInfo().Resources {
			if !resInfo.HasQuota {
				continue
			}
			// NOTE: If `targetQuotasInDB` does not have an entry for this resource, we will write 0 into the backend.
			targetQuotasForBackend[resName] = liquid.ResourceQuotaRequest{Quota: targetQuotasInDB[resName], PerAZ: targetAZQuotasInDB[resName]}
		}

		// apply quotas in backend
		err = connection.SetQuota(ctx, core.KeystoneProjectFromDB(project, domain), targetQuotasForBackend)
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
		_, err = c.DB.Exec(quotaSyncMarkResourcesAsAppliedQuery, srv.ServiceID, project.ID)
		if err != nil {
			return err
		}
		if azSeparatedNeedsApply {
			_, err = c.DB.Exec(azQuotaSyncMarkResourcesAsAppliedQuery, pq.Array(azSeparatedResourceIDs), project.ID)
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
