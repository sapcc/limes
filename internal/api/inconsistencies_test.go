// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"testing"

	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/test"
)

func TestInconsistencyReport(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(`{
			"availability_zones": ["az-one", "az-two"],
			"discovery": {
				"method": "static",
				"static_config": {
					"domains": [
						{"name": "germany", "id": "uuid-for-germany"},
						{"name": "pakistan", "id": "uuid-for-pakistan"}
					],
					"projects": {
						"uuid-for-germany": [{"name": "dresden", "id": "uuid-for-dresden", "parent_id": "uuid-for-germany"}],
						"uuid-for-pakistan": [{"name": "karachi", "id": "uuid-for-karachi", "parent_id": "uuid-for-pakistan"}]
					}
				}
			},
			"liquids": {
				"shared": {"area": "shared"}
			}
		}`),
		test.WithPersistedServiceInfo("shared", test.DefaultLiquidServiceInfo()),
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
	)

	// initially, we will put in some numbers that do not have any inconsistencies
	s.MustDBExec(`UPDATE project_resources SET quota = 30, backend_quota = 30`)
	s.MustDBExec(`UPDATE project_az_resources SET usage = 9 WHERE az_resource_id IN ($1, $2)`,
		s.GetAZResourceID("shared", "capacity", "az-one"),
		s.GetAZResourceID("shared", "capacity", "az-two"),
	)
	s.MustDBExec(`UPDATE project_az_resources SET usage = 10 WHERE az_resource_id = $1`,
		s.GetAZResourceID("shared", "things", "any"),
	)

	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/inconsistencies",
		ExpectStatus: 200,
		ExpectBody: assert.JSONObject{
			"inconsistencies": assert.JSONObject{
				"domain_quota_overcommitted": []assert.JSONObject{},
				"project_quota_overspent":    []assert.JSONObject{},
				"project_quota_mismatch":     []assert.JSONObject{},
			},
		},
	}.Check(t, s.Handler)

	// now put in some inconsistencies
	s.MustDBExec(`UPDATE project_resources SET backend_quota = 10 WHERE project_id = $1 AND resource_id = $2`,
		s.GetProjectID("dresden"),
		s.GetResourceID("shared", "capacity"),
	)
	s.MustDBExec(`UPDATE project_resources SET quota = 14, backend_quota = 14 WHERE project_id = $1 AND resource_id = $2`,
		s.GetProjectID("karachi"),
		s.GetResourceID("shared", "capacity"),
	)

	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/inconsistencies",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/inconsistency-list.json"),
	}.Check(t, s.Handler)
}
