// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"net/http"
	"testing"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/test"
)

const (
	liquidCapacityTestConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: static
			static_config:
				domains: [{ name: domain-1, id: uuid-for-domain-1 }]
				projects:
					uuid-for-domain-1: [{ name: project-1, id: uuid-for-project-1, parent_id: uuid-for-domain-1 }]
		liquids:
			unittest: { area: testing }
		resource_behavior:
		- { resource: unittest/capacity, overcommit_factor: 1.5 }
	`
)

func commonLiquidTestSetup(t *testing.T, srvInfo liquid.ServiceInfo) (s test.Setup) {
	t.Helper()
	s = test.NewSetup(t,
		test.WithConfig(liquidCapacityTestConfigYAML),
		test.WithInitialDiscovery,
		test.WithEmptyRecordsAsNeeded,
		test.WithPersistedServiceInfo("unittest", srvInfo),
		test.WithMockLiquidClient("unittest", srvInfo),
	)
	return
}

func TestGetServiceCapacityRequest(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo()
	resInfo := srvInfo.Resources["capacity"]
	resInfo.NeedsResourceDemand = true // must be set to test rendering of ServiceCapacityRequest.DemandForResource
	srvInfo.Resources["capacity"] = resInfo

	s := commonLiquidTestSetup(t, srvInfo)

	// modify the first Resource that the Setup creates
	s.MustDBExec(
		`UPDATE project_az_resources SET usage = 10 WHERE az_resource_id = $1`,
		s.GetAZResourceID("unittest", "capacity", "az-one"),
	)

	// endpoint requires cluster show permissions
	s.TokenValidator.Enforcer.AllowView = false
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-capacity-request?service_type=unittest",
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowView = true

	// expect error when service type is missing
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-capacity-request",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("missing required parameter: service_type\n"),
	}.Check(t, s.Handler)

	// expect error for invalid service type
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-capacity-request?service_type=invalid_service_type",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("invalid service type\n"),
	}.Check(t, s.Handler)

	// happy path
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-capacity-request?service_type=unittest",
		ExpectStatus: 200,
		ExpectBody: assert.JSONObject{
			"allAZs": []string{"az-one", "az-two"},
			"demandByResource": assert.JSONObject{
				"capacity": assert.JSONObject{
					"overcommitFactor": 1.5,
					"perAZ": assert.JSONObject{
						"az-one": assert.JSONObject{
							"usage":              10,
							"unusedCommitments":  0,
							"pendingCommitments": 0,
						},
						"az-two": assert.JSONObject{
							"usage":              0,
							"unusedCommitments":  0,
							"pendingCommitments": 0,
						},
					},
				},
			},
		},
	}.Check(t, s.Handler)
}

func TestServiceUsageRequest(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo()
	srvInfo.UsageReportNeedsProjectMetadata = true

	s := commonLiquidTestSetup(t, srvInfo)

	// endpoint requires cluster show permissions
	s.TokenValidator.Enforcer.AllowView = false
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?service_type=unittest&project_id=uuid-for-project-1",
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowView = true

	// expect error when service type is missing
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?project_id=uuid-for-project-1",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("missing required parameter: service_type\n"),
	}.Check(t, s.Handler)

	// expect error when project_id is missing
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?service_type=unittest",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("missing required parameter: project_id\n"),
	}.Check(t, s.Handler)

	// expect error for invalid service type
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?service_type=invalid_service_type&project_id=uuid-for-project-1",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("invalid service type\n"),
	}.Check(t, s.Handler)

	// expect error for invalid project_id
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?service_type=unittest&project_id=-1",
		ExpectStatus: http.StatusNotFound,
		ExpectBody:   assert.StringData("project not found\n"),
	}.Check(t, s.Handler)

	// happy path
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?service_type=unittest&project_id=uuid-for-project-1",
		ExpectStatus: 200,
		ExpectBody: assert.JSONObject{
			"allAZs": []string{"az-one", "az-two"},
			"projectMetadata": assert.JSONObject{
				"uuid": "uuid-for-project-1",
				"name": "project-1",
				"domain": assert.JSONObject{
					"uuid": "uuid-for-domain-1",
					"name": "domain-1",
				},
			},
		},
	}.Check(t, s.Handler)
}
