// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package collector

import (
	"context"
	"fmt"
	"time"

	"github.com/lib/pq"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/jobloop"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/sqlext"
	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/oblast"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
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

func (c *Collector) discoverQuotaSyncTask(ctx context.Context, labels prometheus.Labels) (srv db.ProjectService, err error) {
	serviceType := db.ServiceType(labels["service_type"])

	// Defense in depth: Verify that we have a LiquidConnection for the serviceType of this task.
	// If there is a functioning LiquidConnection, we can also be sure that calls to Cluster.ServiceInfoCache
	// will have the data which should be in line with the returned liquid.ServiceUsageReport.
	_, ok := c.Cluster.LiquidConnections[serviceType]
	if !ok {
		return srv, fmt.Errorf("no such service type: %q", serviceType)
	}
	labels["service_name"] = labels["service_type"] // for backwards compatibility only (TODO: remove usage from alert definitions, then remove this label)

	return db.ProjectServiceStore.SelectOne(ctx, c.DB, quotaSyncDiscoverQuery, serviceType)
}

func (c *Collector) processQuotaSyncTask(ctx context.Context, srv db.ProjectService, labels prometheus.Labels) error {
	serviceType := db.ServiceType(labels["service_type"])

	dbProject, dbDomain, project, err := c.identifyProjectBeingScraped(ctx, srv)
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
	quotaSyncSelectQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
		SELECT pazr.id AS project_az_resource_id, azr.id as az_resource_id, pazr.backend_quota, pazr.quota
		FROM project_resources pr
		JOIN az_resources azr ON azr.resource_id = pr.resource_id
		JOIN project_az_resources pazr ON pazr.az_resource_id = azr.id AND pazr.project_id = pr.project_id
		WHERE pr.resource_id = ANY($1) AND pr.project_id = $2
		AND (pr.forbidden = false OR COALESCE(pazr.backend_quota, 0) != 0)
	`))
	quotaSyncMarkProjectAZResourcesAsAppliedQuery = sqlext.SimplifyWhitespace(`
		UPDATE project_az_resources pazr
		SET backend_quota = quota
		WHERE pazr.id = ANY($1)
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

	sis := c.Cluster.SIC.GetSnapshot()
	resources := sis.GetResourcesForType(serviceType)
	if resources.Len() == 0 {
		return fmt.Errorf("no data found in ServiceInfoCache for %s", serviceType)
	}
	resourceIDs := make([]db.ResourceID, 0, resources.Len())
	for resource := range resources.Values() {
		resourceIDs = append(resourceIDs, resource.ID)
	}

	// collect az quotas and "total" quota from the DB to check what needs to be applied
	targetQuotasInDB := make(map[liquid.ResourceName]uint64)
	targetAZQuotasInDB := make(map[liquid.ResourceName]map[liquid.AvailabilityZone]liquid.AZResourceQuotaRequest)
	needsApply := false
	var projectAZResourceIDs []db.ProjectAZResourceID
	type quotaToSyncRecord struct {
		ProjectAZResourceID db.ProjectAZResourceID `db:"project_az_resource_id"`
		AZResourceID        db.AZResourceID        `db:"az_resource_id"`
		CurrentQuotaOpt     Option[int64]          `db:"backend_quota"`
		TargetQuotaOpt      Option[uint64]         `db:"quota"`
	}
	err := oblast.MustNewStore[quotaToSyncRecord](oblast.PostgresDialect()).Select(ctx, c.DB, quotaSyncSelectQuery, pq.Array(resourceIDs), project.ID).Foreach(func(r quotaToSyncRecord) error {
		azResource, ok := sis.GetAZResourceForID(r.AZResourceID)
		if !ok {
			// race condition: az_resource deleted while the select was executed
			return nil
		}
		resource := must.BeOK(sis.GetResourceForID(azResource.ResourceID))
		if !resource.HasQuota {
			return nil
		}

		// skip AZ-specific quotas if the backend does not support them
		if !datamodel.AZHasBackendQuotaForTopology(resource.Topology, azResource.AvailabilityZone) {
			return nil
		}
		targetQuota, targetQuotaExists := r.TargetQuotaOpt.Unpack()
		if !targetQuotaExists {
			return fmt.Errorf("found unexpected NULL value in project_az_resources.quota for %s/%s/%s", serviceType, resource.Name, azResource.AvailabilityZone)
		}
		currentQuota, currentQuotaExists := r.CurrentQuotaOpt.Unpack()
		// defense in depth: configured backend_quota for AZ any or unknown are not valid for the azSeparatedQuota topology.
		if resource.Topology == liquid.AZSeparatedTopology && (azResource.AvailabilityZone == liquid.AvailabilityZoneAny || azResource.AvailabilityZone == liquid.AvailabilityZoneUnknown) && !currentQuotaExists {
			return fmt.Errorf("detected invalid AZ %q for resource %s/%s with topology %s (backend quota was nil)", azResource.AvailabilityZone, serviceType, resource.Name, resource.Topology)
		}
		projectAZResourceIDs = append(projectAZResourceIDs, r.ProjectAZResourceID)
		if targetAZQuotasInDB[resource.Name] == nil {
			targetAZQuotasInDB[resource.Name] = make(map[liquid.AvailabilityZone]liquid.AZResourceQuotaRequest)
		}
		// due to the interface not understanding liquid.AvailabilityZoneTotal, we have to fish that out
		if azResource.AvailabilityZone == liquid.AvailabilityZoneTotal {
			targetQuotasInDB[resource.Name] = targetQuota
		} else {
			targetAZQuotasInDB[resource.Name][azResource.AvailabilityZone] = liquid.AZResourceQuotaRequest{Quota: targetQuota}
		}
		if !currentQuotaExists || currentQuota < 0 || uint64(currentQuota) != targetQuota {
			needsApply = true
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("while collecting target quota values for %s backend: %w", serviceType, err)
	}

	if needsApply {
		// double-check that we only include quota values for resources that the backend currently knows about
		targetQuotasForBackend := make(map[liquid.ResourceName]liquid.ResourceQuotaRequest)
		for resName, resource := range sis.GetResourcesForType(serviceType).All() {
			if !resource.HasQuota {
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
		_, err = c.DB.Exec(quotaSyncMarkProjectAZResourcesAsAppliedQuery, pq.Array(projectAZResourceIDs))
		if err != nil {
			return err
		}
	}

	finishedAt := c.MeasureTimeAtEnd()
	durationSecs := finishedAt.Sub(startedAt).Seconds()
	_, err = c.DB.Exec(quotaSyncMarkServiceAsAppliedQuery, srv.ID, durationSecs)
	return err
}
