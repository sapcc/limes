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
	"strconv"

	"github.com/sapcc/go-bits/respondwith"
	"go.xyrillian.de/gg/gsql"
	"go.xyrillian.de/gg/is"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"
)

// Scope describes the object Scope of a validated request.
// Currently, there is no option to have a Scope with more than one domain or project.
// If project.IsSome(), then domain.IsSome() too.
type Scope struct {
	Domain  Option[db.Domain]
	Project Option[db.Project]
}

// NewScope obtains the project and domain from the database.
// If both domainUUID and projectUUID are given, then the requested project must be in the requested domain.
func NewScope(ctx context.Context, domainUUID, projectUUID Option[string], dbm *gsql.DB) (s Scope, err error) {
	var none Scope // only used in error returns

	// case 1: project UUID given -> scope is that project
	if unpackedProjectUUID, ok := projectUUID.Unpack(); ok {
		project, err := db.ProjectStore.SelectOneWhere(ctx, dbm, `uuid = $1`, unpackedProjectUUID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return none, respondwith.CustomStatus(http.StatusNotFound, fmt.Errorf("no such project (UUID = %s)", unpackedProjectUUID))
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
			return none, fmt.Errorf("inconsistent NewScope() invocation: got domainUUID = %q and projectUUID = %q, but that project actually belongs to domain %q with UUID = %q",
				domainUUID, projectUUID, domain.Name, domain.UUID,
			)
		}
		return Scope{Some(domain), Some(project)}, nil
	}

	// case 2: project UUID missing, but domain UUID given -> scope is that domain
	if unpackedDomainUUID, ok := domainUUID.Unpack(); ok {
		domain, err := db.DomainStore.SelectOneWhere(ctx, dbm, `uuid = $1`, unpackedDomainUUID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return none, respondwith.CustomStatus(http.StatusNotFound, fmt.Errorf("no such domain (UUID = %s)", unpackedDomainUUID))
			}
			return none, err
		}

		return Scope{Some(domain), None[db.Project]()}, nil
	}

	// case 3: project UUID and domain UUID missing -> scope is full cluster
	return Scope{}, nil
}

var scopeFilterReplaceRx = regexp.MustCompile(`{{(\S+?) = ANY\(\$(domain_id|project_id)\)}}`)

// ExpandScopeFilters takes an SQL query string with curly-bracketed
// where-clauses and will replace each one with an arg position and return the
// according SQL arg for this filter, namely a scope ID.
// The expressions must be of the form "{{[filter-field] = $[id-field]}}"
// where filter-field can be a primary key column or a foreign key and id-field
// is the name of the scope entity whose ID-column values are used.
// It supports domain_id and project_id.
// On unknown keywords it will panic.
func (s Scope) ExpandScopeFilters(originalQuery string, originalArgs ...any) (query string, args []any) {
	// get current highest index
	var err error
	i := 0
	queryVariables := regexp.MustCompile(`\$(\d+)`)
	matches := queryVariables.FindAllString(originalQuery, -1)
	if len(matches) > 0 {
		last := matches[len(matches)-1]
		i, err = strconv.Atoi(queryVariables.FindStringSubmatch(last)[1])
		if err != nil {
			panic("digits should be parseable integer")
		}
	}
	args = append(args, originalArgs...)

	query = scopeFilterReplaceRx.ReplaceAllStringFunc(originalQuery, func(matchStr string) string {
		match := scopeFilterReplaceRx.FindStringSubmatch(matchStr)

		switch match[2] {
		case "domain_id":
			if domain, ok := s.Domain.Unpack(); ok {
				args = append(args, domain.ID)
			} else {
				return util.SQLFilterNoop
			}
		case "project_id":
			if project, ok := s.Project.Unpack(); ok {
				args = append(args, project.ID)
			} else {
				return util.SQLFilterNoop
			}
		default:
			panic("unreachable")
		}
		i++
		return match[1] + " = $" + strconv.Itoa(i)
	})
	return query, args
}
