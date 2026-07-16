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
	"github.com/sapcc/limes/internal/test/common_fixtures"
	"github.com/sapcc/limes/internal/util"
)

var resourceReportConfigJSON = string(must.Return(httptest.NewJQModifiableJSONString(test.RemoveCommentsFromJSON(`
	{
		"liquids": {
			"first": {
				"area": "first",
				"commitment_behavior_per_resource": [
					{"key": "capacity", "value": {"durations_per_domain": [{"key": ".*", "value": ["1 year", "3 years"]}]}}
				]
			},
			"second": {
				"area": "second",
				"commitment_behavior_per_resource": [
					{"key": "capacity", "value": {"durations_per_domain": [{"key": ".*", "value": ["1 year", "3 years"]}]}}
				]
			}
		},
		"resource_behavior": [
			{"resource": "first/capacity", "overcommit_factor": 2}	
		]
	}`), "resourceReportConfigJSON").
	ModifyWithVariable(".discovery = $ref", common_fixtures.DiscoveryBerlinDresdenParis).
	ModifyWithVariable(".areas = $ref", common_fixtures.AreasFirstSecond).
	ModifyWithVariable(".availability_zones = $ref", common_fixtures.AZsOneTwo).
	MarshalJSON()))

func TestV2ClusterResourceReport(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(resourceReportConfigJSON),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)
	fixturePath := "./fixtures/resource-cluster.json"

	s.Clock.StepBy(time.Hour)

	// set up some capacity and usage data
	// "first/capacity" is AZ-aware with has_capacity=true: set raw_capacity on az_resources
	s.MustDBExec(`UPDATE az_resources SET raw_capacity = 1000, usage = 500 WHERE path LIKE 'first/capacity/az-%'`)
	s.MustDBExec(`UPDATE az_resources SET raw_capacity = 500 WHERE path LIKE 'second/capacity/az-%'`)
	// set usage on project_az_resources (3 projects, each gets 10 usage for capacity in each AZ)
	s.MustDBExec(`UPDATE project_az_resources SET usage = 10 WHERE az_resource_id IN (SELECT id FROM az_resources WHERE path LIKE '%/capacity/%')`)
	// set usage for "things" (flat topology, so only "any" AZ)
	s.MustDBExec(`UPDATE project_az_resources SET usage = 5, physical_usage = 2 WHERE az_resource_id IN (SELECT id FROM az_resources WHERE path LIKE '%/things/%')`)
	// set subcapacities on one AZ resource for testing with=subcapacities
	s.MustDBExec(`UPDATE az_resources SET subcapacities = '[{"foo":"bar"},{"foo":"baz"}]' WHERE path = 'first/capacity/az-one'`)
	// scraped_at
	s.MustDBExec(`UPDATE services SET scraped_at = $1`, s.Clock.Now().UTC())

	// setup some commitments in all states to check the filtering and produce committed_confirmed_unutilized
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

	// permission check
	s.TokenValidator.Enforcer.AllowReportSingle = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/cluster").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReportSingle = true

	// full result with all options
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/cluster?with=info&with=subcapacities&with=commitment_stats&with=timing").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "all options"))

	var (
		withoutInfoMods            = []string{"del(.info)"}
		withoutTimingMods          = []string{`walk(if type == "object" then del(.scraped_at) else . end)`}
		withoutSubcapacitiesMods   = []string{`walk(if type == "object" then del(.subcapacities) else . end)`}
		withoutCommitmentStatsMods = []string{`walk(if type == "object" then del(.committed) else . end)`, `walk(if type == "object" then del(.committed_confirmed_unutilized) else . end)`,
			`walk(if type == "object" then del(.usage_uncommitted) else . end)`}
	)

	// without any extras
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/cluster").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify(withoutInfoMods...).
			Modify(withoutTimingMods...).
			Modify(withoutSubcapacitiesMods...).
			Modify(withoutCommitmentStatsMods...))

	// just one extra at a time
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/cluster?with=subcapacities").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify(withoutInfoMods...).
			Modify(withoutTimingMods...).
			Modify(withoutCommitmentStatsMods...))
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/cluster?with=commitment_stats").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify(withoutInfoMods...).
			Modify(withoutTimingMods...).
			Modify(withoutSubcapacitiesMods...))
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/cluster?with=timing").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify(withoutInfoMods...).
			Modify(withoutSubcapacitiesMods...).
			Modify(withoutCommitmentStatsMods...))

	// filter by area (second area has resources)
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/cluster?with=info&area=second").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "area filter").
			Modify("del(.info.service_areas.first)").
			Modify("del(.cluster_report.service_areas.first)").
			Modify(withoutTimingMods...).
			Modify(withoutSubcapacitiesMods...).
			Modify(withoutCommitmentStatsMods...))

	// filter by service
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/cluster?with=info&service=first").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "service filter").
			Modify("del(.info.service_areas.second)").
			Modify("del(.cluster_report.service_areas.second)").
			Modify(withoutTimingMods...).
			Modify(withoutSubcapacitiesMods...).
			Modify(withoutCommitmentStatsMods...))

	// filter by resource
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/cluster?with=info&resource=capacity").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "resource filter").
			Modify("del(.info.service_areas.first.services.first.categories.first)").
			Modify("del(.info.service_areas.second.services.second.categories.second)").
			Modify("del(.cluster_report.service_areas.first.services.first.categories.first)").
			Modify("del(.cluster_report.service_areas.second.services.second.categories.second)").
			Modify(withoutTimingMods...).
			Modify(withoutSubcapacitiesMods...).
			Modify(withoutCommitmentStatsMods...))

	// filter by category
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/cluster?with=info&category=foo_category").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "category filter").
			Modify("del(.info.service_areas.first.services.first.categories.first)").
			Modify("del(.info.service_areas.second.services.second.categories.second)").
			Modify("del(.cluster_report.service_areas.first.services.first.categories.first)").
			Modify("del(.cluster_report.service_areas.second.services.second.categories.second)").
			Modify(withoutTimingMods...).
			Modify(withoutSubcapacitiesMods...).
			Modify(withoutCommitmentStatsMods...))
}

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
