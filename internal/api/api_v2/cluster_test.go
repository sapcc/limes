// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"net/http"
	"testing"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"

	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/common_fixtures"
)

var rateReportConfigJSON = string(must.Return(httptest.NewJQModifiableJSONString(test.RemoveCommentsFromJSON(`
	{
		"liquids": {
		"first": {
			"area": "first",
			"commitment_behavior_per_resource": [],
			"rate_limits": {
				"global": [
					{"name": "objects:create", "limit": 5000, "window": "1s", "unit": "piece"}
				],
				"project_default": [
					{"name": "objects:create", "limit": 5, "window": "1m", "unit": "piece"},
					{"name": "objects:update", "limit": 2, "window": "1s", "unit": "piece"}
				]
			}
		},
		"second": {
			"area": "second",
			"commitment_behavior_per_resource": []
		}
	}
	}`), "rateReportConfigJSON").
	ModifyWithVariable(".discovery = $ref", common_fixtures.DiscoveryBerlinDresdenParis).
	ModifyWithVariable(".areas = $ref", common_fixtures.AreasFirstSecond).
	ModifyWithVariable(".availability_zones = $ref", common_fixtures.AZsOneTwo).
	MarshalJSON()))

func TestV2ClusterRateReport(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo("First")
	srvInfoFirst.Rates = map[liquid.RateName]liquid.RateInfo{
		"objects:create":    {DisplayName: "Object Creations", Topology: liquid.FlatTopology, HasUsage: true, Category: Some(liquid.CategoryName("foo_category"))},
		"objects:delete":    {DisplayName: "Object Deletions", Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"objects:update":    {DisplayName: "Object Updates", Topology: liquid.FlatTopology, HasUsage: false},
		"objects:unlimited": {DisplayName: "Object Unlimited Operations", Unit: liquid.UnitKibibytes, Topology: liquid.FlatTopology, HasUsage: true},
	}

	s := test.NewSetup(t,
		test.WithConfig(rateReportConfigJSON),
		test.WithPersistedServiceInfo("first", srvInfoFirst),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
		test.WithEmptyRateRecordsAsNeeded,
	)
	fixturePath := "./fixtures/rate-cluster.json"

	// we will just update all project_rates with usage to have a value of 5, so 3 projects * 5 = 15 usage
	objectsCreateRateID := s.GetRateID("first", "objects:update")
	s.MustDBExec(`UPDATE project_rates SET usage_as_bigint = '5' WHERE rate_id != $1`, objectsCreateRateID)

	s.TokenValidator.Enforcer.AllowReportSingle = false
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/cluster").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReportSingle = true

	// the maximum result set includes the info
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/cluster?with=info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "with-info"))

	// without info
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/cluster").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify("del(.info)"))

	// when filtering for a certain rate, the info and report get filtered similarly
	// area: this will lead to an empty report, hence the structure is cut off at a higher level!
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/cluster?with=info&area=second").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "area filter").
			Modify(".cluster_report.service_areas=null").
			Modify("del(.info.service_areas.first)"))
	// service: also empty report
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/cluster?with=info&service=second").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "service filter").
			Modify(".cluster_report.service_areas=null").
			Modify("del(.info.service_areas.first)"))
	// category
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/cluster?with=info&category=foo_category").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "category filter").
			Modify("del(.cluster_report.service_areas.first.services.first.categories.first)").
			Modify("del(.info.service_areas.first.services.first.categories.first)"))
	// category - special case: empty category (category = service type)
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/cluster?with=info&category=first").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "category filter - empty category/ equal service type").
			Modify("del(.cluster_report.service_areas.first.services.first.categories.foo_category)").
			Modify("del(.info.service_areas.first.services.first.categories.foo_category)").
			Modify("del(.info.service_areas.second)"))
	// rate
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/cluster?with=info&rate=objects:create").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "rate filter").
			Modify("del(.cluster_report.service_areas.first.services.first.categories.first)").
			Modify("del(.info.service_areas.first.services.first.categories.first)"))
}
