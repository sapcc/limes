// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"database/sql"
	"errors"
	"maps"
	"net/http"
	"slices"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"

	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"

	. "github.com/majewsky/gg/option"
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

func (p *v2Provider) authenticateInfoRequest(r *http.Request) (projectUUID, domainUUID, domainName string, err error) {
	token := p.CheckToken(r)
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
	case token.Check("v2:project:info"):
		return projectUUID, projectDomainUUID, projectDomainName, nil
	default:
		return "", "", "", respondwith.CustomStatus(http.StatusUnauthorized, errors.New("unauthorized"))
	}
}

// GetResourcesInfo handles GET /resources/v2/info.
func (p *v2Provider) GetResourcesInfo(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/info")

	projectUUID, domainUUID, domainName, err := p.authenticateInfoRequest(r)
	if respondwith.ErrorText(w, err) {
		return
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
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// assemble the report
	report := resourcesv2.InfoReport{
		Areas: make(map[string]resourcesv2.AreaInfoReport),
	}
	services := p.Cluster.ServiceInfoCache.GetServices()
	resources := p.Cluster.ServiceInfoCache.GetResources()
	categories := p.Cluster.ServiceInfoCache.GetCategories()

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

			switch {
			case domainUUID == "":
				scopedCommitmentBehavior = commitmentBehavior.ForCluster()
			case projectUUID == "":
				scopedCommitmentBehavior = commitmentBehavior.ForDomain(domainName)
			default:
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
	respondwith.JSON(w, http.StatusOK, report)
}

// GetRatesInfo handles GET /rates/v2/info.
func (p *v2Provider) GetRatesInfo(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/info")

	_, _, _, err := p.authenticateInfoRequest(r)
	if respondwith.ErrorText(w, err) {
		return
	}

	// assemble the report
	report := ratesv2.InfoReport{
		Areas: make(map[string]ratesv2.AreaInfoReport),
	}
	services := p.Cluster.ServiceInfoCache.GetServices()
	rates := p.Cluster.ServiceInfoCache.GetRates()

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
			Rates:       make(map[liquid.RateName]ratesv2.RateInfoReport),
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
			if rateConfig, ok := config.RateLimits.GetProjectDefaultRateLimit(rateName); ok {
				rir.ProjectDefaultLimit = rateConfig.Limit
				rir.ProjectDefaultWindow = Some(rateConfig.Window)
			}
			serviceReport.Rates[rateName] = rir
		}
	}
	respondwith.JSON(w, http.StatusOK, report)
}
