// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"database/sql"
	"maps"
	"slices"
	"time"

	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gopherpolicy"
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
	WHERE ((p.uuid = $1 OR $1 = '') AND (d.uuid = $2 OR $2 = ''))
	AND pr.forbidden = false
`)

func authenticateInfoRequest(token *gopherpolicy.Token) (projectUUID, domainUUID, domainName string, err error) {
	projectUUID = token.ProjectScopeUUID()
	projectDomainUUID := token.ProjectScopeDomainUUID()
	projectDomainName := token.ProjectScopeDomainName()
	domainUUID = token.DomainScopeUUID()
	domainName = token.DomainScopeName()

	switch {
	case token.Check("v2:cluster:info"):
		return "", "", "", nil
	case token.Check("v2:domain:info"):
		return "", domainUUID, domainName, nil
	default:
		// the final token.Check() is a token.Enforce() because Enforce can properly distinguish between 401 and 403 errors
		err := token.Enforce("v2:project:info")
		if err == nil {
			return projectUUID, projectDomainUUID, projectDomainName, nil
		} else {
			return "", "", "", err
		}
	}
}

// GetResourcesInfo returns a resourcesv2.InfoReport, which can be exposed via
// an own endpoint or re-used in resource reports.
func GetResourcesInfo(cluster *core.Cluster, token *gopherpolicy.Token, timeNow time.Time, sis core.ServiceInfoReader) (resourcesv2.InfoReport, error) {
	dbm := cluster.DB
	var none resourcesv2.InfoReport // used on error return paths only

	projectUUID, domainUUID, domainName, err := authenticateInfoRequest(token)
	if err != nil {
		return none, err
	}

	// collect allowed items for this user
	allowedResourcesByService := make(map[db.ServiceType][]liquid.ResourceName)
	err = sqlext.ForeachRow(dbm, findAllowedResourcesQuery, []any{projectUUID, domainUUID}, func(rows *sql.Rows) error {
		var (
			serviceType  db.ServiceType
			resourceName liquid.ResourceName
		)
		err := rows.Scan(&serviceType, &resourceName)
		if err == nil {
			allowedResourcesByService[serviceType] = append(allowedResourcesByService[serviceType], resourceName)
		}
		return err
	})
	if err != nil {
		return none, err
	}

	// assemble the report
	report := resourcesv2.InfoReport{
		AllAZs: cluster.Config.AvailabilityZones,
		Areas:  make(map[string]resourcesv2.AreaInfoReport),
	}
	services := sis.GetServices()
	categories := sis.GetCategories()

	for _, serviceType := range slices.Sorted(maps.Keys(services)) {
		service := services[serviceType]
		resources, _ := sis.GetResourcesForType(serviceType) // can have no resources
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

		for _, resourceName := range slices.Sorted(maps.Keys(resources)) {
			resource := resources[resourceName]
			// skip non-allowed resources for this user, if any
			if !slices.Contains(allowedResources, resourceName) {
				continue
			}
			category := liquid.CategoryName(serviceType)
			categoryDisplayName := service.DisplayName
			if categoryID, exists := resource.CategoryID.Unpack(); exists {
				category = categories[categoryID].Name
				categoryDisplayName = categories[categoryID].DisplayName
			}
			if _, exists := serviceReport.Categories[category]; !exists {
				serviceReport.Categories[category] = resourcesv2.CategoryInfoReport{
					DisplayName: categoryDisplayName,
					Resources:   make(map[liquid.ResourceName]resourcesv2.ResourceInfoReport),
				}
			}
			commitmentBehavior := cluster.CommitmentBehaviorForResource(serviceType, resourceName)
			var scopedCommitmentBehavior core.ScopedCommitmentBehavior

			if domainUUID == "" {
				scopedCommitmentBehavior = commitmentBehavior.ForCluster()
			} else {
				scopedCommitmentBehavior = commitmentBehavior.ForDomain(domainName)
			}
			serviceReport.Categories[category].Resources[resourceName] = resourcesv2.ResourceInfoReport{
				DisplayName:      resource.DisplayName,
				Unit:             resource.Unit,
				Topology:         resource.Topology,
				HasCapacity:      resource.HasCapacity,
				HasQuota:         resource.HasQuota,
				CommitmentConfig: scopedCommitmentBehavior.ForV2API(timeNow),
			}
		}
	}
	return report, nil
}

// GetRatesInfo returns a ratesv2.InfoReport, which can be exposed via
// an own endpoint or re-used in rate reports.
func GetRatesInfo(cluster *core.Cluster, token *gopherpolicy.Token, sis core.ServiceInfoReader) (ratesv2.InfoReport, error) {
	var none ratesv2.InfoReport // used on error return paths only

	_, _, _, err := authenticateInfoRequest(token)
	if err != nil {
		return none, err
	}

	// assemble the report
	report := ratesv2.InfoReport{
		AllAZs: cluster.Config.AvailabilityZones,
		Areas:  make(map[string]ratesv2.AreaInfoReport),
	}
	services := sis.GetServices()
	categories := sis.GetCategories()

	for _, serviceType := range slices.Sorted(maps.Keys(services)) {
		service := services[serviceType]
		rates, _ := sis.GetRatesForType(serviceType) // can have no rates
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

		for _, rateName := range slices.Sorted(maps.Keys(rates)) {
			rate := rates[rateName]
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
			category := liquid.CategoryName(serviceType)
			categoryDisplayName := service.DisplayName
			if categoryID, exists := rate.CategoryID.Unpack(); exists {
				category = categories[categoryID].Name
				categoryDisplayName = categories[categoryID].DisplayName
			}
			if _, exists := serviceReport.Categories[category]; !exists {
				serviceReport.Categories[category] = ratesv2.CategoryInfoReport{
					DisplayName: categoryDisplayName,
					Rates:       make(map[liquid.RateName]ratesv2.RateInfoReport),
				}
			}
			serviceReport.Categories[category].Rates[rateName] = rir
		}
	}
	return report, nil
}
