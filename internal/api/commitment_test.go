/******************************************************************************
*
*  Copyright 2023 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package api

import (
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/test"
)

const day = 24 * time.Hour

const testCommitmentsYAML = `
	availability_zones: [ az-one, az-two ]
	discovery:
		method: --test-static
	services:
		- service_type: first
			type: --test-generic
		- service_type: second
			type: --test-generic
	resource_behavior:
		# for both service types, "capacity" has commitments, but "things" does not
		- resource: .*/capacity
			commitment_durations: ["1 hour", "2 hours"]
			commitment_min_confirm_date: '1970-01-08T00:00:00Z' # one week after start of mock.Clock
`

func TestCommitmentLifecycleWithDelayedConfirmation(t *testing.T) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(testCommitmentsYAML),
		test.WithAPIHandler(NewV1API),
	)

	//GET returns an empty list if there are no commitments
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{}},
	}.Check(t, s.Handler)

	//create a commitment
	s.Clock.StepBy(1 * time.Hour)
	req1 := assert.JSONObject{
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"amount":            10,
		"duration":          "1 hour",
		"confirm_by":        s.Clock.Now().Add(14 * day).Unix(),
	}
	resp1 := assert.JSONObject{
		"id":                1,
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"amount":            10,
		"unit":              "B",
		"duration":          "1 hour",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"confirm_by":        req1["confirm_by"],
		"expires_at":        s.Clock.Now().Add(14*day + 1*time.Hour).Unix(),
	}
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req1},
		ExpectStatus: http.StatusCreated,
		ExpectBody:   assert.JSONObject{"commitment": resp1},
	}.Check(t, s.Handler)

	//create another commitment
	s.Clock.StepBy(1 * time.Hour)
	req2 := assert.JSONObject{
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            20,
		"duration":          "2 hours",
		"confirm_by":        s.Clock.Now().Add(14 * day).Unix(),
	}
	resp2 := assert.JSONObject{
		"id":                2,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            20,
		"unit":              "B",
		"duration":          "2 hours",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"confirm_by":        req2["confirm_by"],
		"expires_at":        s.Clock.Now().Add(14*day + 2*time.Hour).Unix(),
	}
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req2},
		ExpectStatus: http.StatusCreated,
		ExpectBody:   assert.JSONObject{"commitment": resp2},
	}.Check(t, s.Handler)

	//GET now returns something
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp1, resp2}},
	}.Check(t, s.Handler)

	//check filters on GET
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments?service=first",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp1}},
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments?service=third",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{}},
	}.Check(t, s.Handler)

	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments?resource=capacity",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp1, resp2}},
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments?resource=things",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{}},
	}.Check(t, s.Handler)

	//commitments can be deleted with sufficient privilege
	assert.HTTPRequest{
		Method:       http.MethodDelete,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/2",
		ExpectStatus: http.StatusNoContent,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp1}},
	}.Check(t, s.Handler)

	//confirm the other commitment
	s.Clock.StepBy(1 * time.Hour)
	_, err := s.DB.Exec("UPDATE project_commitments SET confirmed_at = $1, expires_at = $2",
		s.Clock.Now(), s.Clock.Now().Add(2*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}

	//check that the confirmation shows up on GET
	resp1["confirmed_at"] = s.Clock.Now().Unix()
	resp1["expires_at"] = s.Clock.Now().Add(2 * time.Hour).Unix()
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp1}},
	}.Check(t, s.Handler)

	//confirmed deletions can be deleted by cluster admins
	s.TokenValidator.Enforcer.AllowCluster = true
	assert.HTTPRequest{
		Method:       http.MethodDelete,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1",
		ExpectStatus: http.StatusNoContent,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{}},
	}.Check(t, s.Handler)
}

func TestCommitmentLifecycleWithImmediateConfirmation(t *testing.T) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(testCommitmentsYAML),
		test.WithAPIHandler(NewV1API),
	)

	//We will try to create requests for resource "first/capacity" in "az-one" in project "berlin".
	request := func(amount uint64) assert.JSONObject {
		return assert.JSONObject{
			"commitment": assert.JSONObject{
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"amount":            amount,
				"duration":          "1 hour",
			},
		}
	}
	//This AZ resource has 10 capacity, of which 2 are used in "berlin" and 4 are used in other projects.
	//Therefore, "berlin" can commit up to 10-4 = 6 amount.
	maxCommittableCapacity := uint64(6)
	//We will later test with this amount of capacity already committed.
	committedCapacity := uint64(4)

	//the capacity resources have min_confirm_date in the future, which blocks immediate confirmation
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/can-confirm",
		Body:         request(1),
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"result": false},
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         request(1),
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("this commitment needs a `confirm_by` timestamp at or after 1970-01-08T00:00:00Z\n"),
	}.Check(t, s.Handler)

	//move clock forward past the min_confirm_date
	s.Clock.StepBy(14 * day)

	//immediate confirmation for this small commitment request is now possible
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/can-confirm",
		Body:         request(1),
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"result": true},
	}.Check(t, s.Handler)

	//check that we cannot immediately commit to more capacity than available
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/can-confirm",
		Body:         request(maxCommittableCapacity),
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"result": true},
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/can-confirm",
		Body:         request(maxCommittableCapacity + 1),
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"result": false},
	}.Check(t, s.Handler)

	//create a commitment for some of that capacity
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         request(committedCapacity),
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	//check that can-confirm can only confirm the remainder of the available capacity, not more
	remainingCommitableCapacity := maxCommittableCapacity - committedCapacity
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/can-confirm",
		Body:         request(remainingCommitableCapacity),
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"result": true},
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/can-confirm",
		Body:         request(remainingCommitableCapacity + 1),
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"result": false},
	}.Check(t, s.Handler)

	//check that can-confirm ignores expired commitments
	_, err := s.DB.Exec(`UPDATE project_commitments SET expires_at = $1`, s.Clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/can-confirm",
		Body:         request(maxCommittableCapacity),
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"result": true},
	}.Check(t, s.Handler)
}

func TestGetCommitmentsErrorCases(t *testing.T) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(testCommitmentsYAML),
		test.WithAPIHandler(NewV1API),
	)

	//no authentication
	s.TokenValidator.Enforcer.AllowView = false
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowView = true

	//unknown objects along the path
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/unknown/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusNotFound,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/unknown/commitments",
		ExpectStatus: http.StatusNotFound,
	}.Check(t, s.Handler)
}

func TestPutCommitmentErrorCases(t *testing.T) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(testCommitmentsYAML),
		test.WithAPIHandler(NewV1API),
	)

	request := assert.JSONObject{
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"amount":            10,
		"duration":          "1 hour",
		"confirm_by":        s.Clock.Now().Add(14 * day).Unix(),
	}

	//no authentication
	s.TokenValidator.Enforcer.AllowEdit = false
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": request},
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowEdit = true

	//unknown objects along the path
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/unknown/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": request},
		ExpectStatus: http.StatusNotFound,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/unknown/commitments/new",
		Body:         assert.JSONObject{"commitment": request},
		ExpectStatus: http.StatusNotFound,
	}.Check(t, s.Handler)

	//invalid request field: service_type does not exist
	cloned := maps.Clone(request)
	cloned["availability_zone"] = "unknown"
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("no such availability zone\n"),
	}.Check(t, s.Handler)

	//invalid request field: service_type does not exist
	cloned = maps.Clone(request)
	cloned["service_type"] = "unknown"
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("no such service\n"),
	}.Check(t, s.Handler)

	//invalid request field: resource_name does not exist
	cloned = maps.Clone(request)
	cloned["resource_name"] = "unknown"
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("no such resource\n"),
	}.Check(t, s.Handler)

	//invalid request field: resource_name does not accept commitments
	cloned = maps.Clone(request)
	cloned["resource_name"] = "things"
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("commitments are not enabled for this resource\n"),
	}.Check(t, s.Handler)

	//invalid request field: duration is not one of the configured values
	cloned = maps.Clone(request)
	cloned["duration"] = "3 hours"
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("unacceptable commitment duration for this resource, acceptable values: [\"1 hour\",\"2 hours\"]\n"),
	}.Check(t, s.Handler)

	//invalid request field: amount may not be negative (this is caught by the JSON parser)
	cloned = maps.Clone(request)
	cloned["amount"] = -42
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("request body is not valid JSON: json: cannot unmarshal number -42 into Go struct field CommitmentRequest.commitment.amount of type uint64\n"),
	}.Check(t, s.Handler)

	//invalid request field: amount may not be zero (this is caught by our logic)
	cloned = maps.Clone(request)
	cloned["amount"] = 0
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("amount of committed resource must be greater than zero\n"),
	}.Check(t, s.Handler)
}

func TestDeleteCommitmentErrorCases(t *testing.T) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(testCommitmentsYAML),
		test.WithAPIHandler(NewV1API),
	)

	//we need a commitment in the DB to test deletion
	request := assert.JSONObject{
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"amount":            10,
		"duration":          "1 hour",
		"confirm_by":        s.Clock.Now().Add(14 * day).Unix(),
	}
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": request},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	//no authentication
	s.TokenValidator.Enforcer.AllowUncommit = false
	assert.HTTPRequest{
		Method:       http.MethodDelete,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1",
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowUncommit = true

	//unknown objects along the path
	assert.HTTPRequest{
		Method:       http.MethodDelete,
		Path:         "/v1/domains/unknown/projects/uuid-for-berlin/commitments/1",
		ExpectStatus: http.StatusNotFound,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodDelete,
		Path:         "/v1/domains/uuid-for-germany/projects/unknown/commitments/1",
		ExpectStatus: http.StatusNotFound,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodDelete,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/2",
		ExpectStatus: http.StatusNotFound,
		ExpectBody:   assert.StringData("no such commitment\n"),
	}.Check(t, s.Handler)
}
