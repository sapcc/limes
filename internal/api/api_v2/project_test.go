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

func TestV2ProjectRateReport(t *testing.T) {
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
	fixturePath := "./fixtures/rate-projects.json"

	// for now, we will set all rates to usage - later we will check the combination with rate limits
	objectsUpdateRateID := s.GetRateID("first", "objects:update")
	s.MustDBExec(`UPDATE project_rates SET usage_as_bigint = '5' WHERE rate_id != $1`, objectsUpdateRateID)

	s.TokenValidator.Enforcer.AllowReport = false
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReport = true

	// the maximum result set includes the info
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects?with=info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "with-info"))

	// without info
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify("del(.info)"))

	// one project
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects/uuid-for-paris?with=info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "project filter paris").
			Modify(`del(.domains["uuid-for-germany"])`))
	// another project
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects/uuid-for-berlin?with=info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "project filter berlin").
			Modify(`del(.domains["uuid-for-france"])`).
			Modify(`del(.domains["uuid-for-germany"].projects["uuid-for-dresden"])`))
	// another project
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects/uuid-for-dresden?with=info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "project filter dresden").
			Modify(`del(.domains["uuid-for-france"])`).
			Modify(`del(.domains["uuid-for-germany"].projects["uuid-for-berlin"])`))

	// one full domain
	// for this, a cloud_admin or domain token is required.
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects?with=info&domain_uuid=uuid-for-france").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain filter france").
			Modify(`del(.domains["uuid-for-germany"])`))
	// the other domain
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects?with=info&domain_uuid=uuid-for-germany").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain filter france").
			Modify(`del(.domains["uuid-for-france"])`))

	// when filtering for a certain rate, the info and report get filtered similarly
	// area: this will lead to an empty report, hence the structure is cut off at a higher level!
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects?with=info&area=second").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "area filter").
			Modify(".domains=null").
			Modify("del(.info.service_areas.first)"))
	// service: also empty report
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects?with=info&service=second").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "service filter").
			Modify(".domains=null").
			Modify("del(.info.service_areas.first)"))
	// category
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects?with=info&category=foo_category").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "category filter").
			Modify("del(.domains[].projects[].service_areas.first.services.first.categories.first)").
			Modify("del(.info.service_areas.first.services.first.categories.first)"))
	// category - special case: empty category (category = service type)
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects?with=info&category=first").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "category filter - empty category/ equal service type").
			Modify("del(.domains[].projects[].service_areas.first.services.first.categories.foo_category)").
			Modify("del(.info.service_areas.first.services.first.categories.foo_category)").
			Modify("del(.info.service_areas.second)"))
	// rate
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects?with=info&rate=objects:create").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "rate filter").
			Modify("del(.domains[].projects[].service_areas.first.services.first.categories.first)").
			Modify("del(.info.service_areas.first.services.first.categories.first)"))

	// no we add some rate limits to get the other 2 combinations (objects:update=limit only, object:delete=limit + usage)
	objectsDeleteRateID := s.GetRateID("first", "objects:delete")
	s.MustDBExec(`UPDATE project_rates SET rate_limit = 10, window_ns = 5000000000 WHERE rate_id IN ($1, $2)`, objectsDeleteRateID, objectsUpdateRateID)
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects?with=info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "with limits").
			Modify(`.domains[].projects[].service_areas.first.services.first.categories.first.rates["objects:delete"]={"usage_as_bigint":"5", "project_limit":10, "project_window":"5s"}`).
			Modify(`.domains[].projects[].service_areas.first.services.first.categories.first.rates["objects:update"]={"project_limit":10, "project_window":"5s"}`))

	// there are some error cases possible depending on the scope in this endpoint:
	// a domain user needs to have one of the filters set
	s.TokenValidator.Enforcer.IsDomainRole = true
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects").ExpectText(t, http.StatusBadRequest, "specify URL project_uuid or query domain_uuid\n")

	// cannot set both at the same time
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects/uuid-for-paris?domain_uuid=uuid-for-france").ExpectText(t, http.StatusBadRequest, "query domain_uuid cannot be set, when URL project_uuid is set\n")

	// same for cloud admin
	s.TokenValidator.Enforcer.IsDomainRole = false
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects/uuid-for-paris?domain_uuid=uuid-for-france").ExpectText(t, http.StatusBadRequest, "query domain_uuid cannot be set, when URL project_uuid is set\n")

	// unknown domain/ project
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects?domain_uuid=does-not-exists").ExpectText(t, http.StatusNotFound, "no such domain (UUID = does-not-exists)\n")
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects/does-not-exists").ExpectText(t, http.StatusNotFound, "no such project (UUID = does-not-exists)\n")
}
