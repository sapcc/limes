// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"database/sql"
	"maps"
	"net/http"
	"slices"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/sqlext"
	. "go.xyrillian.de/gg/option"

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

func (p *v2Provider) authenticateInfoRequest(token *gopherpolicy.Token) (projectUUID, domainUUID, domainName string, err error) {
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

// handleGetResourcesInfo handles GET /resources/v2/info.
func (p *v2Provider) handleGetResourcesInfo(r *http.Request, token *gopherpolicy.Token) (resourcesv2.InfoReport, error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/info")
	var none resourcesv2.InfoReport // used on error return paths only

	projectUUID, domainUUID, domainName, err := p.authenticateInfoRequest(token)
	if err != nil {
		return none, err
	}

	// collect allowed items for this user
	allowedResourcesByService := make(map[db.ServiceType][]liquid.ResourceName)
	err = sqlext.ForeachRow(p.DB, findAllowedResourcesQuery, []any{projectUUID, domainUUID}, func(rows *sql.Rows) error {
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
		Areas: make(map[string]resourcesv2.AreaInfoReport),
	}
	services := p.Cluster.SIC.GetServices()
	resources := p.Cluster.SIC.GetResources()
	categories := p.Cluster.SIC.GetCategories()

	for _, serviceType := range slices.Sorted(maps.Keys(services)) {
		service := services[serviceType]
		// skip non-allowed resources for this user, if any
		allowedResources, serviceTypeOK := allowedResourcesByService[serviceType]
		if !serviceTypeOK {
			continue
		}
		config := p.Cluster.Config.Liquids[serviceType]
		area := config.Area
		// defense in depth: config should be in sync with serviceInfo
		if area == "" {
			continue
		}
		areaDisplayName := ""
		// defense in depth: when area was found in config, a displayName has to be available
		if areaConfig, exists := p.Cluster.Config.Areas[area]; exists {
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

		for _, resourceName := range slices.Sorted(maps.Keys(resources[serviceType])) {
			resource := resources[serviceType][resourceName]
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
			commitmentBehavior := p.Cluster.CommitmentBehaviorForResource(serviceType, resourceName)
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
				CommitmentConfig: scopedCommitmentBehavior.ForV2API(p.timeNow()),
			}
		}
	}
	return report, nil
}

// handleGetRatesInfo handles GET /rates/v2/info.
func (p *v2Provider) handleGetRatesInfo(r *http.Request, token *gopherpolicy.Token) (ratesv2.InfoReport, error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/info")
	var none ratesv2.InfoReport // used on error return paths only

	_, _, _, err := p.authenticateInfoRequest(token)
	if err != nil {
		return none, err
	}

	// assemble the report
	report := ratesv2.InfoReport{
		Areas: make(map[string]ratesv2.AreaInfoReport),
	}
	services := p.Cluster.SIC.GetServices()
	rates := p.Cluster.SIC.GetRates()
	categories := p.Cluster.SIC.GetCategories()

	for _, serviceType := range slices.Sorted(maps.Keys(services)) {
		service := services[serviceType]
		config := p.Cluster.Config.Liquids[serviceType]
		area := config.Area
		// defense in depth: config should be in sync with serviceInfo
		if area == "" {
			continue
		}
		areaDisplayName := ""
		// defense in depth: when area was found in config, a displayName has to be available
		if areaConfig, exists := p.Cluster.Config.Areas[area]; exists {
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

		for _, rateName := range slices.Sorted(maps.Keys(rates[serviceType])) {
			rate := rates[serviceType][rateName]
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
