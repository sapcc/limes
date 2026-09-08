// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2_test

import (
	"testing"

	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"
	"go.xyrillian.de/gg/pgruntime"

	"github.com/sapcc/limes/internal/api/reports_v2"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/common_fixtures"
)

func TestMain(m *testing.M) {
	pgruntime.WithTestDB(m, m.Run)
}

var scopeConfigJSON = string(must.Return(httptest.NewJQModifiableJSONString("{}", "scopeConfigJSON").
	ModifyWithVariable(".discovery = $ref", common_fixtures.DiscoveryBerlinDresdenParis).
	ModifyWithVariable(".availability_zones = $ref", common_fixtures.AZsOneTwo).
	ModifyWithVariable(". * $ref", common_fixtures.AreaLiquidFirstSecond).
	MarshalJSON()))

func TestV2ExpandScopeFilters(t *testing.T) {
	ctx := t.Context()

	s := test.NewSetup(t,
		test.WithConfig(scopeConfigJSON),
		test.WithInitialDiscovery,
	)
	var (
		domainFrance = must.ReturnT(db.DomainStore.SelectOneWhere(ctx, s.DB, `uuid = $1`, "uuid-for-france"))(t)
		projectParis = must.ReturnT(db.ProjectStore.SelectOneWhere(ctx, s.DB, `uuid = $1`, "uuid-for-paris"))(t)
	)

	// empty scope: all placeholders become TRUE = TRUE
	clusterScope := reports_v2.ClusterScope{}
	query, args := clusterScope.ExpandScopeFilters(
		`SELECT * FROM t WHERE {{d.id = ANY($domain_id)}} AND {{p.id = ANY($project_id)}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE TRUE = TRUE AND TRUE = TRUE`)
	assert.Equal(t, len(args), 0)

	// domain-only scope: domain_id gets replaced, project_id becomes noop
	domainScope := reports_v2.DomainScope{
		Domain: domainFrance,
	}
	query, args = domainScope.ExpandScopeFilters(
		`SELECT * FROM t WHERE {{d.id = ANY($domain_id)}} AND {{p.id = ANY($project_id)}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE d.id = $1 AND TRUE = TRUE`)
	assert.Equal(t, len(args), 1)
	assert.Equal(t, args[0].(db.DomainID), domainFrance.ID)

	// project scope (both domain and project set): both get replaced
	projectScope := reports_v2.ProjectScope{
		Domain:  domainFrance,
		Project: projectParis,
	}
	query, args = projectScope.ExpandScopeFilters(
		`SELECT * FROM t WHERE {{d.id = ANY($domain_id)}} AND {{p.id = ANY($project_id)}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE d.id = $1 AND p.id = $2`)
	assert.Equal(t, len(args), 2)
	assert.Equal(t, args[0].(db.DomainID), domainFrance.ID)
	assert.Equal(t, args[1].(db.ProjectID), projectParis.ID)

	// with pre-existing args: arg positions continue from the highest existing index
	query, args = projectScope.ExpandScopeFilters(
		`SELECT * FROM t WHERE t.name = $1 AND {{d.id = ANY($domain_id)}} AND {{p.id = ANY($project_id)}}`,
		"some-value",
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE t.name = $1 AND d.id = $2 AND p.id = $3`)
	assert.Equal(t, len(args), 3)
	assert.Equal(t, args[0].(string), "some-value")
	assert.Equal(t, args[1].(db.DomainID), domainFrance.ID)
	assert.Equal(t, args[2].(db.ProjectID), projectParis.ID)

	// only project_id placeholder in query with project scope
	query, args = projectScope.ExpandScopeFilters(
		`SELECT * FROM t WHERE {{p.id = ANY($project_id)}}`,
	)
	assert.Equal(t, query, `SELECT * FROM t WHERE p.id = $1`)
	assert.Equal(t, len(args), 1)
	assert.Equal(t, args[0].(db.ProjectID), projectParis.ID)
}
