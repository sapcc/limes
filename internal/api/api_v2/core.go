// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/gorilla/mux"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
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
	tv := p.tokenValidator
	r.Methods("GET").Path("/resources/v2/info").HandlerFunc(handlerFunc(http.StatusOK, tv, p.handleGetResourcesInfo))
	r.Methods("GET").Path("/rates/v2/info").HandlerFunc(handlerFunc(http.StatusOK, tv, p.handleGetRatesInfo))
}

// Wrapper for request handlers that enforces a structure,
// where the request handler function has an error return
// instead of being able to randomly use `w` at any point.
//
// This wrapper also performs AuthN and provides a parsed token to the actual request handler.
// The wrapper needs the token to decide whether to use error obfuscation.
func handlerFunc[T any](successCode int, tv gopherpolicy.Validator, action func(*http.Request, *gopherpolicy.Token) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t := tv.CheckToken(r)

		var (
			resp T
			err  error
		)
		if t.Err == nil {
			resp, err = action(r, t)
		} else {
			// fast exit for AuthN errors: `t.Enforce()` renders the 401 error in the standard way
			// (using a dummy policy rule; the specific value does not matter because AuthZ is not done when AuthN has already failed)
			err = t.Enforce("v2:unknown:unknown")
			// defense in depth: t.Enforce() should definitely return an error because `t.Err` is non-nil
			err = cmp.Or(err, t.Err)
		}

		if err != nil {
			// This is intentionally a double-negative: If the rule does not exist,
			// Check() returns false, and we get the safe behavior of obfuscating everything.
			if t.Check("v2:meta:no_error_obfuscation") {
				respondwith.ErrorText(w, err)
			} else {
				respondwith.ObfuscatedErrorText(w, err)
			}
			return
		}

		if successCode == http.StatusNoContent {
			w.WriteHeader(http.StatusNoContent)
		} else {
			respondwith.JSON(w, successCode, resp)
		}
	}
}

// checkProjectAccess authenticates and authorizes a project-scoped request using the given policy rule.
// On success, returns the database records for the project scope, its containing domain and the authenticated token.
func (p *v2Provider) checkProjectAccess(t *gopherpolicy.Token, projectUUID, policyRule string) (_ db.Domain, _ db.Project, err error) { //nolint:unused // TODO: remove this nolint once it is used
	// NOTE: This method is written in a way that obfuscates "domain not found"
	// errors to users without successful authorization (including by timing side-channel).

	// find the domain belonging to this project
	var domain db.Domain
	err = p.DB.SelectOne(&domain,
		`SELECT d.* FROM domains d JOIN projects p ON d.id = p.domain_id WHERE p.uuid = $1`,
		projectUUID,
	)
	switch {
	case err == nil:
		t.Context.Request = map[string]string{
			"domain_id":  domain.UUID,
			"project_id": projectUUID,
		}
		err = t.Enforce(policyRule)
		if err != nil {
			return
		}
	case errors.Is(err, sql.ErrNoRows):
		t.Context.Request = map[string]string{
			"domain_id":  "unknown",
			"project_id": projectUUID,
		}
		err = t.Enforce(policyRule)
		if err == nil {
			err = fmt.Errorf("no such project (UUID = %s)", projectUUID)
			err = respondwith.CustomStatus(http.StatusNotFound, err)
		}
		return
	default:
		return
	}

	var project db.Project
	err = p.DB.SelectOne(&project, `SELECT * FROM projects WHERE uuid = $1`, projectUUID)
	if errors.Is(err, sql.ErrNoRows) {
		// defense in depth: this branch should not be reachable if the first database query found a result
		err = fmt.Errorf("no such project (UUID = %s)", projectUUID)
		err = respondwith.CustomStatus(http.StatusNotFound, err)
	}
	return domain, project, err
}
