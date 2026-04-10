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
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"

	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/db"

	. "github.com/majewsky/gg/option"
)

var findAllowedResources = sqlext.SimplifyWhitespace(`
	SELECT DISTINCT s.type, r.name
	FROM project_resources pr
	JOIN resources r ON pr.resource_id = r.id
	JOIN services s ON r.service_id = s.id
	JOIN projects p ON pr.project_id = p.id
	JOIN domains d ON p.domain_id = d.id
	WHERE ((p.uuid = $1 OR $1 = '') AND (d.uuid = $2 OR $2 = ''))
	AND pr.forbidden = false
`)

func (p *v2Provider) authenticateInfoRequest(r *http.Request) (token *gopherpolicy.Token, projectUUID, domainUUID string, err error) {
	token = p.CheckToken(r)
	projectUUID = token.ProjectScopeUUID()
	projectDomainUUID := token.ProjectScopeDomainUUID()
	domainUUID = token.DomainScopeUUID()

	// a token.Require() check below cloud-level is only successful, when scope is in the context
	// usually, this would come from vars in the URL
	if projectUUID != "" {
		token.Context.Request["project_id"] = projectUUID
		token.Context.Request["domain_id"] = projectDomainUUID
	}
	if domainUUID != "" {
		token.Context.Request["domain_id"] = domainUUID
	}

	switch {
	case token.Check("v2:cluster:info"):
		return token, "", "", nil
	case token.Check("v2:domain:info"):
		return token, "", domainUUID, nil
	case token.Check("v2:project:info"):
		return token, projectUUID, projectDomainUUID, nil
	default:
		return nil, "", "", respondwith.CustomStatus(http.StatusUnauthorized, errors.New("unauthorized"))
	}
}

// GetResourcesInfo handles GET /resources/v2/info.
func (p *v2Provider) GetResourcesInfo(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/info")

	token, projectUUID, domainUUID, err := p.authenticateInfoRequest(r)
	if respondwith.ErrorText(w, err) {
		return
	}

	// collect allowed items for this user
	allowedResourcesByService := make(map[db.ServiceType][]liquid.ResourceName)
	err = sqlext.ForeachRow(p.DB, findAllowedResources, []any{projectUUID, domainUUID}, func(rows *sql.Rows) error {
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
			unit := None[liquid.Unit]()
			if !resource.Unit.IsZero() {
				unit = Some(resource.Unit)
			}
			scopedCommitmentBehavior := p.Cluster.CommitmentBehaviorForResource(serviceType, resourceName).ForCluster()
			if domainUUID != "" && projectUUID == "" {
				scopedCommitmentBehavior = p.Cluster.CommitmentBehaviorForResource(serviceType, resourceName).ForDomain(token.DomainScopeName())
			}
			if domainUUID != "" && projectUUID != "" {
				scopedCommitmentBehavior = p.Cluster.CommitmentBehaviorForResource(serviceType, resourceName).ForDomain(token.ProjectScopeDomainName())
			}
			serviceReport.Categories[category].Resources[resourceName] = resourcesv2.ResourceInfoReport{
				DisplayName:      resource.DisplayName,
				Unit:             unit,
				Topology:         resource.Topology,
				HasCapacity:      resource.HasCapacity,
				HasQuota:         resource.HasQuota,
				CommitmentConfig: scopedCommitmentBehavior.ForV2API(p.timeNow()),
			}
		}
	}
	respondwith.JSON(w, 200, report)
}

// GetRatesInfo handles GET /rates/v2/info.
func (p *v2Provider) GetRatesInfo(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/info")

	_, projectUUID, domainUUID, err := p.authenticateInfoRequest(r)
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
			}
			if !rate.Unit.IsZero() {
				rir.Unit = Some(rate.Unit)
			}
			if rateConfig, ok := config.RateLimits.GetGlobalDefaultRateLimit(rateName); domainUUID == "" && projectUUID == "" && ok {
				rir.DefaultLimit = rateConfig.Limit
				rir.DefaultWindow = Some(rateConfig.Window)
			}
			if rateConfig, ok := config.RateLimits.GetProjectDefaultRateLimit(rateName); domainUUID != "" && projectUUID != "" && ok {
				rir.DefaultLimit = rateConfig.Limit
				rir.DefaultWindow = Some(rateConfig.Window)
			}
			serviceReport.Rates[rateName] = rir
		}
	}
	respondwith.JSON(w, 200, report)
}
