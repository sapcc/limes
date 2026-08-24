// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/respondwith"
	"go.xyrillian.de/gg/gsql"
	"go.xyrillian.de/gg/is"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// Scope describes the scope of a validated request.
// The concrete types are [ClusterScope], [DomainScope] and [ProjectScope].
type Scope interface {
	// ExpandScopeFilters modifies an SQL query by replacing placeholders of the forms
	//
	//	{{some_field = ANY($domain_id)}}
	//	{{some_field = ANY($project_id)}}
	//
	// with appropriate conditions that restrict matches to domains and projects within this scope.
	// Returns the modified query and the appropriately extended argument list.
	ExpandScopeFilters(originalQuery string, originalArgs ...any) (query string, args []any)
}

// ClusterScope is a [Scope] for operations that are not restricted to a specific domain or project.
type ClusterScope struct{}

// ExpandScopeFilters implements the [Scope] interface.
func (ClusterScope) ExpandScopeFilters(originalQuery string, originalArgs ...any) (query string, args []any) {
	return expandScopeFilters(None[db.Domain](), None[db.Project](), originalQuery, originalArgs)
}

// DomainScope is a [Scope] for operations that are restricted to a single domain.
type DomainScope struct {
	Domain db.Domain
}

// NewDomainScope builds a [DomainScope] by looking for the requested domain in the database.
func NewDomainScope(ctx context.Context, domainUUID string, dbm *gsql.DB) (DomainScope, error) {
	var none DomainScope // only used in error returns

	domain, err := db.DomainStore.SelectOneWhere(ctx, dbm, `uuid = $1`, domainUUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return none, respondwith.CustomStatus(http.StatusNotFound, fmt.Errorf("no such domain (UUID = %s)", domainUUID))
		}
		return none, err
	}

	return DomainScope{domain}, nil
}

// ExpandScopeFilters implements the [Scope] interface.
func (s DomainScope) ExpandScopeFilters(originalQuery string, originalArgs ...any) (query string, args []any) {
	return expandScopeFilters(Some(s.Domain), None[db.Project](), originalQuery, originalArgs)
}

// ProjectScope is a [Scope] for operations that are restricted to a single domain.
type ProjectScope struct {
	Domain  db.Domain
	Project db.Project
}

// NewProjectScope builds a [ProjectScope] by looking for the requested project in the database.
// If a domainUUID is also given, returns an error if the project turns out not to be in that domain.
func NewProjectScope(ctx context.Context, projectUUID liquid.ProjectUUID, domainUUID Option[string], dbm *gsql.DB) (ProjectScope, error) {
	var none ProjectScope // only used in error returns

	project, err := db.ProjectStore.SelectOneWhere(ctx, dbm, `uuid = $1`, projectUUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return none, respondwith.CustomStatus(http.StatusNotFound, fmt.Errorf("no such project (UUID = %s)", projectUUID))
		}
		return none, err
	}

	domain, err := db.DomainStore.SelectOneWhere(ctx, dbm, `id = $1`, project.DomainID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return none, fmt.Errorf("referential integrity error: project %s references unknown domain with ID = %d", project.UUID, project.DomainID)
		}
		return none, err
	}

	if domainUUID.IsSomeAnd(is.DifferentFrom(domain.UUID)) {
		err = fmt.Errorf("inconsistent NewScope() invocation: got domainUUID = %q and projectUUID = %q, but that project actually belongs to domain %q with UUID = %q",
			domainUUID, projectUUID, domain.Name, domain.UUID,
		)
		return none, respondwith.CustomStatus(http.StatusBadRequest, err)
	}
	return ProjectScope{domain, project}, nil
}

// ExpandScopeFilters implements the [Scope] interface.
func (s ProjectScope) ExpandScopeFilters(originalQuery string, originalArgs ...any) (query string, args []any) {
	return expandScopeFilters(Some(s.Domain), Some(s.Project), originalQuery, originalArgs)
}

var scopeFilterReplaceRx = regexp.MustCompile(`{{(\S+?) = ANY\(\$(domain_id|project_id)\)}}`)

// Common implementation for ExpandScopeFilters of [Scope].
func expandScopeFilters(domain Option[db.Domain], project Option[db.Project], originalQuery string, originalArgs []any) (query string, args []any) {
	args = slices.Clone(originalArgs)
	query = scopeFilterReplaceRx.ReplaceAllStringFunc(originalQuery, func(matchStr string) string {
		match := scopeFilterReplaceRx.FindStringSubmatch(matchStr)

		switch match[2] {
		case "domain_id":
			if unpackedDomain, ok := domain.Unpack(); ok {
				args = append(args, unpackedDomain.ID)
			} else {
				return util.SQLFilterNoop
			}
		case "project_id":
			if unpackedProject, ok := project.Unpack(); ok {
				args = append(args, unpackedProject.ID)
			} else {
				return util.SQLFilterNoop
			}
		default:
			panic("unreachable")
		}
		return match[1] + " = $" + strconv.Itoa(len(args))
	})
	return query, args
}
