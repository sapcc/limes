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
	"maps"
	"net/http"
	"time"

	"github.com/go-gorp/gorp/v3"
	"github.com/gorilla/mux"
	"github.com/sapcc/go-bits/audittools"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/httpapi"
	"github.com/sapcc/go-bits/respondwith"
	"go.xyrillian.de/oblast"

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
	r.Methods("GET").Path("/resources/v2/info").HandlerFunc(p.handleGetResourcesInfo)
	r.Methods("GET").Path("/rates/v2/info").HandlerFunc(p.handleGetRatesInfo)
	r.Methods("POST").Path("/resources/v2/commitments/new").HandlerFunc(handlerFunc(http.StatusCreated, p.handlePostNewCommitment))
}

// Wrapper for request handlers that enforces a structure,
// where the request handler function has an error return
// instead of being able to randomly use `w` at any point.
func handlerFunc[T any](successCode int, action func(r *http.Request) (T, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := action(r)
		if errUnpacked, ok := err.(errorWithResponseHeaders); ok { //nolint:errorlint
			maps.Copy(w.Header(), errUnpacked.Header)
			err = errUnpacked.Err
		}
		if respondwith.ObfuscatedErrorText(w, err) {
			return
		}
		if successCode == http.StatusNoContent {
			w.WriteHeader(http.StatusNoContent)
		} else {
			respondwith.JSON(w, successCode, resp)
		}
	}
}

// Extension for respondwith.CustomStatus() that allows setting response headers.
// TODO: upstream into go-bits (not like this, but in a way that solves the basic problem)
type errorWithResponseHeaders struct { //nolint:errorlint
	Header http.Header
	Err    error
}

// Error implements the builtin/error interface.
func (e errorWithResponseHeaders) Error() string {
	return e.Err.Error()
}

// Unpack implements the interface implied by package errors in std.
func (e errorWithResponseHeaders) Unpack() error {
	return e.Err
}

// checkProjectAccess authenticates and authorizes a project-scoped request using the given policy rule.
// On success, returns the database records for the project scope, its containing domain and the authenticated token.
func (p *v2Provider) checkProjectAccess(r *http.Request, projectUUID, policyRule string) (_ db.Domain, _ db.Project, _ *gopherpolicy.Token, err error) {
	// if AuthN fails, return immediately without trying to access the DB at all
	t := p.tokenValidator.CheckToken(r)
	if t.Err != nil {
		t.Context.Request = map[string]string{
			"domain_id":  "unknown",
			"project_id": projectUUID,
		}
		// defense in depth: t.Enforce() should definitely return an error because `t.Err` is non-nil
		err = cmp.Or(t.Enforce(policyRule), t.Err)
		return
	}

	// NOTE: This method is written in a way that obfuscates "domain not found"
	// errors to users without successful authorization (including by timing side-channel).

	// find the domain belonging to this project
	ctx := r.Context()
	dbh := oblast.NewDB(p.DB.Db)
	domain, err := db.DomainStore.SelectOne(ctx, dbh,
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

	project, err := db.ProjectStore.SelectOneWhere(ctx, dbh, `uuid = $1`, projectUUID)
	if errors.Is(err, sql.ErrNoRows) {
		err = fmt.Errorf("no such project (UUID = %s)", projectUUID)
		err = respondwith.CustomStatus(http.StatusNotFound, err)
	}
	return domain, project, t, err
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
func (p *v2Provider) checkToken(r *http.Request) *gopherpolicy.Token {
	return CheckToken(r, p.tokenValidator)
}

func parseRequestBodyAs[T any](r *http.Request) (T, error) {
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
		err = respondwith.CustomStatus(http.StatusBadRequest,
			fmt.Errorf("request body is not valid JSON: %w", err),
		)
	}
	return result, err
}
