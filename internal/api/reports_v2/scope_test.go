// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2_test

import (
	"maps"
	"net/http"
	nethttptest "net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/api/reports_v2"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/common_fixtures"
)

func TestMain(m *testing.M) {
	easypg.WithTestDB(m, func() int { return m.Run() })
}

var scopeConfigJSON = string(must.Return(httptest.NewJQModifiableJSONString("{}", "scopeConfigJSON").
	ModifyWithVariable(".discovery = $ref", common_fixtures.DiscoveryBerlinDresdenParis).
	ModifyWithVariable(".availability_zones = $ref", common_fixtures.AZsOneTwo).
	ModifyWithVariable(". * $ref", common_fixtures.AreaLiquidFirstSecond).
	MarshalJSON()))

type singleScopeTestInput struct {
	s               test.Setup
	r               *http.Request
	forProject      bool
	queryDomainUUID Option[string]
}

type singleScopeTestExpectation struct {
	err                Option[string]
	project            Option[db.Project]
	domain             Option[db.Domain]
	requestProjectUUID Option[string]
	requestDomainUUID  Option[string]
}

func executeSingleScopeTest(t *testing.T, i singleScopeTestInput, e singleScopeTestExpectation) {
	t.Helper()

	token := i.s.TokenValidator.CheckToken(i.r)
	// the real validator fills the Request, so we have to emulate this here
	maps.Copy(token.Context.Request, mux.Vars(i.r))
	scope, err := reports_v2.NewScope(i.forProject, i.r, i.queryDomainUUID, token, i.s.DB)

	if errMsg, ok := e.err.Unpack(); ok {
		assert.ErrEqual(t, err, errMsg)
	} else {
		assert.ErrEqual(t, err, nil)
	}
	assert.Equal(t, scope.Project, e.project)
	assert.Equal(t, scope.Domain, e.domain)
	assert.Equal(t, token.Context.Request["project_uuid"], e.requestProjectUUID.UnwrapOr(""))
	assert.Equal(t, token.Context.Request["domain_uuid"], e.requestDomainUUID.UnwrapOr(""))
}

func TestV2ScopeCreation(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(scopeConfigJSON),
		test.WithInitialDiscovery,
	)
	var (
		domainFrance db.Domain
		projectParis db.Project
	)
	must.SucceedT(t, s.DB.SelectOne(&domainFrance, "SELECT * FROM domains WHERE uuid = 'uuid-for-france'"))
	must.SucceedT(t, s.DB.SelectOne(&projectParis, "SELECT * FROM projects WHERE uuid = 'uuid-for-paris'"))

	// cluster level is only possible for cloud_admin, so nothing happens
	r := nethttptest.NewRequest(http.MethodGet, "/some/unimportant/path", http.NoBody)
	executeSingleScopeTest(t, singleScopeTestInput{
		s:          s,
		r:          r,
		forProject: false,
	}, singleScopeTestExpectation{})

	// domain level is possible for domain tokens, does not alter the token
	r = nethttptest.NewRequest(http.MethodGet, "/some/path/with/uuid-for-france", http.NoBody)
	r = mux.SetURLVars(r, map[string]string{"domain_uuid": "uuid-for-france"})
	s.TokenValidator.Enforcer.IsDomainRole = true
	executeSingleScopeTest(t, singleScopeTestInput{
		s:          s,
		r:          r,
		forProject: false,
	}, singleScopeTestExpectation{
		requestDomainUUID: Some("uuid-for-france"),
		domain:            Some(domainFrance),
	})
	// domain level is possible for cloud_admin, does not alter the token
	s.TokenValidator.Enforcer.IsDomainRole = false
	executeSingleScopeTest(t, singleScopeTestInput{
		s:          s,
		r:          r,
		forProject: false,
	}, singleScopeTestExpectation{
		requestDomainUUID: Some("uuid-for-france"),
		domain:            Some(domainFrance),
	})

	// project level is possible for project tokens, will add domain to the token context
	r = nethttptest.NewRequest(http.MethodGet, "/some/path/with/uuid-for-paris", http.NoBody)
	r = mux.SetURLVars(r, map[string]string{"project_uuid": "uuid-for-paris"})
	s.TokenValidator.Enforcer.IsProjectRole = true
	executeSingleScopeTest(t, singleScopeTestInput{
		s:          s,
		r:          r,
		forProject: true,
	}, singleScopeTestExpectation{
		requestProjectUUID: Some("uuid-for-paris"),
		requestDomainUUID:  Some("uuid-for-france"),
		domain:             Some(domainFrance),
		project:            Some(projectParis),
	})

	// project level is possible for domain token (specific project), will add domain to the token context
	r = nethttptest.NewRequest(http.MethodGet, "/some/path/with/uuid-for-paris", http.NoBody)
	r = mux.SetURLVars(r, map[string]string{"project_uuid": "uuid-for-paris"})
	s.TokenValidator.Enforcer.IsDomainRole = true
	s.TokenValidator.Enforcer.IsProjectRole = false
	executeSingleScopeTest(t, singleScopeTestInput{
		s:          s,
		r:          r,
		forProject: true,
	}, singleScopeTestExpectation{
		requestProjectUUID: Some("uuid-for-paris"),
		requestDomainUUID:  Some("uuid-for-france"),
		domain:             Some(domainFrance),
		project:            Some(projectParis),
	})
	// project level is possible for domain token (whole domain), will add domain to the token context
	r = nethttptest.NewRequest(http.MethodGet, "/some/unimportant/path", http.NoBody)
	executeSingleScopeTest(t, singleScopeTestInput{
		s:               s,
		r:               r,
		forProject:      true,
		queryDomainUUID: Some("uuid-for-france"),
	}, singleScopeTestExpectation{
		requestDomainUUID: Some("uuid-for-france"),
		domain:            Some(domainFrance),
	})

	// project level is possible for cloud_admin (specific project), does not alter the token
	r = nethttptest.NewRequest(http.MethodGet, "/some/path/with/uuid-for-paris", http.NoBody)
	r = mux.SetURLVars(r, map[string]string{"project_uuid": "uuid-for-paris"})
	s.TokenValidator.Enforcer.IsDomainRole = false
	executeSingleScopeTest(t, singleScopeTestInput{
		s:          s,
		r:          r,
		forProject: true,
	}, singleScopeTestExpectation{
		requestProjectUUID: Some("uuid-for-paris"),
		domain:             Some(domainFrance),
		project:            Some(projectParis),
	})
	// project level is possible for cloud_admin (whole domain), does not alter the token
	r = nethttptest.NewRequest(http.MethodGet, "/some/unimportant/path", http.NoBody)
	executeSingleScopeTest(t, singleScopeTestInput{
		s:               s,
		r:               r,
		forProject:      true,
		queryDomainUUID: Some("uuid-for-france"),
	}, singleScopeTestExpectation{
		domain: Some(domainFrance),
	})

	// errors:
	// project level with domain token but no identifier specified
	r = nethttptest.NewRequest(http.MethodGet, "/some/unimportant/path", http.NoBody)
	s.TokenValidator.Enforcer.IsDomainRole = true
	executeSingleScopeTest(t, singleScopeTestInput{
		s:          s,
		r:          r,
		forProject: true,
	}, singleScopeTestExpectation{
		err: Some("specify URL project_uuid or query domain_uuid"),
	})

	// both identifiers set at the same time
	r = nethttptest.NewRequest(http.MethodGet, "/some/path/with/uuid-for-paris", http.NoBody)
	r = mux.SetURLVars(r, map[string]string{"project_uuid": "uuid-for-paris"})
	executeSingleScopeTest(t, singleScopeTestInput{
		s:               s,
		r:               r,
		forProject:      true,
		queryDomainUUID: Some("uuid-for-france"),
	}, singleScopeTestExpectation{
		err:                Some("query domain_uuid cannot be set, when URL project_uuid is set"),
		requestProjectUUID: Some("uuid-for-paris"),
	})
}

func TestV2ExpandScopeFilters(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(scopeConfigJSON),
		test.WithInitialDiscovery,
	)
	var (
		domainFrance db.Domain
		projectParis db.Project
	)
	must.SucceedT(t, s.DB.SelectOne(&domainFrance, "SELECT * FROM domains WHERE uuid = 'uuid-for-france'"))
	must.SucceedT(t, s.DB.SelectOne(&projectParis, "SELECT * FROM projects WHERE uuid = 'uuid-for-paris'"))

	// empty scope: all placeholders become TRUE = TRUE
	emptyScope := reports_v2.Scope{}
	query, args := emptyScope.ExpandScopeFilters(
		`SELECT * FROM t WHERE {{d.id = $domain_id}} AND {{p.id = $project_id}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE TRUE = TRUE AND TRUE = TRUE`)
	assert.Equal(t, len(args), 0)

	// domain-only scope: domain_id gets replaced, project_id becomes noop
	domainScope := reports_v2.Scope{
		Domain: Some(domainFrance),
	}
	query, args = domainScope.ExpandScopeFilters(
		`SELECT * FROM t WHERE {{d.id = $domain_id}} AND {{p.id = $project_id}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE d.id = $1 AND TRUE = TRUE`)
	assert.Equal(t, len(args), 1)
	assert.Equal(t, args[0].(db.DomainID), domainFrance.ID)

	// project scope (both domain and project set): both get replaced
	projectScope := reports_v2.Scope{
		Domain:  Some(domainFrance),
		Project: Some(projectParis),
	}
	query, args = projectScope.ExpandScopeFilters(
		`SELECT * FROM t WHERE {{d.id = $domain_id}} AND {{p.id = $project_id}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE d.id = $1 AND p.id = $2`)
	assert.Equal(t, len(args), 2)
	assert.Equal(t, args[0].(db.DomainID), domainFrance.ID)
	assert.Equal(t, args[1].(db.ProjectID), projectParis.ID)

	// with pre-existing args: arg positions continue from the highest existing index
	query, args = projectScope.ExpandScopeFilters(
		`SELECT * FROM t WHERE t.name = $14 AND {{d.id = $domain_id}} AND {{p.id = $project_id}}`,
		"some-value",
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE t.name = $14 AND d.id = $15 AND p.id = $16`)
	assert.Equal(t, len(args), 3)
	assert.Equal(t, args[0].(string), "some-value")
	assert.Equal(t, args[1].(db.DomainID), domainFrance.ID)
	assert.Equal(t, args[2].(db.ProjectID), projectParis.ID)

	// only project_id placeholder in query with project scope
	query, args = projectScope.ExpandScopeFilters(
		`SELECT * FROM t WHERE {{p.id = $project_id}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE p.id = $1`)
	assert.Equal(t, len(args), 1)
	assert.Equal(t, args[0].(db.ProjectID), projectParis.ID)
}
