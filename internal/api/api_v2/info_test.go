// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/httptest"

	"github.com/sapcc/limes/internal/test"
)

func TestMain(m *testing.M) {
	easypg.WithTestDB(m, func() int { return m.Run() })
}

const configJSON = `{
	"availability_zones": ["az-one", "az-two"],
	"discovery": {
		"method": "static",
		"static_config": {
			"domains": [
				{"name": "germany", "id": "uuid-for-germany"},
				{"name": "france", "id": "uuid-for-france"}
			],
			"projects": {
				"uuid-for-germany": [
					{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"},
					{"name": "dresden", "id": "uuid-for-dresden", "parent_id": "uuid-for-berlin"}
				],
				"uuid-for-france": [
					{"name": "paris", "id": "uuid-for-paris", "parent_id": "uuid-for-france"}
				]
			}
		}
	},
	"areas": { "first": { "display_name": "First" }, "second": { "display_name": "Second" }},
	"liquids": {
		"first": {
			"area": "first",
			"commitment_behavior_per_resource": [],
			"rate_limits": {
				"global": [
					{"name": "objects:create", "limit": 5000, "window": "1s"}
				],
				"project_default": [
					{"name": "objects:create", "limit": 5, "window": "1m"},
					{"name": "objects:update", "limit": 2, "window": "1s"}
				]
			}
		},
		"second": {
			"area": "second",
			"commitment_behavior_per_resource": [{
				"key": "capacity",
				"value": {
					"durations_per_domain": [{"key": "germany", "value": ["1 hour", "2 hours"]}],
					"min_confirm_date": "1970-01-08T00:00:00Z"
				}
			}]
		}
	}
}`

func TestV2ResourcesInfoAPI(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(configJSON),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
		test.WithEmptyRateRecordsAsNeeded,
	)
	firstCapacity := s.GetResourceID("first", "capacity")
	berlin := s.GetProjectID("berlin")
	paris := s.GetProjectID("paris")
	s.MustDBExec(`UPDATE project_resources pr SET forbidden = true WHERE pr.project_id = $1 AND pr.resource_id = $2`, berlin, firstCapacity)
	fixturePath := "./fixtures/resource-info.json"

	// we start with cloud_admin permissions, the scope does not matter for this case
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "cloud-admin"))

	// now a domain admin with a domain scoped token where no resource is forbidden, but commitments disabled
	s.TokenValidator.Enforcer.AllowCluster = false
	s.UpdateMockUserIdentity(map[string]string{
		"project_id": "", "project_domain_id": "", "project_name": "", "project_domain_name": "",
		"domain_id": "uuid-for-france", "domain_name": "france",
	})
	pathToModify := ".service_areas.second.services.second.categories.foo_category.resources.capacity"
	commitmentMod := []string{
		fmt.Sprintf("del(%s.commitment_config)", pathToModify),
		pathToModify + ".has_quota = true",
	}
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain-commitments-disabled").
			Modify(commitmentMod...))

	// now, the single project in this domain has a forbidden resource, commitments still disabled
	s.MustDBExec(`UPDATE project_resources pr SET forbidden = true WHERE pr.project_id = $1 AND pr.resource_id = $2`, paris, firstCapacity)
	forbiddenMod := "del(.service_areas.first.services.first.categories.foo_category)"
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain-forbidden-resource").
			Modify(commitmentMod...).Modify(forbiddenMod))

	// now a domain admin with a domain scoped token where a resource is forbidden in one project --> no impact
	// commitments are enabled for this domain
	s.UpdateMockUserIdentity(map[string]string{"domain_id": "uuid-for-germany", "domain_name": "germany"})
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain-germany"))

	// now a project admin with a project scoped token where no resource is forbidden, commitments are enabled
	s.TokenValidator.Enforcer.AllowDomain = false
	s.UpdateMockUserIdentity(map[string]string{
		"domain_id": "", "domain_name": "",
		"project_id": "uuid-for-dresden", "project_name": "dresden", "project_domain_id": "uuid-for-germany", "project_domain_name": "germany",
	})
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "project-dresden"))

	// now a project admin with a project scoped token where a resource is forbidden, commitments still enabled
	s.UpdateMockUserIdentity(map[string]string{"project_id": "uuid-for-berlin", "project_name": "berlin"})
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "project-forbidden-resource").
			Modify(forbiddenMod))

	// now a project admin with a project scoped token where a resource is forbidden and commitments are disabled
	s.UpdateMockUserIdentity(map[string]string{"project_id": "uuid-for-paris", "project_name": "paris", "project_domain_id": "uuid-for-france", "project_domain_name": "france"})
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "project-forbidden-no-commitments").
			Modify(forbiddenMod).Modify(commitmentMod...))
}

func TestV2RatesInfoAPI(t *testing.T) {
	serviceInfoFirst := test.DefaultLiquidServiceInfo("First")
	serviceInfoFirst.Rates = map[liquid.RateName]liquid.RateInfo{
		"objects:create":    {DisplayName: "Object Creations", Topology: liquid.FlatTopology, HasUsage: true},
		"objects:delete":    {DisplayName: "Object Deletions", Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"objects:update":    {DisplayName: "Object Updates", Topology: liquid.FlatTopology, HasUsage: true},
		"objects:unlimited": {DisplayName: "Object Unlimited Operations", Unit: liquid.UnitKibibytes, Topology: liquid.FlatTopology, HasUsage: true},
	}
	serviceInfoSecond := test.DefaultLiquidServiceInfo("Second")
	s := test.NewSetup(t,
		test.WithConfig(configJSON),
		test.WithPersistedServiceInfo("first", serviceInfoFirst),
		test.WithPersistedServiceInfo("second", serviceInfoSecond),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
		test.WithEmptyRateRecordsAsNeeded,
	)
	fixturePath := "./fixtures/rate-info.json"

	// we start with cloud_admin permissions, the scope does not matter for this case
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "cloud-admin"))

	// now a domain admin with a domain scoped token, he cannot see global limits
	s.TokenValidator.Enforcer.AllowCluster = false
	s.UpdateMockUserIdentity(map[string]string{
		"project_id": "", "project_domain_id": "", "project_name": "", "project_domain_name": "",
		"domain_id": "uuid-for-france", "domain_name": "france",
	})
	path := `.service_areas.first.services.first.rates["objects:create"]`
	globalDefaultLimitMods := []string{
		fmt.Sprintf(`del(%s.default_limit)`, path),
		fmt.Sprintf(`del(%s.default_window)`, path),
	}

	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "domain-no-global-limit").
			Modify(globalDefaultLimitMods...))

	// now a project admin with a project scoped token, he sees project default limits
	s.TokenValidator.Enforcer.AllowDomain = false
	s.UpdateMockUserIdentity(map[string]string{
		"domain_id": "", "domain_name": "",
		"project_id": "uuid-for-dresden", "project_name": "dresden", "project_domain_id": "uuid-for-germany", "project_domain_name": "germany",
	})
	projectDefaultLimitMods := []string{
		`.service_areas.first.services.first.rates["objects:create"] += {"default_limit": 5, "default_window": "1m"}`,
		`.service_areas.first.services.first.rates["objects:update"] += {"default_limit": 2, "default_window": "1s"}`,
	}
	s.Handler.RespondTo(s.Ctx, "GET /rates/v2/info").ExpectJSON(t, http.StatusOK,
		httptest.NewJQModifiableJSONFixture(fixturePath, "project-no-custom-limit").
			Modify(globalDefaultLimitMods...).Modify(projectDefaultLimitMods...))
}
