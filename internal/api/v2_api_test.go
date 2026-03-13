// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"testing"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/test"
)

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

func TestV2InfoAPI(t *testing.T) {
	serviceInfoFirst := test.DefaultLiquidServiceInfo()
	serviceInfoFirst.Rates = map[liquid.RateName]liquid.RateInfo{
		"objects:create":    {Topology: liquid.FlatTopology, HasUsage: true},
		"objects:delete":    {Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"objects:update":    {Topology: liquid.FlatTopology, HasUsage: true},
		"objects:unlimited": {Unit: liquid.UnitKibibytes, Topology: liquid.FlatTopology, HasUsage: true},
	}
	serviceInfoSecond := test.DefaultLiquidServiceInfo()
	s := test.NewSetup(t,
		test.WithConfig(configJSON),
		test.WithPersistedServiceInfo("first", serviceInfoFirst),
		test.WithPersistedServiceInfo("second", serviceInfoSecond),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
		test.WithEmptyRateRecordsAsNeeded,
	)
	firstCapacity := s.GetResourceID("first", "capacity")
	firstObjectsUpdate := s.GetRateID("first", "objects:update")
	firstObjectsDelete := s.GetRateID("first", "objects:delete")
	berlin := s.GetProjectID("berlin")
	dresden := s.GetProjectID("dresden")
	paris := s.GetProjectID("paris")
	s.MustDBExec(`UPDATE project_resources pr SET forbidden = true WHERE pr.project_id = $1 AND pr.resource_id = $2`, berlin, firstCapacity)
	s.MustDBExec(`UPDATE project_rates pra SET rate_limit = 1234, window_ns = 1800000000000 WHERE pra.project_id = $1 AND pra.rate_id IN ($2, $3)`, dresden, firstObjectsUpdate, firstObjectsDelete)

	// we start with cloud_admin permissions, the scope does not matter for this case
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v2/info",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/info-cluster.json"),
	}.Check(t, s.Handler)

	// now a domain admin with a domain scoped token where no resource is forbidden, but commitments disabled
	s.TokenValidator.Enforcer.AllowCluster = false
	s.UpdateMockUserIdentity(map[string]string{
		"project_id": "", "project_domain_id": "", "project_name": "", "project_domain_name": "",
		"domain_id": "uuid-for-france", "domain_name": "france",
	})
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v2/info",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/info-domain-without-commitments.json"),
	}.Check(t, s.Handler)

	// now, the single project in this domain has a forbidden resource, commitments still disabled
	s.MustDBExec(`UPDATE project_resources pr SET forbidden = true WHERE pr.project_id = $1 AND pr.resource_id = $2`, paris, firstCapacity)
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v2/info",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/info-domain-without-commitments-with-forbidden.json"),
	}.Check(t, s.Handler)

	// now a domain admin with a domain scoped token where a resource is forbidden in one project --> no impact
	// commitments are enabled for this domain
	s.UpdateMockUserIdentity(map[string]string{"domain_id": "uuid-for-germany", "domain_name": "germany"})
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v2/info",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/info-domain.json"),
	}.Check(t, s.Handler)

	// now a project admin with a project scoped token where no resource is forbidden, commitments are enabled
	s.TokenValidator.Enforcer.AllowDomain = false
	s.UpdateMockUserIdentity(map[string]string{
		"domain_id": "", "domain_name": "",
		"project_id": "uuid-for-dresden", "project_name": "dresden", "project_domain_id": "uuid-for-germany", "project_domain_name": "germany",
	})
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v2/info",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/info-project-local-rate-limit.json"),
	}.Check(t, s.Handler)

	// now a project admin with a project scoped token where a resource is forbidden, commitments still enabled
	s.UpdateMockUserIdentity(map[string]string{"project_id": "uuid-for-berlin", "project_name": "berlin"})
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v2/info",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/info-project-with-forbidden.json"),
	}.Check(t, s.Handler)

	// now a project admin with a project scoped token where a resource is forbidden and commitments are disabled
	s.UpdateMockUserIdentity(map[string]string{"project_id": "uuid-for-paris", "project_name": "paris", "project_domain_id": "uuid-for-france", "project_domain_name": "france"})
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v2/info",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/info-project-without-commitments-with-forbidden.json"),
	}.Check(t, s.Handler)
}
