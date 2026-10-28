// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"net/http"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"

	"github.com/sapcc/limes/internal/api/reports_v2"
	ratesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/rates"
	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
)

// handleGetResourcesInfo handles GET /resources/v2/info.
func (p *v2Provider) handleGetResourcesInfo(r *http.Request, token *gopherpolicy.Token) (resourcesv2.InfoReport, error) {
	httpapi.IdentifyEndpoint(r, "/resources/v2/info")
	infoScope, err := p.resolveInfoScope(r, token)
	if err != nil {
		return resourcesv2.InfoReport{}, err
	}
	return reports_v2.GetResourcesInfo(r.Context(), p.Cluster, infoScope, p.timeNow(), p.Cluster.SIC.GetSnapshot())
}

// handleGetRatesInfo handles GET /rates/v2/info.
func (p *v2Provider) handleGetRatesInfo(r *http.Request, token *gopherpolicy.Token) (ratesv2.InfoReport, error) {
	httpapi.IdentifyEndpoint(r, "/rates/v2/info")
	infoScope, err := p.resolveInfoScope(r, token)
	if err != nil {
		return ratesv2.InfoReport{}, err
	}
	return reports_v2.GetRatesInfo(p.Cluster, infoScope, p.Cluster.SIC.GetSnapshot())
}

// resolveInfoScope determines the InfoScope for an info request by considering
// optional query parameters (?project_uuid, ?domain_uuid) that can narrow the
// scope beyond what the token alone provides.
func (p *v2Provider) resolveInfoScope(r *http.Request, token *gopherpolicy.Token) (reports_v2.InfoScope, error) {
	queryProjectUUID := r.URL.Query().Get("project_uuid")
	queryDomainUUID := r.URL.Query().Get("domain_uuid")

	if queryProjectUUID != "" {
		scope, err := p.checkProjectAccess(r.Context(), token, liquid.ProjectUUID(queryProjectUUID), "v2:project:info")
		if err != nil {
			return reports_v2.InfoScope{}, err
		}
		return reports_v2.InfoScope{
			ProjectUUID: string(scope.Project.UUID),
			DomainUUID:  scope.Domain.UUID,
			DomainName:  scope.Domain.Name,
		}, nil
	}

	if queryDomainUUID != "" {
		scope, err := p.checkDomainAccess(r.Context(), token, queryDomainUUID, "v2:domain:info")
		if err != nil {
			return reports_v2.InfoScope{}, err
		}
		return reports_v2.InfoScope{
			DomainUUID: scope.Domain.UUID,
			DomainName: scope.Domain.Name,
		}, nil
	}

	return reports_v2.AuthenticateInfoRequest(token)
}
