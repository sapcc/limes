// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/go-gorp/gorp/v3"
	"github.com/lib/pq"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/db"

	"github.com/gorilla/mux"
	. "go.xyrillian.de/gg/option"
)

// ScopeConfig is used to construct a Scope and specifies which parameters to validate
// in the given request.
type ScopeConfig struct {
	AllowQueryProjectID  bool
	AllowQueryDomainID   bool
	RequirePathDomainID  bool
	RequirePathProjectID bool
}

// Scope describes the object Scope of a validated request.
// Currently, there is no option to have a Scope with more than one domain or project.
// If Project.IsSome(), then Domain.IsSome() too.
type Scope struct {
	Domain  Option[db.Domain]
	Project Option[db.Project]
}

// NewScope checks the given request against the ScopeConfig and project/ domain data
// accessed with dbm. It returns the validated scope information and a success bool.
func (sc ScopeConfig) NewScope(w http.ResponseWriter, r *http.Request, dbm *gorp.DbMap) (s Scope, success bool) {
	queryDomainIDs := r.URL.Query()["domain_id"]
	queryProjectIDs := r.URL.Query()["project_id"]
	pathDomainID := mux.Vars(r)["domain_id"]
	pathProjectID := mux.Vars(r)["project_id"]
	var (
		filter string
		args   []any
	)

	if sc.RequirePathProjectID || sc.AllowQueryProjectID {
		switch {
		case sc.RequirePathProjectID && pathProjectID == "":
			http.Error(w, "project ID missing", http.StatusBadRequest)
			return s, false
		case sc.RequirePathProjectID:
			filter = "uuid = $1"
			args = []any{pathProjectID}
		case sc.AllowQueryProjectID && len(queryProjectIDs) > 1:
			http.Error(w, "cannot filter multiple project ids", http.StatusBadRequest)
			return s, false
		case sc.AllowQueryProjectID && len(queryProjectIDs) == 1:
			filter = "uuid = $1"
			args = []any{queryProjectIDs[0]}
		}
		var project db.Project
		err := dbm.SelectOne(&project, "SELECT * FROM projects WHERE "+filter, args...)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			http.Error(w, "no such domain", http.StatusNotFound)
			return s, false
		case respondwith.ObfuscatedErrorText(w, err):
			return s, false
		default:
			s.Project = Some(project)
		}
	}

	switch {
	case sc.RequirePathDomainID && pathDomainID == "":
		http.Error(w, "domain ID missing", http.StatusBadRequest)
		return s, false
	case sc.RequirePathDomainID:
		filter = "uuid = $1"
		args = []any{pathDomainID}
	case sc.AllowQueryDomainID && len(queryDomainIDs) > 1:
		http.Error(w, "cannot filter for multiple domain ids", http.StatusBadRequest)
		return s, false
	case sc.AllowQueryDomainID && len(queryDomainIDs) > 0:
		filter = "uuid = $1"
		args = []any{pq.Array(queryDomainIDs)}
	case s.Project.IsSome():
		filter = "id = $1"
		args = []any{s.Project.UnwrapOrPanic("projects was checked to be Some()").DomainID}
	default:
		return s, true
	}
	var domain db.Domain
	err := dbm.SelectOne(&domain, "SELECT * FROM domains WHERE "+filter, args...)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		// error for RequirePathDomainID or AllowQueryDomainID, for project.DomainID this is just defense in depth
		http.Error(w, "no such project", http.StatusNotFound)
		return s, false
	case respondwith.ObfuscatedErrorText(w, err):
		return s, false
	default:
		s.Domain = Some(domain)
		return s, true
	}
}

// UpdateTokenContext should be used on APIs where the ScopeConfig has
// AllowQueryProjectID or AllowQueryDomainID enabled, so that
// token.Check and token.Require validate the correct scope.
func (s Scope) UpdateTokenContext(token *gopherpolicy.Token) {
	if d, ok := s.Domain.Unpack(); ok {
		token.Context.Request["domain_id"] = d.UUID
	}
	if p, ok := s.Project.Unpack(); ok {
		token.Context.Request["project_id"] = string(p.UUID)
	}
}
