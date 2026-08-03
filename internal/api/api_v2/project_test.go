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

func TestV2ProjectResourceReport(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(resourceReportConfigJSON),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)
	fixturePath := "./fixtures/resource-projects.json"

	s.Clock.StepBy(time.Hour)

	// set up usage on project_az_resources
	// germany has 2 projects (berlin, dresden), france has 1 (paris)
	s.MustDBExec(`UPDATE project_az_resources SET usage = 10 WHERE az_resource_id IN (SELECT id FROM az_resources WHERE path LIKE '%/capacity/%')`)
	s.MustDBExec(`UPDATE project_az_resources SET usage = 5, physical_usage = 2 WHERE az_resource_id IN (SELECT id FROM az_resources WHERE path LIKE '%/things/%')`)
	// set some quota values
	s.MustDBExec(`UPDATE project_az_resources SET quota = 100 WHERE az_resource_id IN (SELECT id FROM az_resources WHERE path LIKE '%/capacity/%')`)
	s.MustDBExec(`UPDATE project_az_resources SET quota = 50 WHERE az_resource_id IN (SELECT id FROM az_resources WHERE path LIKE '%/things/%')`)
	// set subresources for testing with=subresources
	s.MustDBExec(`UPDATE project_az_resources SET subresources = '[{"name":"sub1"},{"name":"sub2"}]' WHERE az_resource_id IN (SELECT id FROM az_resources WHERE path = 'first/capacity/az-one')`)
	// scraped_at for timing
	s.MustDBExec(`UPDATE project_services SET scraped_at = $1`, s.Clock.Now().UTC())
	// historical_usage for testing with=historical_usage (only rendered for resources with autogrow quota distribution)
	// The format is {"t":[unix_seconds...],"v":[usage_values...]}
	historicalUsageJSON := `{"t":[3000,3300,3600],"v":[5,8,10]}`
	s.MustDBExec(`UPDATE project_az_resources SET historical_usage = $1 WHERE az_resource_id IN (SELECT id FROM az_resources WHERE path LIKE 'first/capacity/az-%')`, historicalUsageJSON)

	// setup commitments for commitment_stats testing
	berlin := s.GetProjectID("berlin")
	firstCapacityAZOne := s.GetAZResourceID("first", "capacity", "az-one")
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

	// permission checks
	s.TokenValidator.Enforcer.AllowReportMultiple = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReportMultiple = true
	s.TokenValidator.Enforcer.AllowReportSingle = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects/uuid-for-paris").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReportSingle = true

	// permission checks for with= params that require special permissions
	s.TokenValidator.Enforcer.ForbidWithTiming = true
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=timing").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.ForbidWithTiming = false

	s.TokenValidator.Enforcer.ForbidWithSubresources = true
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=subresources").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.ForbidWithSubresources = false

	s.TokenValidator.Enforcer.ForbidWithHistoricalUsage = true
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=historical_usage").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.ForbidWithHistoricalUsage = false

	// full result with all options
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=info&with=commitment_stats&with=timing&with=subresources&with=historical_usage&with=constraints").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "all options"))

	var (
		withoutInfoMods            = []string{"del(.info)"}
		withoutTimingMods          = []string{`walk(if type == "object" then del(.scraped_at) else . end)`}
		withoutSubresourcesMods    = []string{`walk(if type == "object" then del(.subresources) else . end)`}
		withoutHistoricalUsageMods = []string{`walk(if type == "object" then del(.historical_usage) else . end)`}
		withoutCommitmentStatsMods = []string{
			`walk(if type == "object" then del(.committed) else . end)`,
			`walk(if type == "object" then del(.committed_confirmed_unutilized) else . end)`,
			`walk(if type == "object" then del(.usage_uncommitted) else . end)`,
		}
		withoutConstraintsMods = []string{
			`walk(if type == "object" then del(.max_quota) else . end)`,
			`walk(if type == "object" then del(.forbid_autogrowth) else . end)`,
		}
	)

	// without any extras
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify(withoutInfoMods...).
			Modify(withoutTimingMods...).
			Modify(withoutSubresourcesMods...).
			Modify(withoutHistoricalUsageMods...).
			Modify(withoutCommitmentStatsMods...).
			Modify(withoutConstraintsMods...))

	// individual with= params
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=commitment_stats").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify(withoutInfoMods...).
			Modify(withoutTimingMods...).
			Modify(withoutSubresourcesMods...).
			Modify(withoutHistoricalUsageMods...).
			Modify(withoutConstraintsMods...))
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=timing").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify(withoutInfoMods...).
			Modify(withoutSubresourcesMods...).
			Modify(withoutHistoricalUsageMods...).
			Modify(withoutCommitmentStatsMods...).
			Modify(withoutConstraintsMods...))
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=subresources").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify(withoutInfoMods...).
			Modify(withoutTimingMods...).
			Modify(withoutHistoricalUsageMods...).
			Modify(withoutCommitmentStatsMods...).
			Modify(withoutConstraintsMods...))
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=historical_usage").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify(withoutInfoMods...).
			Modify(withoutTimingMods...).
			Modify(withoutSubresourcesMods...).
			Modify(withoutCommitmentStatsMods...).
			Modify(withoutConstraintsMods...))
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=constraints").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "no params").
			Modify(withoutInfoMods...).
			Modify(withoutTimingMods...).
			Modify(withoutSubresourcesMods...).
			Modify(withoutHistoricalUsageMods...).
			Modify(withoutCommitmentStatsMods...))

	// single project
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects/uuid-for-paris?with=info&with=commitment_stats").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "project filter paris").
			Modify(`del(.domains["uuid-for-germany"])`).
			Modify(withoutTimingMods...).
			Modify(withoutSubresourcesMods...).
			Modify(withoutHistoricalUsageMods...).
			Modify(withoutConstraintsMods...))
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects/uuid-for-berlin?with=info&with=commitment_stats").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "project filter berlin").
			Modify(`del(.domains["uuid-for-france"])`).
			Modify(`del(.domains["uuid-for-germany"].projects["uuid-for-dresden"])`).
			Modify(withoutTimingMods...).
			Modify(withoutSubresourcesMods...).
			Modify(withoutHistoricalUsageMods...).
			Modify(withoutConstraintsMods...))

	// domain filter
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=info&with=commitment_stats&domain_uuid=uuid-for-france").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain filter france").
			Modify(`del(.domains["uuid-for-germany"])`).
			Modify(withoutTimingMods...).
			Modify(withoutSubresourcesMods...).
			Modify(withoutHistoricalUsageMods...).
			Modify(withoutConstraintsMods...))

	// filter by area
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=info&area=second").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "area filter").
			Modify("del(.info.service_areas.first)").
			Modify("del(.domains[].projects[].service_areas.first)").
			Modify(withoutTimingMods...).
			Modify(withoutSubresourcesMods...).
			Modify(withoutHistoricalUsageMods...).
			Modify(withoutCommitmentStatsMods...).
			Modify(withoutConstraintsMods...))

	// filter by service
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=info&service=first").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "service filter").
			Modify("del(.info.service_areas.second)").
			Modify("del(.domains[].projects[].service_areas.second)").
			Modify(withoutTimingMods...).
			Modify(withoutSubresourcesMods...).
			Modify(withoutHistoricalUsageMods...).
			Modify(withoutCommitmentStatsMods...).
			Modify(withoutConstraintsMods...))

	// filter by resource
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=info&resource=capacity").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "resource filter").
			Modify("del(.info.service_areas.first.services.first.categories.first)").
			Modify("del(.info.service_areas.second.services.second.categories.second)").
			Modify("del(.domains[].projects[].service_areas.first.services.first.categories.first)").
			Modify("del(.domains[].projects[].service_areas.second.services.second.categories.second)").
			Modify(withoutTimingMods...).
			Modify(withoutSubresourcesMods...).
			Modify(withoutHistoricalUsageMods...).
			Modify(withoutCommitmentStatsMods...).
			Modify(withoutConstraintsMods...))

	// filter by category
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?with=info&category=foo_category").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "category filter").
			Modify("del(.info.service_areas.first.services.first.categories.first)").
			Modify("del(.info.service_areas.second.services.second.categories.second)").
			Modify("del(.domains[].projects[].service_areas.first.services.first.categories.first)").
			Modify("del(.domains[].projects[].service_areas.second.services.second.categories.second)").
			Modify(withoutTimingMods...).
			Modify(withoutSubresourcesMods...).
			Modify(withoutHistoricalUsageMods...).
			Modify(withoutCommitmentStatsMods...).
			Modify(withoutConstraintsMods...))

	// scope errors
	s.TokenValidator.Enforcer.IsDomainRole = true
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects").ExpectText(t, http.StatusBadRequest, "specify URL project_uuid or query domain_uuid\n")
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects/uuid-for-paris?domain_uuid=uuid-for-france").ExpectText(t, http.StatusBadRequest, "query domain_uuid cannot be set, when URL project_uuid is set\n")
	s.TokenValidator.Enforcer.IsDomainRole = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects/uuid-for-paris?domain_uuid=uuid-for-france").ExpectText(t, http.StatusBadRequest, "query domain_uuid cannot be set, when URL project_uuid is set\n")

	// unknown domain/project
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects?domain_uuid=does-not-exist").ExpectText(t, http.StatusNotFound, "no such domain (UUID = does-not-exist)\n")
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/projects/does-not-exist").ExpectText(t, http.StatusNotFound, "no such project (UUID = does-not-exist)\n")
}

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

	s.TokenValidator.Enforcer.AllowReportMultiple = false
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReportMultiple = true
	s.TokenValidator.Enforcer.AllowReportSingle = false
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/projects/uuid-for-paris").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowReportSingle = true

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
