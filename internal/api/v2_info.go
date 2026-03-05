// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"database/sql"
	"net/http"
	"slices"

	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"
	"github.com/sapcc/go-bits/sqlext"

	"github.com/sapcc/limes/internal/db"
)

// V2InfoReport is the response type for GET /v2/info.
// It contains all metadata information about the clusters services.
type V2InfoReport struct {
	ServiceAreas map[string]V2ServiceAreaReport `json:"service_areas"`
}

// V2ServiceAreaReport groups services into areas, which are defined in the config.
// It appears in V2InfoReport.
type V2ServiceAreaReport struct {
	// DisplayName string `json:"display_name"` // TODO: needed?
	Services map[db.ServiceType]V2ServiceInfoReport `json:"services"`
}

// V2ServiceInfoReport contains details about a service.
// It appears in V2ServiceAreaReport.
type V2ServiceInfoReport struct {
	Version            int64                                `json:"version"`
	DisplayName        string                               `json:"display_name"`
	ResourceCategories map[string]V2ServiceCategoryReport   `json:"resources"`
	Rates              map[liquid.RateName]V2RateInfoReport `json:"rates"`
}

// V2ServiceCategoryReport groups resources into categories, which are defined in the config.
// It appears in V2ServiceInfoReport.
type V2ServiceCategoryReport struct {
	// DisplayName string `json:"display_name"` // TODO: needed?
	Resources map[liquid.ResourceName]V2ResourceInfoReport `json:"resources"`
}

// V2ResourceInfoReport contains details about a resource.
// It appears in V2ServiceCategoryReport.
type V2ResourceInfoReport struct {
	DisplayName      string                                  `json:"display_name"`
	Unit             liquid.Unit                             `json:"unit,omitempty"`
	Topology         liquid.Topology                         `json:"topology"`
	HasCapacity      bool                                    `json:"has_capacity"`
	HasQuota         bool                                    `json:"has_quota"`
	CommitmentConfig *limesresources.CommitmentConfiguration `json:"commitment_config,omitempty"`
}

// V2RateInfoReport contains details about a rate.
// It appears in V2ServiceInfoReport.
type V2RateInfoReport struct {
	DisplayName string             `json:"display_name"`
	Unit        liquid.Unit        `json:"unit,omitempty"`
	Topology    liquid.Topology    `json:"topology"`
	HasUsage    bool               `json:"has_usage"`
	Limits      *V2RateLimitReport `json:"limits,omitempty"`
}

// V2RateLimitReport contains details about the limits of a rate.
// Default limits might exist on cluster and project level.
// Additionally, the local limit might be set on project level.
// This object cannot exist on domain level.
type V2RateLimitReport struct {
	Limit         uint64             `json:"limit,omitempty"`
	Window        *limesrates.Window `json:"window,omitempty"`
	DefaultLimit  uint64             `json:"default_limit,omitempty"`
	DefaultWindow *limesrates.Window `json:"default_window,omitempty"`
}

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

// TODO: Implement path on rates - moving this TODO from the test setup to here
// TODO: related: I found that in the tests, rates have the full path, this is wrong IMHO?
var findRateLimitForProject = sqlext.SimplifyWhitespace(`
	SELECT pra.rate_limit, pra.window_ns
	FROM project_rates pra
	JOIN rates r ON pra.rate_id = r.id
	JOIN services s ON r.service_id = s.id
	JOIN projects p ON pra.project_id = p.id
	WHERE p.uuid = $1
	AND s.type = $2
	AND r.name = $3
`)

// GetInfo handles GET /v2/info.
func (p *v2Provider) GetInfo(w http.ResponseWriter, r *http.Request) {
	httpapi.IdentifyEndpoint(r, "/v2/info")
	token := p.CheckToken(r)
	projectUUID := token.ProjectScopeUUID()
	projectDomainUUID := token.ProjectScopeDomainUUID()
	domainUUID := token.DomainScopeUUID()

	// a token.Require() check below cloud-level is only successful, when scope is in the context
	// usually, this would come from vars in the URL
	if projectUUID != "" {
		token.Context.Request["project_id"] = projectUUID
		token.Context.Request["domain_id"] = projectDomainUUID
	}
	if domainUUID != "" {
		token.Context.Request["domain_id"] = domainUUID
	}

	// the user must have any viewer permission
	if !token.Require(w, "project:show") {
		return
	}

	// collect allowed items for this user
	allowedResourcesByService := make(map[db.ServiceType][]liquid.ResourceName)
	if token.Check("cluster:show") {
		// a cluster admin can see everything, regardless of the scope of his token
		projectUUID = ""
		domainUUID = ""
	}
	err := sqlext.ForeachRow(p.DB, findAllowedResources, []any{projectUUID, domainUUID}, func(rows *sql.Rows) error {
		var (
			serviceType  db.ServiceType
			resourceName liquid.ResourceName
		)
		err := rows.Scan(&serviceType, &resourceName)
		if err == nil {
			if _, exists := allowedResourcesByService[serviceType]; !exists {
				allowedResourcesByService[serviceType] = make([]liquid.ResourceName, 0)
			}
			allowedResourcesByService[serviceType] = append(allowedResourcesByService[serviceType], resourceName)
		}
		return err
	})
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	// assemble the report
	report := V2InfoReport{
		ServiceAreas: make(map[string]V2ServiceAreaReport),
	}
	serviceInfos, err := p.Cluster.AllServiceInfos()
	if respondwith.ObfuscatedErrorText(w, err) {
		return
	}

	for serviceType, serviceInfo := range serviceInfos {
		// TODO: Do we need to do a v2-name-mapping or are the v2 names 1:1 from the database and the
		// display names are sufficient?

		// skip non-allowed resources for this user, if any
		allowedResources, serviceTypeOK := allowedResourcesByService[serviceType]
		if !serviceTypeOK {
			continue
		}
		config := p.Cluster.ConfigForService(serviceType)
		area := config.Area
		// defense in depth: config should be in sync with serviceInfo
		if area == "" {
			continue
		}
		if _, exists := report.ServiceAreas[area]; !exists {
			report.ServiceAreas[area] = V2ServiceAreaReport{
				Services: make(map[db.ServiceType]V2ServiceInfoReport),
			}
		}
		report.ServiceAreas[area].Services[serviceType] = V2ServiceInfoReport{
			Version:            serviceInfo.Version,
			DisplayName:        "", // TODO: wait for liquid interface change
			ResourceCategories: make(map[string]V2ServiceCategoryReport),
			Rates:              make(map[liquid.RateName]V2RateInfoReport),
		}
		serviceReport := report.ServiceAreas[area].Services[serviceType]

		for resourceName, resourceInfo := range serviceInfo.Resources {
			// skip non-allowed resources for this user, if any
			if !slices.Contains(allowedResources, resourceName) {
				continue
			}
			category := p.Cluster.BehaviorForResource(serviceType, resourceName).Category
			if category == "" {
				category = string(resourceName) // TODO: currently, a category is not required to be set via config! how to handle?
			}
			if _, exists := serviceReport.ResourceCategories[category]; !exists {
				serviceReport.ResourceCategories[category] = V2ServiceCategoryReport{
					Resources: make(map[liquid.ResourceName]V2ResourceInfoReport),
				}
			}
			scopedCommitmentBehavior := p.Cluster.CommitmentBehaviorForResource(serviceType, resourceName).ForCluster()
			if !token.Check("cluster:show") && token.Check("domain:show") {
				scopedCommitmentBehavior = p.Cluster.CommitmentBehaviorForResource(serviceType, resourceName).ForDomain(token.DomainScopeName())
			}
			if !token.Check("cluster:show") && !token.Check("domain:show") {
				scopedCommitmentBehavior = p.Cluster.CommitmentBehaviorForResource(serviceType, resourceName).ForDomain(token.ProjectScopeDomainName())
			}
			serviceReport.ResourceCategories[category].Resources[resourceName] = V2ResourceInfoReport{
				DisplayName:      "", // TODO: wait for liquid interface change
				Unit:             resourceInfo.Unit,
				Topology:         resourceInfo.Topology,
				HasCapacity:      resourceInfo.HasCapacity,
				HasQuota:         resourceInfo.HasQuota,
				CommitmentConfig: scopedCommitmentBehavior.ForAPI(p.timeNow()).AsPointer(),
			}
		}

		for rateName, rateInfo := range serviceInfo.Rates {
			var rateLimits *V2RateLimitReport
			if rateConfig, ok := config.RateLimits.GetGlobalDefaultRateLimit(rateName); ok && token.Check("cluster:show") {
				rateLimits = &V2RateLimitReport{
					DefaultLimit:  rateConfig.Limit,
					DefaultWindow: &rateConfig.Window,
				}
			} else if token.Check("project:show") && !token.Check("domain:show") {
				// TODO: I find it to be clearer for the customer, if we show the default limits separately. Ok?
				if rateConfig, ok := config.RateLimits.GetProjectDefaultRateLimit(rateName); ok {
					rateLimits = &V2RateLimitReport{
						DefaultLimit:  rateConfig.Limit,
						DefaultWindow: &rateConfig.Window,
					}
				}

				var (
					projectLimit  *uint64
					projectWindow *limesrates.Window
				)
				err = p.DB.QueryRow(findRateLimitForProject, token.ProjectScopeUUID(), serviceType, rateName).
					Scan(&projectLimit, &projectWindow)
				// the rate should always be there, only the values could be null
				if respondwith.ObfuscatedErrorText(w, err) {
					return
				}
				if projectLimit != nil && projectWindow != nil {
					if rateLimits == nil {
						rateLimits = &V2RateLimitReport{}
					}
					rateLimits.Limit = *projectLimit
					rateLimits.Window = projectWindow
				}
			}
			serviceReport.Rates[rateName] = V2RateInfoReport{
				DisplayName: "", // TODO: wait for liquid interface change
				Unit:        rateInfo.Unit,
				Topology:    rateInfo.Topology,
				HasUsage:    rateInfo.HasUsage,
				Limits:      rateLimits,
			}
		}
	}
	respondwith.JSON(w, 200, report)
}
