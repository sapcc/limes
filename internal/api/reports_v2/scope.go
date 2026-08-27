// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"regexp"
	"slices"
	"strconv"

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

// ExpandScopeFilters implements the [Scope] interface.
func (s DomainScope) ExpandScopeFilters(originalQuery string, originalArgs ...any) (query string, args []any) {
	return expandScopeFilters(Some(s.Domain), None[db.Project](), originalQuery, originalArgs)
}

// ProjectScope is a [Scope] for operations that are restricted to a single domain.
type ProjectScope struct {
	Domain  db.Domain
	Project db.Project
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
