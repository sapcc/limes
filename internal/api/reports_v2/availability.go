// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"context"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/liquidapi"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/sqlext"
	"go.xyrillian.de/gg/oblast"

	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/datamodel"
	"github.com/sapcc/limes/internal/db"
)

// The allocation per AZ resource is computed as:
//
//	SUM over projects of max(confirmed_commitment, usage)
//
// This is the same "usedCapacity" concept as in datamodel.clusterAZAllocationStats.CanAcceptCommitmentChanges.
// Only confirmed commitments count: pending/planned commitments do not yet consume capacity.
//
// The allowed_resources CTE reuses [findAllowedResourcesQuery] to restrict the result set
// to resources visible for the authenticated scope. For scoped users this also prunes the
// heavy aggregation CTEs (project_commitment_sums, allocation_per_az_resource) to only
// process rows belonging to visible resources.
var availabilityReportQuery = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
	WITH
	allowed_resources AS (` + findAllowedResourcesQuery + `),
	project_commitment_sums AS (
		SELECT pc.az_resource_id, pc.project_id, COALESCE(SUM(pc.amount), 0) AS committed
		FROM project_commitments pc
		JOIN az_resources azr ON azr.id = pc.az_resource_id
		JOIN allowed_resources ar ON ar.resource_id = azr.resource_id
		WHERE pc.status = {{liquid.CommitmentStatusConfirmed}}
		GROUP BY pc.az_resource_id, pc.project_id
	),
	allocation_per_az_resource AS (
		SELECT pazr.az_resource_id,
		SUM(GREATEST(COALESCE(pcs.committed, 0), pazr.usage)) AS allocation
		FROM project_az_resources pazr
		JOIN az_resources azr ON azr.id = pazr.az_resource_id
		JOIN allowed_resources ar ON ar.resource_id = azr.resource_id
		LEFT JOIN project_commitment_sums pcs
		ON pcs.project_id = pazr.project_id AND pcs.az_resource_id = pazr.az_resource_id
		GROUP BY pazr.az_resource_id
	)
	SELECT azr.id, azr.raw_capacity, COALESCE(alloc.allocation, 0) AS allocation
	FROM az_resources azr
	JOIN resources r ON r.id = azr.resource_id
	JOIN allowed_resources ar ON ar.resource_id = azr.resource_id
	LEFT JOIN allocation_per_az_resource alloc ON alloc.az_resource_id = azr.id
	WHERE azr.az != {{liquid.AvailabilityZoneTotal}}
	AND r.has_capacity = true
`))

// GetResourcesAvailability returns a resourcesv2.AvailabilityReport showing
// the headroom per AZ for each resource that reports capacity.
// The allocation is always cluster-wide; the scope only determines which
// resources are visible to the authenticated user.
func GetResourcesAvailability(ctx context.Context, cluster *core.Cluster, scope Scope, sis core.ServiceInfoReader) (resourcesv2.AvailabilityReport, error) {
	var result resourcesv2.AvailabilityReport

	type record struct {
		AZResourceID db.AZResourceID `db:"id"`
		RawCapacity  uint64          `db:"raw_capacity"`
		Allocation   uint64          `db:"allocation"`
	}
	query, args := scope.ExpandScopeFilters(availabilityReportQuery)
	err := oblast.MustNewStore[record](oblast.PostgresDialect()).Select(ctx, cluster.DB, query, args...).Foreach(func(r record) error {
		azResource, aExists := sis.GetAZResourceForID(r.AZResourceID)
		if !aExists {
			return nil
		}
		resource := must.BeOK(sis.GetResourceForID(azResource.ResourceID))
		service := must.BeOK(sis.GetServiceForID(resource.ServiceID))
		if !datamodel.AZHasCapacityForTopology(resource.Topology, azResource.AvailabilityZone) {
			return nil
		}

		overcommitFactor := cluster.BehaviorForResourcePath(resource.Path).OvercommitFactor
		capacity := overcommitFactor.ApplyTo(r.RawCapacity)
		available := liquidapi.SaturatingSub(capacity, r.Allocation)

		setInAvailabilityReport(sis, cluster, &result, azResource, resource, service, available)
		return nil
	})

	return result, err
}

// setInAvailabilityReport assembles the nested report structure.
func setInAvailabilityReport(sis core.ServiceInfoReader, cluster *core.Cluster, report *resourcesv2.AvailabilityReport, azResource db.AZResource, resource db.Resource, service db.Service, available uint64) {
	config := cluster.Config.Liquids[service.Type]
	area := config.Area
	if area == "" {
		return
	}

	if report.Areas == nil {
		report.Areas = make(map[string]resourcesv2.AreaAvailabilityReport)
	}
	if _, exists := report.Areas[area]; !exists {
		report.Areas[area] = resourcesv2.AreaAvailabilityReport{Services: make(map[db.ServiceType]resourcesv2.ServiceAvailabilityReport)}
	}
	areaReport := report.Areas[area]

	if _, exists := areaReport.Services[service.Type]; !exists {
		areaReport.Services[service.Type] = resourcesv2.ServiceAvailabilityReport{Categories: make(map[liquid.CategoryName]resourcesv2.CategoryAvailabilityReport)}
	}
	serviceReport := areaReport.Services[service.Type]

	categoryName := must.BeOK(sis.GetCategoryForID(resource.CategoryID)).Name
	if _, exists := serviceReport.Categories[categoryName]; !exists {
		serviceReport.Categories[categoryName] = resourcesv2.CategoryAvailabilityReport{Resources: make(map[liquid.ResourceName]resourcesv2.ResourceAvailabilityReport)}
	}
	categoryReport := serviceReport.Categories[categoryName]

	if _, exists := categoryReport.Resources[resource.Name]; !exists {
		categoryReport.Resources[resource.Name] = resourcesv2.ResourceAvailabilityReport{AvailabilityZones: make(map[limes.AvailabilityZone]uint64)}
	}
	resReport := categoryReport.Resources[resource.Name]

	resReport.AvailabilityZones[azResource.AvailabilityZone] = available
}
