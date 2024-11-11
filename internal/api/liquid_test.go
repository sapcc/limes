/*******************************************************************************
*
* Copyright 2024 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package api

import (
	"net/http"
	"testing"

	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/test"
)

const (
	liquidQuotaTestConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: --test-static
			params:
				domains:
					- { name: germany, id: uuid-for-germany }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
		services:
			- service_type: unittest
				type: --test-generic
	`
	liquidCapacityTestConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: --test-static
		services:
			- service_type: unittest
				type: --test-generic
		capacitors:
		- id: unittest
			type: --test-static
	`
)

func TestGetServiceCapacityRequest(t *testing.T) {
	t.Helper()
	s := test.NewSetup(t,
		test.WithConfig(liquidCapacityTestConfigYAML),
		test.WithAPIHandler(NewV1API),
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
						"any": assert.JSONObject{
							"usage":              10,
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
	t.Helper()
	s := test.NewSetup(t,
		test.WithConfig(liquidQuotaTestConfigYAML),
		test.WithAPIHandler(NewV1API),
		test.WithDBFixtureFile("fixtures/start-data.sql"),
	)

	// endpoint requires cluster show permissions
	s.TokenValidator.Enforcer.AllowView = false
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?service_type=unittest&project_id=uuid-for-berlin",
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowView = true

	// expect error when service type is missing
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/liquid/service-usage-request?project_id=uuid-for-berlin",
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
		Path:         "/admin/liquid/service-usage-request?service_type=invalid_service_type&project_id=uuid-for-berlin",
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
		Path:         "/admin/liquid/service-usage-request?service_type=unittest&project_id=uuid-for-berlin",
		ExpectStatus: 200,
		ExpectBody: assert.JSONObject{
			"allAZs": []string{"az-one", "az-two"},
			"projectMetadata": assert.JSONObject{
				"uuid": "uuid-for-berlin",
				"name": "berlin",
				"domain": assert.JSONObject{
					"uuid": "uuid-for-germany",
					"name": "germany",
				},
			},
		},
	}.Check(t, s.Handler)
}
