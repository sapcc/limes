// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"net/http"
	"testing"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/httptest"

	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/test"
)

func TestV2DomainRateReport(t *testing.T) {
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
	fixturePath := "./fixtures/rate-domains.json"

	// we will just update all project_rates with usage to have a value of 5, so one domain has 10 usage and the other 5
	updatesUpdateRateID := s.GetRateID("first", "objects:update")
	s.MustDBExec(`UPDATE project_rates SET usage_as_bigint = '5' WHERE rate_id != $1`, updatesUpdateRateID)

	s.TokenValidator.Enforcer.AllowReport = false
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReport = true

	// the maximum result set includes the info
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains?with=info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "with-info"))

	// without info
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify("del(.info)"))

	// one domain
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains/uuid-for-france?with=info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain filter france").
			Modify(`del(.domains["uuid-for-germany"])`))
	// the other domain
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains/uuid-for-germany?with=info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain filter germany").
			Modify(`del(.domains["uuid-for-france"])`))

	// when filtering for a certain rate, the info and report get filtered similarly
	// area: this will lead to an empty report, hence the structure is cut off at a higher level!
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains?with=info&area=second").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "area filter").
			Modify(".domains=null").
			Modify("del(.info.service_areas.first)"))
	// service: also empty report
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains?with=info&service=second").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "service filter").
			Modify(".domains=null").
			Modify("del(.info.service_areas.first)"))
	// category
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains?with=info&category=foo_category").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "category filter").
			Modify("del(.domains[].service_areas.first.services.first.categories.first)").
			Modify("del(.info.service_areas.first.services.first.categories.first)"))
	// category - special case: empty category (category = service type)
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains?with=info&category=first").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "category filter - empty category/ equal service type").
			Modify("del(.domains[].service_areas.first.services.first.categories.foo_category)").
			Modify("del(.info.service_areas.first.services.first.categories.foo_category)").
			Modify("del(.info.service_areas.second)"))
	// rate
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains?with=info&rate=objects:create").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "rate filter").
			Modify("del(.domains[].service_areas.first.services.first.categories.first)").
			Modify("del(.info.service_areas.first.services.first.categories.first)"))

	// unknown domain
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains/does-not-exists").ExpectText(t, http.StatusNotFound, "no such domain (UUID = does-not-exists)\n")
}
