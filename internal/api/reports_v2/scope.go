// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-gorp/gorp/v3"
	"github.com/sapcc/go-bits/gopherpolicy"
	"github.com/sapcc/go-bits/respondwith"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/util"

	"github.com/gorilla/mux"
	. "go.xyrillian.de/gg/option"
)

// Scope describes the object Scope of a validated request.
// Currently, there is no option to have a Scope with more than one domain or project.
// If project.IsSome(), then domain.IsSome() too.
type Scope struct {
	Domain  Option[db.Domain]
	Project Option[db.Project]
}

// NewScope obtains the project and domain from the database.
// When forProject=true, this should be called before evaluating the tokens authorization
// so that the project-->domain relation of domain admins is checked properly.
// It returns the validated scope information or an error.
func NewScope(forProject bool, r *http.Request, queryDomainUUID Option[string], token *gopherpolicy.Token, dbm *gorp.DbMap) (s Scope, err error) {
	urlDomainUUID := mux.Vars(r)["domain_uuid"]
	urlProjectUUID := mux.Vars(r)["project_uuid"]

	isDomainUser := token.Check("v2:domain:role")
	isProjectUser := token.Check("v2:project:role")

	// a project user for project API without URL param will get an authentication error from the token check, so we just return here
	if forProject && isProjectUser && urlProjectUUID == "" {
		return Scope{Project: Some(db.Project{ID: -1})}, nil
	}
	// a domain user needs to have one of the values set
	if forProject && isDomainUser && urlProjectUUID == "" && queryDomainUUID.IsNone() {
		return s, respondwith.CustomStatus(http.StatusBadRequest, errors.New("specify URL project_uuid or query domain_uuid"))
	}
	// both values cannot be set together, this check is superfluous for non-project because it's not in opts there
	if forProject && queryDomainUUID.IsSome() && urlProjectUUID != "" {
		return s, respondwith.CustomStatus(http.StatusBadRequest, errors.New("query domain_uuid cannot be set, when URL project_uuid is set"))
	}
	// For domain and cluster mode, auth check is done in advance, so we don't need to check urlDomainUUID and urlProjectUUID.
	// queryDomainUUID.IsSome() gets rejected by option parsing for domain and cluster.

	if urlProjectUUID != "" {
		var project db.Project
		err := dbm.SelectOne(&project, "SELECT * FROM projects WHERE uuid = $1", urlProjectUUID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return s, respondwith.CustomStatus(http.StatusNotFound, fmt.Errorf("no such project (UUID = %s)", urlProjectUUID))
		case err != nil:
			return s, err
		default:
			s.Project = Some(project)
		}
	}

	var (
		filter string
		arg    any
	)
	switch {
	case urlDomainUUID != "":
		filter = "UUID = $1"
		arg = urlDomainUUID
	case queryDomainUUID.IsSome():
		filter = "UUID = $1"
		arg = queryDomainUUID.UnwrapOrPanic("queryDomainUUID was checked to be Some()")
	case s.Project.IsSome():
		filter = "ID = $1"
		arg = s.Project.UnwrapOrPanic("project was checked to be Some()").DomainID
	default:
		return s, nil
	}
	var domain db.Domain
	err = dbm.SelectOne(&domain, "SELECT * FROM domains WHERE "+filter, arg)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		return s, respondwith.CustomStatus(http.StatusNotFound, fmt.Errorf("no such domain (%s)", fmt.Sprintf(strings.ReplaceAll(filter, "$1", "%s"), arg)))
	case err != nil:
		return s, err
	default:
		s.Domain = Some(domain)
		// important: we need this for the token authentication with specific requested
		// project to work as the paths don't contain the domain_uuid anymore.
		if forProject && (isProjectUser || isDomainUser) {
			token.Context.Request["domain_uuid"] = domain.UUID
		}
	}

	return s, nil
}

var scopeFilterReplaceRx = regexp.MustCompile(`{{(.*?) = \$(domain_id|project_id)}}`)

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
	queryVariables := regexp.MustCompile(`\$(\d{1,2})`)
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
