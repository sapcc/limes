// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import (
	"bytes"
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/gorilla/mux"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
)

type v2Provider struct {
	Cluster        *core.Cluster
	DB             *gorp.DbMap
	DomainNames    Option[DomainNames]
	tokenValidator gopherpolicy.Validator
	auditor        audittools.Auditor

	// slots for test doubles
	timeNow func() time.Time
}

// NewV2API creates an httpapi.API that serves the Limes v2 API.
// It also returns the VersionData for this API version which is needed for the
// version advertisement on "GET /".
func NewV2API(cluster *core.Cluster, domainNames Option[DomainNames], tokenValidator gopherpolicy.Validator, auditor audittools.Auditor, timeNow func() time.Time) httpapi.API {
	return &v2Provider{Cluster: cluster, DB: cluster.DB, DomainNames: domainNames, tokenValidator: tokenValidator, auditor: auditor, timeNow: timeNow}
}

// AddTo implements the httpapi.API interface.
func (p *v2Provider) AddTo(r *mux.Router) {
	resRouter := r.PathPrefix("/resources/v2/").Subrouter()
	ratesRouter := r.PathPrefix("/rates/v2/").Subrouter()

	if apiDomainNames, ok := p.DomainNames.Unpack(); ok {
		mw := EnforceDomainName(apiDomainNames.V2)
		resRouter.Use(mw)
		ratesRouter.Use(mw)
	}

	tv := p.tokenValidator
	resRouter.Methods("GET").Path("/info").HandlerFunc(handlerFunc(http.StatusOK, tv, p.handleGetResourcesInfo))
	ratesRouter.Methods("GET").Path("/info").HandlerFunc(handlerFunc(http.StatusOK, tv, p.handleGetRatesInfo))
	resRouter.Methods("POST").Path("/commitments/new").HandlerFunc(handlerFunc(http.StatusCreated, tv, p.handlePostNewCommitment))
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

// parseRequestBodyAs unmarshals a JSON-encoded request body.
func parseRequestBodyAs[T any](r *http.Request) (T, error) {
	// TODO: With how clever this function is now, it probably should be in go-bits.
	var result T

	// To guard against complexity attacks using extremely large request bodies,
	// we never read more than 8 KiB. There are no request types in the v2 API
	// that could ever require more than that.
	const maxRequestSize = 8192
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxRequestSize))
	if err != nil {
		return result, fmt.Errorf("while reading request body: %w", err)
	}
	if len(buf) == maxRequestSize {
		// looks like we could have read more if we wanted to
		err = errors.New("request body too large")
		return result, respondwith.CustomStatus(http.StatusRequestEntityTooLarge, err)
	}

	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.DisallowUnknownFields()
	err = dec.Decode(&result)
	if err != nil {
		return result, respondwith.CustomStatus(http.StatusBadRequest,
			fmt.Errorf("request body is not valid JSON: %w", err),
		)
	}

	// Decoder.Decode() only reads until the end of a JSON payload, which may be before the end of `buf`;
	// complain if there is anything of substance after the JSON payload (e.g. another JSON payload)
	remainder, err := io.ReadAll(dec.Buffered())
	if err != nil {
		// defense in depth: reading from a buffer should never fail
		return result, fmt.Errorf("unexpected error when checking buffer remains in parseRequestBodyAs: %w", err)
	}
	if len(bytes.TrimSpace(remainder)) > 0 {
		return result, respondwith.CustomStatus(http.StatusBadRequest,
			fmt.Errorf("request body contains %d unexpected bytes after the JSON payload", len(remainder)),
		)
	}

	return result, nil
}

// checkProjectAccess authenticates and authorizes a project-scoped request using the given policy rule.
// On success, returns the database records for the project scope, its containing domain and the authenticated token.
func (p *v2Provider) checkProjectAccess(t *gopherpolicy.Token, projectUUID liquid.ProjectUUID, policyRule string) (_ db.Domain, _ db.Project, err error) {
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
			"project_id": string(projectUUID),
		}
		err = t.Enforce(policyRule)
		if err != nil {
			return
		}
	case errors.Is(err, sql.ErrNoRows):
		t.Context.Request = map[string]string{
			"domain_id":  "unknown",
			"project_id": string(projectUUID),
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
