// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"database/sql"
	"errors"
	"maps"
	"net/http"
	"slices"

	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"

	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
	"github.com/sapcc/limes/internal/db"

	. "github.com/majewsky/gg/option"
	"github.com/majewsky/gg/options"
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

var findRateLimitForProject = sqlext.SimplifyWhitespace(`
	SELECT pra.rate_limit, pra.window_ns
	FROM project_rates pra
	JOIN rates r ON pra.rate_id = r.id
	JOIN services s ON r.service_id = s.id
	JOIN projects p ON pra.project_id = p.id
	WHERE (p.uuid = $1 OR $1 = '')
	AND s.type = $2
	AND r.name = $3
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
		Areas: make(map[string]resourcesv2.AreaReport),
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
			report.Areas[area] = resourcesv2.AreaReport{
				DisplayName: areaDisplayName,
				Services:    make(map[db.ServiceType]resourcesv2.ServiceInfoReport),
			}
		}
		report.Areas[area].Services[serviceType] = resourcesv2.ServiceInfoReport{
			Version:     service.LiquidVersion,
			DisplayName: service.DisplayName,
			Categories:  make(map[liquid.CategoryName]resourcesv2.CategoryReport),
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
				serviceReport.Categories[category] = resourcesv2.CategoryReport{
					DisplayName: categoryDisplayName,
					Resources:   make(map[liquid.ResourceName]resourcesv2.ResourceInfoReport),
				}
			}
			unit := None[liquid.Unit]()
			if !resource.Unit.IsZero() {
				unit = Some(resource.Unit)
			}
			scopedCommitmentBehavior := p.Cluster.CommitmentBehaviorForResource(serviceType, resourceName).ForCluster()
			if !token.Check("cluster:show") && token.Check("domain:show") {
				scopedCommitmentBehavior = p.Cluster.CommitmentBehaviorForResource(serviceType, resourceName).ForDomain(token.DomainScopeName())
			}
			if !token.Check("cluster:show") && !token.Check("domain:show") {
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

	token, projectUUID, _, err := p.authenticateInfoRequest(r)
	if respondwith.ErrorText(w, err) {
		return
	}

	// assemble the report
	report := ratesv2.InfoReport{
		Areas: make(map[string]ratesv2.AreaReport),
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
			report.Areas[area] = ratesv2.AreaReport{
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
			var rateLimits Option[ratesv2.RateLimitReport]
			if rateConfig, ok := config.RateLimits.GetGlobalDefaultRateLimit(rateName); ok && token.Check("cluster:show") {
				rateLimits = Some(ratesv2.RateLimitReport{
					DefaultLimit:  rateConfig.Limit,
					DefaultWindow: Some(rateConfig.Window),
				})
			} else if token.Check("project:show") && !token.Check("domain:show") {
				// TODO: I find it to be clearer for the customer, if we show the default limits separately. Ok?
				if rateConfig, ok := config.RateLimits.GetProjectDefaultRateLimit(rateName); ok {
					rateLimits = Some(ratesv2.RateLimitReport{
						DefaultLimit:  rateConfig.Limit,
						DefaultWindow: Some(rateConfig.Window),
					})
				}

				var (
					projectLimit  *uint64
					projectWindow *limesrates.Window
				)
				err = p.DB.QueryRow(findRateLimitForProject, projectUUID, serviceType, rateName).
					Scan(&projectLimit, &projectWindow)
				// the rate should always be there, only the values could be null
				if respondwith.ObfuscatedErrorText(w, err) {
					return
				}
				if projectLimit != nil && projectWindow != nil {
					rl, exists := rateLimits.Unpack()
					if !exists {
						rl = ratesv2.RateLimitReport{}
					}
					rl.Limit = *projectLimit
					rl.Window = options.FromPointer(projectWindow)
					rateLimits = Some(rl)
				}
			}
			unit := None[liquid.Unit]()
			if !rate.Unit.IsZero() {
				unit = Some(rate.Unit)
			}
			serviceReport.Rates[rateName] = ratesv2.RateInfoReport{
				DisplayName: rate.DisplayName,
				Unit:        unit,
				Topology:    rate.Topology,
				HasUsage:    rate.HasUsage,
				Limits:      rateLimits,
			}
		}
	}
	respondwith.JSON(w, 200, report)
}
