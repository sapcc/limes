// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"net/http"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/gorilla/mux"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"

	"github.com/sapcc/limes/internal/core"
)

type v2Provider struct {
	Cluster        *core.Cluster
	DB             *gorp.DbMap
	tokenValidator gopherpolicy.Validator
	auditor        audittools.Auditor

	// slots for test doubles
	timeNow func() time.Time
}

// NewV2API creates an httpapi.API that serves the Limes v2 API.
// It also returns the VersionData for this API version which is needed for the
// version advertisement on "GET /".
func NewV2API(cluster *core.Cluster, tokenValidator gopherpolicy.Validator, auditor audittools.Auditor, timeNow func() time.Time) httpapi.API {
	return &v2Provider{Cluster: cluster, DB: cluster.DB, tokenValidator: tokenValidator, auditor: auditor, timeNow: timeNow}
}

// AddTo implements the httpapi.API interface.
func (p *v2Provider) AddTo(r *mux.Router) {
	r.Methods("GET").Path("/resources/v2/info").HandlerFunc(p.GetResourcesInfo)
	r.Methods("GET").Path("/resources/v2/cluster").HandlerFunc(p.GetResourcesCluster)
	r.Methods("GET").Path("/resources/v2/domains").HandlerFunc(p.GetResourcesDomains)
	r.Methods("GET").Path("/resources/v2/domains/{domain_id}").HandlerFunc(p.GetResourcesDomain)
	r.Methods("GET").Path("/resources/v2/projects").HandlerFunc(p.GetResourcesProjects)
	r.Methods("GET").Path("/resources/v2/projects/{project_id}").HandlerFunc(p.GetResourcesProject)

	r.Methods("GET").Path("/rates/v2/info").HandlerFunc(p.GetRatesInfo)
	r.Methods("GET").Path("/rates/v2/cluster").HandlerFunc(p.GetRatesCluster)
	r.Methods("GET").Path("/rates/v2/domains").HandlerFunc(p.GetRatesDomains)
	r.Methods("GET").Path("/rates/v2/domains/{domain_id}").HandlerFunc(p.GetRatesDomain)
	r.Methods("GET").Path("/rates/v2/projects").HandlerFunc(p.GetRatesProjects)
	r.Methods("GET").Path("/rates/v2/projects/{project_id}").HandlerFunc(p.GetRatesProject)
}

// CheckToken is a local helper to service the CheckToken functions of the different providers.
// TODO: refactor when v1 deleted
func CheckToken(r *http.Request, tokenValidator gopherpolicy.Validator) *gopherpolicy.Token {
	t := tokenValidator.CheckToken(r)
	t.Context.Request = mux.Vars(r)
	return t
}

// CheckToken checks the validity of the request's X-Auth-Token in Keystone, and
// returns a Token instance for checking authorization. Any errors that occur
// during this function are deferred until Require() is called.
func (p *v2Provider) CheckToken(r *http.Request) *gopherpolicy.Token {
	return CheckToken(r, p.tokenValidator)
}
