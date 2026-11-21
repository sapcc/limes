// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"net/http"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"

	"github.com/sapcc/limes/internal/api/reports_v2"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
)

// handleGetResourcesAvailability handles GET /v2/resources/availability.
func (p *v2Provider) handleGetResourcesAvailability(r *http.Request, token *gopherpolicy.Token) (resourcesv2.AvailabilityReport, error) {
	httpapi.IdentifyEndpoint(r, "/v2/resources/availability")
	scope, err := p.authenticateAvailabilityRequest(r, token)
	if err != nil {
		return resourcesv2.AvailabilityReport{}, err
	}
	return reports_v2.GetResourcesAvailability(r.Context(), p.Cluster, scope, p.Cluster.SIC.GetSnapshot())
}

// authenticateAvailabilityRequest resolves the scope from the token alone
// (no query-parameter narrowing). The spec says:
//   - A project-scoped token → resources available in that project.
//   - A domain-scoped token  → resources available in at least one project of that domain.
//   - A cloud-admin token    → all resources.
func (p *v2Provider) authenticateAvailabilityRequest(r *http.Request, token *gopherpolicy.Token) (reports_v2.Scope, error) {
	// broadest to narrowest
	err := token.Enforce("v2:cluster:report_availability")
	if err == nil {
		return reports_v2.ClusterScope{}, nil
	}
	if implicitDomainUUID := token.DomainScopeUUID(); implicitDomainUUID != "" {
		return p.checkDomainAccess(r.Context(), token, implicitDomainUUID, "v2:domain:report_availability")
	}
	if implicitProjectUUID := token.ProjectScopeUUID(); implicitProjectUUID != "" {
		return p.checkProjectAccess(r.Context(), token, liquid.ProjectUUID(implicitProjectUUID), "v2:project:report_availability")
	}
	return reports_v2.ClusterScope{}, err
}
