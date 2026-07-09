// SPDX-FileCopyrightText: 2024 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"net/http"
	"testing"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/common_fixtures"
	"github.com/sapcc/limes/internal/test/oldassert"
)

var liquidCapacityTestConfigJSON = string(must.Return(httptest.NewJQModifiableJSONString(`
	{
		"resource_behavior": [
			{"resource": "first/capacity", "overcommit_factor": 1.5}
		]
	}`, "liquidCapacityTestConfigJSON").
	ModifyWithVariable(".availability_zones = $ref", common_fixtures.AZsOneTwo).
	ModifyWithVariable(". * $ref", common_fixtures.AreaLiquidFirstSecond).
	ModifyWithVariable(".discovery = $ref", common_fixtures.DiscoveryBerlinDresdenParis).
	MarshalJSON()))

func commonLiquidTestSetup(t *testing.T, srvInfo liquid.ServiceInfo) (s test.Setup) {
	t.Helper()
	s = test.NewSetup(t,
		test.WithConfig(liquidCapacityTestConfigJSON),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
		test.WithPersistedServiceInfo("first", srvInfo),
		test.WithPersistedServiceInfo("second", srvInfo),
		test.WithMockLiquidClient("first", srvInfo),
		test.WithMockLiquidClient("second", srvInfo),
	)
	return
}

func TestGetServiceCapacityRequest(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo("")
	resInfo := srvInfo.Resources["capacity"]
	resInfo.NeedsResourceDemand = true // must be set to test rendering of ServiceCapacityRequest.DemandForResource
	srvInfo.Resources["capacity"] = resInfo

	s := commonLiquidTestSetup(t, srvInfo)

	// modify the first Resource that the Setup creates
	s.MustDBExec(
		`UPDATE project_az_resources SET usage = 10 WHERE az_resource_id = $1`,
		s.GetAZResourceID("first", "capacity", "az-one"),
	)

	// endpoint requires cluster show permissions
	s.TokenValidator.Enforcer.AllowView = false
	oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-capacity-request?service_type=first",
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowView = true

	// expect error when service type is missing
	oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-capacity-request",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   oldassert.StringData("missing required parameter: service_type\n"),
	}.Check(t, s.Handler)

	// expect error for invalid service type
	oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-capacity-request?service_type=invalid_service_type",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   oldassert.StringData("unknown service type\n"),
	}.Check(t, s.Handler)

	// happy path
	oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-capacity-request?service_type=first",
		ExpectStatus: 200,
		ExpectBody: oldassert.JSONObject{
			"allAZs": []string{"az-one", "az-two"},
			"demandByResource": oldassert.JSONObject{
				"capacity": oldassert.JSONObject{
					"overcommitFactor": 1.5,
					"perAZ": oldassert.JSONObject{
						"az-one": oldassert.JSONObject{
							"usage":              30,
							"unusedCommitments":  0,
							"pendingCommitments": 0,
						},
						"az-two": oldassert.JSONObject{
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
	srvInfo := test.DefaultLiquidServiceInfo("")
	srvInfo.UsageReportNeedsProjectMetadata = true

	s := commonLiquidTestSetup(t, srvInfo)

	// endpoint requires cluster show permissions
	s.TokenValidator.Enforcer.AllowView = false
	oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?service_type=first&project_id=uuid-for-paris",
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowView = true

	// expect error when service type is missing
	oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?project_id=uuid-for-paris",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   oldassert.StringData("missing required parameter: service_type\n"),
	}.Check(t, s.Handler)

	// expect error when project_id is missing
	oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?service_type=first",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   oldassert.StringData("missing required parameter: project_id\n"),
	}.Check(t, s.Handler)

	// expect error for invalid service type
	oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?service_type=invalid_service_type&project_id=uuid-for-paris",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   oldassert.StringData("unknown service type\n"),
	}.Check(t, s.Handler)

	// expect error for invalid project_id
	oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?service_type=first&project_id=-1",
		ExpectStatus: http.StatusNotFound,
		ExpectBody:   oldassert.StringData("project not found\n"),
	}.Check(t, s.Handler)

	// happy path
	oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?service_type=first&project_id=uuid-for-paris",
		ExpectStatus: 200,
		ExpectBody: oldassert.JSONObject{
			"allAZs": []string{"az-one", "az-two"},
			"projectMetadata": oldassert.JSONObject{
				"uuid": "uuid-for-paris",
				"name": "paris",
				"domain": oldassert.JSONObject{
					"uuid": "uuid-for-france",
					"name": "france",
				},
			},
		},
	}.Check(t, s.Handler)
}
