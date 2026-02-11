// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/gorilla/mux"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/core"
)

type v2Provider struct {
	Cluster        *core.Cluster
	DB             *gorp.DbMap
	VersionData    VersionData
	tokenValidator gopherpolicy.Validator
	auditor        audittools.Auditor

	// slots for test doubles
	timeNow func() time.Time
}

// VersionData is used by version advertisement handlers.
type VersionData struct {
	Status string            `json:"status"`
	ID     string            `json:"id"`
	Links  []VersionLinkData `json:"links"`
}

// VersionLinkData is used by version advertisement handlers, as part of the
// VersionData struct.
type VersionLinkData struct {
	URL      string `json:"href"`
	Relation string `json:"rel"`
	Type     string `json:"type,omitempty"`
}

// NewV2API creates an httpapi.API that serves the Limes v2 API.
// It also returns the VersionData for this API version which is needed for the
// version advertisement on "GET /".
func NewV2API(cluster *core.Cluster, tokenValidator gopherpolicy.Validator, auditor audittools.Auditor, timeNow func() time.Time) httpapi.API {
	p := &v2Provider{Cluster: cluster, DB: cluster.DB, tokenValidator: tokenValidator, auditor: auditor, timeNow: timeNow}
	p.VersionData = VersionData{
		Status: "CURRENT",
		ID:     "v2",
		Links: []VersionLinkData{
			{
				Relation: "self",
				URL:      p.Path(),
			},
			{
				Relation: "describedby",
				URL:      "https://github.com/sapcc/limes/blob/master/docs/users/api-v2-specification.md",
				Type:     "text/html",
			},
		},
	}

	return p
}

// AddTo implements the httpapi.API interface.
func (p *v2Provider) AddTo(r *mux.Router) {
	r.Methods("HEAD", "GET").Path("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpapi.IdentifyEndpoint(r, "/")
		httpapi.SkipRequestLog(r)
		respondwith.JSON(w, 300, map[string]any{"versions": []VersionData{p.VersionData}})
	})

	r.Methods("GET").Path("/v2/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpapi.IdentifyEndpoint(r, "/v2/")
		httpapi.SkipRequestLog(r)
		respondwith.JSON(w, 200, map[string]any{"version": p.VersionData})
	})

	r.Methods("GET").Path("/resources/v2/info").HandlerFunc(p.GetResourcesInfo)
	r.Methods("GET").Path("/rates/v2/info").HandlerFunc(p.GetRatesInfo)
}

// Path is a local helper to assemble api paths.
// TODO: refactor when v1 deleted
func Path(catalogURL, apiVersion string, elements ...string) string {
	parts := []string{strings.TrimSuffix(catalogURL, "/"), apiVersion}
	parts = append(parts, elements...)
	return strings.Join(parts, "/")
}

// Path constructs a full URL for a given URL path below the /v2/ endpoint.
func (p *v2Provider) Path(elements ...string) string {
	return Path(p.Cluster.Config.CatalogURL, "v2", elements...)
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
