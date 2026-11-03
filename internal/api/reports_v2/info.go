// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"time"

	"github.com/sapcc/go-bits/must"
	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/oblast"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/sqlext"

	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

var findAllowedResourcesQuery = sqlext.SimplifyWhitespace(`
	SELECT DISTINCT s.type, r.name
	FROM project_resources pr
	JOIN resources r ON pr.resource_id = r.id
	JOIN services s ON r.service_id = s.id
	JOIN projects p ON pr.project_id = p.id
	JOIN domains d ON p.domain_id = d.id
	WHERE {{p.id = ANY($project_id)}} AND {{d.id = ANY($domain_id)}}
	AND pr.forbidden = false
`)

// InfoScope describes the scope used to build an info report.
// It is resolved from the token and optional query parameters
// by the handler before calling [GetResourcesInfo] or [GetRatesInfo].
type InfoScope struct {
	// ProjectUUID restricts the report to resources/rates available in this project.
	// If empty, the report covers the whole domain (or cluster, if DomainUUID is also empty).
	ProjectUUID string
	// DomainUUID restricts the report to resources/rates available in at least one project of this domain.
	// If empty (and ProjectUUID is also empty), the report covers the whole cluster.
	DomainUUID string
	// DomainName is the name of the domain identified by DomainUUID (needed for commitment behavior scoping).
	DomainName string
}

// GetResourcesInfo returns a resourcesv2.InfoReport, which can be exposed via
// an own endpoint or re-used in resource reports.
func GetResourcesInfo(ctx context.Context, cluster *core.Cluster, scope Scope, timeNow time.Time, sis core.ServiceInfoReader) (resourcesv2.InfoReport, error) {
	dbm := cluster.DB
	var none resourcesv2.InfoReport // used on error return paths only

	// collect allowed items for this user
	allowedResourcesByService := make(map[db.ServiceType]map[liquid.ResourceName]struct{})
	type allowedResourceRecord struct {
		ServiceType  db.ServiceType      `db:"type"`
		ResourceName liquid.ResourceName `db:"name"`
	}
	query, args := scope.ExpandScopeFilters(findAllowedResourcesQuery)
	err := oblast.MustNewStore[allowedResourceRecord](oblast.PostgresDialect()).Select(ctx, dbm, query, args...).Foreach(func(r allowedResourceRecord) error {
		if allowedResourcesByService[r.ServiceType] == nil {
			allowedResourcesByService[r.ServiceType] = make(map[liquid.ResourceName]struct{})
		}
		allowedResourcesByService[r.ServiceType][r.ResourceName] = struct{}{}
		return nil
	})
	if err != nil {
		return none, err
	}

	// first pass: collect all behaviors to re-use later
	allBehaviors := make(map[db.ResourcePath]core.ScopedCommitmentBehavior)
	services := sis.GetServices()
	for serviceType := range services.Keys() {
		for resource := range sis.GetResourcesForType(serviceType).Values() {
			commitmentBehavior := cluster.CommitmentBehaviorForResource(serviceType, resource.Name)
			if domain, ok := scope.GetDomain().Unpack(); ok {
				allBehaviors[resource.Path] = commitmentBehavior.ForDomain(domain.Name)
			} else {
				allBehaviors[resource.Path] = commitmentBehavior.ForCluster()
			}
		}
	}

	// second pass: assemble the report, including conversion targets
	report := resourcesv2.InfoReport{
		AllAZs: cluster.Config.AvailabilityZones,
		Areas:  make(map[string]resourcesv2.AreaInfoReport),
	}
	for _, serviceType := range slices.Sorted(services.Keys()) {
		service := services.GetOrZero(serviceType)
		categories := sis.GetCategoriesForType(serviceType)
		resources := sis.GetResourcesForType(serviceType)
		// skip non-allowed resources for this user, if any
		allowedResources, serviceTypeOK := allowedResourcesByService[serviceType]
		if !serviceTypeOK {
			continue
		}
		config := cluster.Config.Liquids[serviceType]
		area := config.Area
		// defense in depth: config should be in sync with serviceInfo
		if area == "" {
			continue
		}
		areaDisplayName := ""
		// defense in depth: when area was found in config, a displayName has to be available
		if areaConfig, exists := cluster.Config.Areas[area]; exists {
			areaDisplayName = areaConfig.DisplayName
		}

		if _, exists := report.Areas[area]; !exists {
			report.Areas[area] = resourcesv2.AreaInfoReport{
				DisplayName: areaDisplayName,
				Services:    make(map[db.ServiceType]resourcesv2.ServiceInfoReport),
			}
		}
		report.Areas[area].Services[serviceType] = resourcesv2.ServiceInfoReport{
			Version:     service.LiquidVersion,
			DisplayName: service.DisplayName,
			Categories:  make(map[liquid.CategoryName]resourcesv2.CategoryInfoReport),
		}
		serviceReport := report.Areas[area].Services[serviceType]

		for _, resourceName := range slices.Sorted(resources.Keys()) {
			resource := resources.GetOrZero(resourceName)
			// skip non-allowed resources for this user, if any
			if _, resNameOK := allowedResources[resourceName]; !resNameOK {
				continue
			}
			category := categories.GetOrZero(resource.CategoryID)
			if _, exists := serviceReport.Categories[category.Name]; !exists {
				serviceReport.Categories[category.Name] = resourcesv2.CategoryInfoReport{
					DisplayName: category.DisplayName,
					Resources:   make(map[liquid.ResourceName]resourcesv2.ResourceInfoReport),
				}
			}

			sourceBehavior := allBehaviors[resource.Path]
			resourceReport := resourcesv2.ResourceInfoReport{
				DisplayName:      resource.DisplayName,
				Unit:             resource.Unit,
				Topology:         resource.Topology,
				HasCapacity:      resource.HasCapacity,
				HasQuota:         resource.HasQuota,
				CommitmentConfig: sourceBehavior.ForV2API(timeNow),
			}

			// compute conversion targets using behaviors collected in first pass
			for _, targetPath := range slices.SortedFunc(maps.Keys(allBehaviors), func(a, b db.ResourcePath) int {
				return cmp.Compare(a.String(), b.String())
			}) {
				targetBehavior := allBehaviors[targetPath]
				if resource.Path == targetPath || len(sourceBehavior.Durations) == 0 || len(targetBehavior.Durations) == 0 {
					continue
				}
				targetResource := must.BeOK(sis.GetResourceForPath(targetPath))
				if _, resNameOK := allowedResources[targetResource.Name]; !resNameOK {
					continue
				}
				maybeConversion := sourceBehavior.GetConversionRateTo(targetBehavior, resource.Unit, targetResource.Unit)
				if conversion, ok := maybeConversion.Unpack(); ok {
					resourceReport.CommitmentConversionTargets = append(resourceReport.CommitmentConversionTargets, resourcesv2.CommitmentConversionTarget{
						FromAmount:     conversion.FromAmount,
						ToAmount:       conversion.ToAmount,
						TargetService:  targetResource.Path.ServiceType,
						TargetResource: targetResource.Path.ResourceName,
						OneWay:         conversion.OneWay,
						AllowRounding:  conversion.AllowRounding,
					})
				}
			}

			serviceReport.Categories[category.Name].Resources[resourceName] = resourceReport
		}
	}
	return report, nil
}

// GetRatesInfo returns a ratesv2.InfoReport, which can be exposed via
// an own endpoint or re-used in rate reports.
func GetRatesInfo(cluster *core.Cluster, scope Scope, sis core.ServiceInfoReader) (ratesv2.InfoReport, error) {
	// assemble the report
	report := ratesv2.InfoReport{
		AllAZs: cluster.Config.AvailabilityZones,
		Areas:  make(map[string]ratesv2.AreaInfoReport),
	}
	services := sis.GetServices()

	for _, serviceType := range slices.Sorted(services.Keys()) {
		service := services.GetOrZero(serviceType)
		categories := sis.GetCategoriesForType(serviceType)
		rates := sis.GetRatesForType(serviceType) // can have no rates
		config := cluster.Config.Liquids[serviceType]
		area := config.Area
		// defense in depth: config should be in sync with serviceInfo
		if area == "" {
			continue
		}
		areaDisplayName := ""
		// defense in depth: when area was found in config, a displayName has to be available
		if areaConfig, exists := cluster.Config.Areas[area]; exists {
			areaDisplayName = areaConfig.DisplayName
		}

		if _, exists := report.Areas[area]; !exists {
			report.Areas[area] = ratesv2.AreaInfoReport{
				DisplayName: areaDisplayName,
				Services:    make(map[db.ServiceType]ratesv2.ServiceInfoReport),
			}
		}
		report.Areas[area].Services[serviceType] = ratesv2.ServiceInfoReport{
			Version:     service.LiquidVersion,
			DisplayName: service.DisplayName,
			Categories:  make(map[liquid.CategoryName]ratesv2.CategoryInfoReport),
		}
		serviceReport := report.Areas[area].Services[serviceType]

		for _, rateName := range slices.Sorted(rates.Keys()) {
			rate := rates.GetOrZero(rateName)
			rir := ratesv2.RateInfoReport{
				DisplayName: rate.DisplayName,
				Topology:    rate.Topology,
				HasUsage:    rate.HasUsage,
				Unit:        rate.Unit,
			}
			if rateConfig, ok := config.RateLimits.GetProjectDefaultRateLimit(rateName).Unpack(); ok {
				rir.ProjectDefaultLimit = rateConfig.Limit
				rir.ProjectDefaultWindow = Some(rateConfig.Window)
			}
			category := categories.GetOrZero(rate.CategoryID)
			if _, exists := serviceReport.Categories[category.Name]; !exists {
				serviceReport.Categories[category.Name] = ratesv2.CategoryInfoReport{
					DisplayName: category.DisplayName,
					Rates:       make(map[liquid.RateName]ratesv2.RateInfoReport),
				}
			}
			serviceReport.Categories[category.Name].Rates[rateName] = rir
		}
	}
	return report, nil
}
