// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"

	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/util"
)

func TestV2DomainResourceReport(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(resourceReportConfigJSON),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)
	fixturePath := "./fixtures/resource-domains.json"

	s.Clock.StepBy(time.Hour)

	// set up usage on project_az_resources
	// germany has 2 projects (berlin, dresden), france has 1 (paris)
	s.MustDBExec(`UPDATE project_az_resources SET usage = 10 WHERE az_resource_id IN (SELECT id FROM az_resources WHERE path LIKE '%/capacity/%')`)
	s.MustDBExec(`UPDATE project_az_resources SET usage = 5, physical_usage = 2 WHERE az_resource_id IN (SELECT id FROM az_resources WHERE path LIKE '%/things/%')`)

	// setup commitments for commitment_stats testing
	berlin := s.GetProjectID("berlin")
	firstCapacityAZOne := s.GetAZResourceID("first", "capacity", "az-one")
	firstCapacityAZTwo := s.GetAZResourceID("first", "capacity", "az-two")
	for i, config := range []struct {
		status      liquid.CommitmentStatus
		confirmedAt Option[time.Time]
	}{
		{liquid.CommitmentStatusPlanned, None[time.Time]()},
		{liquid.CommitmentStatusPending, None[time.Time]()},
		{liquid.CommitmentStatusConfirmed, Some(s.Clock.Now())},
		{liquid.CommitmentStatusSuperseded, Some(s.Clock.Now())},
		{liquid.CommitmentStatusExpired, Some(s.Clock.Now())},
		{util.CommitmentStatusDeleted, Some(s.Clock.Now())},
	} {
		s.MustDBInsert(&db.ProjectCommitment{
			UUID:                test.GenerateDummyCommitmentUUID(uint64(i + 1)),
			ProjectID:           berlin,
			AZResourceID:        firstCapacityAZOne,
			Amount:              10,
			Duration:            must.ReturnT(limesresources.ParseCommitmentDuration("1 year"))(t),
			CreatedAt:           s.Clock.Now(),
			UpdatedAt:           s.Clock.Now(),
			CreatorUUID:         "dummy",
			CreatorName:         "dummy",
			ConfirmedAt:         config.confirmedAt,
			ExpiresAt:           s.Clock.Now().AddDate(1, 0, 0),
			Status:              config.status,
			CreationContextJSON: json.RawMessage(`{}`),
		})
	}
	// create some committed_confirmed_unutilized
	s.MustDBInsert(&db.ProjectCommitment{
		UUID:                test.GenerateDummyCommitmentUUID(7),
		ProjectID:           berlin,
		AZResourceID:        firstCapacityAZTwo,
		Amount:              50,
		Duration:            must.ReturnT(limesresources.ParseCommitmentDuration("1 year"))(t),
		CreatedAt:           s.Clock.Now(),
		UpdatedAt:           s.Clock.Now(),
		CreatorUUID:         "dummy",
		CreatorName:         "dummy",
		ConfirmedAt:         Some(s.Clock.Now()),
		ExpiresAt:           s.Clock.Now().AddDate(1, 0, 0),
		Status:              liquid.CommitmentStatusConfirmed,
		CreationContextJSON: json.RawMessage(`{}`),
	})

	// permission checks
	s.TokenValidator.Enforcer.AllowReportMultiple = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/domains").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReportMultiple = true
	s.TokenValidator.Enforcer.AllowReportSingle = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/domains/uuid-for-france").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReportSingle = true

	// full result with all options
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/domains?with=info&with=commitment_stats").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "all options"))

	var (
		withoutInfoMods            = []string{"del(.info)"}
		withoutCommitmentStatsMods = []string{
			`walk(if type == "object" then del(.committed) else . end)`,
			`walk(if type == "object" then del(.committed_confirmed_unutilized) else . end)`,
			`walk(if type == "object" then del(.usage_uncommitted) else . end)`,
		}
	)

	// without any extras
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/domains").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify(withoutInfoMods...).
			Modify(withoutCommitmentStatsMods...))

	// with=commitment_stats only
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/domains?with=commitment_stats").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "commitment_stats only").
			Modify(withoutInfoMods...))

	// single domain
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/domains/uuid-for-france?with=info&with=commitment_stats").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain filter france").
			Modify(`del(.domains["uuid-for-germany"])`))
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/domains/uuid-for-germany?with=info&with=commitment_stats").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain filter germany").
			Modify(`del(.domains["uuid-for-france"])`))

	// filter by area
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/domains?with=info&area=second").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "area filter").
			Modify("del(.info.service_areas.first)").
			Modify("del(.domains[].service_areas.first)").
			Modify(withoutCommitmentStatsMods...))

	// filter by service
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/domains?with=info&service=first").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "service filter").
			Modify("del(.info.service_areas.second)").
			Modify("del(.domains[].service_areas.second)").
			Modify(withoutCommitmentStatsMods...))

	// filter by resource
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/domains?with=info&resource=capacity").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "resource filter").
			Modify("del(.info.service_areas.first.services.first.categories.first)").
			Modify("del(.info.service_areas.second.services.second.categories.second)").
			Modify("del(.domains[].service_areas.first.services.first.categories.first)").
			Modify("del(.domains[].service_areas.second.services.second.categories.second)").
			Modify(withoutCommitmentStatsMods...))

	// filter by category
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/domains?with=info&category=foo_category").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "category filter").
			Modify("del(.info.service_areas.first.services.first.categories.first)").
			Modify("del(.info.service_areas.second.services.second.categories.second)").
			Modify("del(.domains[].service_areas.first.services.first.categories.first)").
			Modify("del(.domains[].service_areas.second.services.second.categories.second)").
			Modify(withoutCommitmentStatsMods...))

	// unknown domain
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/domains/does-not-exist").ExpectText(t, http.StatusNotFound, "no such domain (UUID = does-not-exist)\n")
}

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

	s.TokenValidator.Enforcer.AllowReportMultiple = false
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReportMultiple = true
	s.TokenValidator.Enforcer.AllowReportSingle = false
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/domains/uuid-for-france").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReportSingle = true

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
