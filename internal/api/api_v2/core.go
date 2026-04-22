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
	r.Methods("GET").Path("/rates/v2/info").HandlerFunc(p.GetRatesInfo)
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
