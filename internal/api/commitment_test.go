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
	"encoding/json"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/db"
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
		# the resources in "first" have commitments, the ones in "second" do not
		- resource: first/.*
			commitment_durations: ["1 hour", "2 hours"]
			commitment_min_confirm_date: '1970-01-08T00:00:00Z' # one week after start of mock.Clock
		- resource: first/things
			commitment_is_az_aware: false
		- resource: first/capacity
			commitment_is_az_aware: true
`
const testCommitmentsYAMLWithoutMinConfirmDate = `
	availability_zones: [ az-one, az-two ]
	discovery:
		method: --test-static
	services:
		- service_type: first
			type: --test-generic
		- service_type: second
			type: --test-generic
	resource_behavior:
		# the resources in "first" have commitments, the ones in "second" do not
		- resource: second/.*
			commitment_durations: ["1 hour", "2 hours"]
		- resource: second/things
			commitment_is_az_aware: false
		- resource: second/capacity
			commitment_is_az_aware: true
`

func TestCommitmentLifecycleWithDelayedConfirmation(t *testing.T) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(testCommitmentsYAML),
		test.WithAPIHandler(NewV1API),
	)

	// GET returns an empty list if there are no commitments
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{}},
	}.Check(t, s.Handler)

	// create a commitment
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
		"can_be_deleted":    true,
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

	// create another commitment
	s.Clock.StepBy(1 * time.Hour)
	req2 := assert.JSONObject{
		"service_type":      "first",
		"resource_name":     "things",
		"availability_zone": "any",
		"amount":            20,
		"duration":          "2 hours",
		"confirm_by":        s.Clock.Now().Add(14 * day).Unix(),
	}
	resp2 := assert.JSONObject{
		"id":                2,
		"service_type":      "first",
		"resource_name":     "things",
		"availability_zone": "any",
		"amount":            20,
		"duration":          "2 hours",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
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

	// GET now returns something
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp1, resp2}},
	}.Check(t, s.Handler)

	// after 24 hours have passed, `can_be_deleted` is still true if the user has the "uncommit" permission...
	s.Clock.StepBy(48 * time.Hour)
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp1, resp2}},
	}.Check(t, s.Handler)
	// ...but otherwise flips to false
	s.TokenValidator.Enforcer.AllowUncommit = false
	delete(resp1, "can_be_deleted")
	delete(resp2, "can_be_deleted")
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp1, resp2}},
	}.Check(t, s.Handler)

	// check filters on GET
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments?service=first",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp1, resp2}},
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
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp1}},
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments?resource=blobs",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{}},
	}.Check(t, s.Handler)

	// commitments can be deleted with sufficient privilege
	s.TokenValidator.Enforcer.AllowUncommit = true
	assert.HTTPRequest{
		Method:       http.MethodDelete,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/2",
		ExpectStatus: http.StatusNoContent,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowUncommit = false
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp1}},
	}.Check(t, s.Handler)

	// fresh commitments can also be deleted without privilege
	s.Clock.StepBy(1 * time.Hour)
	req3 := assert.JSONObject{
		"service_type":      "first",
		"resource_name":     "things",
		"availability_zone": "any",
		"amount":            30,
		"duration":          "2 hours",
		"confirm_by":        s.Clock.Now().Add(14 * day).Unix(),
	}
	resp3 := assert.JSONObject{
		"id":                3,
		"service_type":      "first",
		"resource_name":     "things",
		"availability_zone": "any",
		"amount":            30,
		"duration":          "2 hours",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirm_by":        req3["confirm_by"],
		"expires_at":        s.Clock.Now().Add(14*day + 2*time.Hour).Unix(),
	}
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req3},
		ExpectStatus: http.StatusCreated,
		ExpectBody:   assert.JSONObject{"commitment": resp3},
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodDelete,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/3",
		ExpectStatus: http.StatusNoContent,
	}.Check(t, s.Handler)

	// confirm the remaining commitment
	s.Clock.StepBy(1 * time.Hour)
	_, err := s.DB.Exec("UPDATE project_commitments SET confirmed_at = $1, expires_at = $2, state = $3",
		s.Clock.Now(), s.Clock.Now().Add(2*time.Hour), db.CommitmentStateActive,
	)
	if err != nil {
		t.Fatal(err)
	}

	// check that the confirmation shows up on GET
	resp1["confirmed_at"] = s.Clock.Now().Unix()
	resp1["expires_at"] = s.Clock.Now().Add(2 * time.Hour).Unix()
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp1}},
	}.Check(t, s.Handler)

	// confirmed deletions can be deleted by cluster admins
	s.TokenValidator.Enforcer.AllowUncommit = true
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

	// We will try to create requests for resource "first/capacity" in "az-one" in project "berlin".
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
	// This AZ resource has 10 capacity, of which 2 are used in "berlin" and 4 are used in other projects.
	// Therefore, "berlin" can commit up to 10-4 = 6 amount.
	maxCommittableCapacity := uint64(6)
	// We will later test with this amount of capacity already committed.
	committedCapacity := uint64(4)

	// the capacity resources have min_confirm_date in the future, which blocks immediate confirmation
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

	// move clock forward past the min_confirm_date
	s.Clock.StepBy(14 * day)

	// immediate confirmation for this small commitment request is now possible
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/can-confirm",
		Body:         request(1),
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"result": true},
	}.Check(t, s.Handler)

	// check that we cannot immediately commit to more capacity than available
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

	// create a commitment for some of that capacity
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         request(committedCapacity),
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	// check that can-confirm can only confirm the remainder of the available capacity, not more
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

	// check that can-confirm ignores expired commitments
	_, err := s.DB.Exec(`UPDATE project_commitments SET expires_at = $1, state = $2`,
		s.Clock.Now(), db.CommitmentStateExpired)
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

	// no authentication
	s.TokenValidator.Enforcer.AllowView = false
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowView = true

	// unknown objects along the path
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

	// no authentication
	s.TokenValidator.Enforcer.AllowEdit = false
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": request},
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowEdit = true

	// unknown objects along the path
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

	// invalid request field: service_type does not exist
	cloned := maps.Clone(request)
	cloned["service_type"] = "unknown"
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("no such service\n"),
	}.Check(t, s.Handler)

	// invalid request field: resource_name does not exist
	cloned = maps.Clone(request)
	cloned["resource_name"] = "unknown"
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("no such resource\n"),
	}.Check(t, s.Handler)

	// invalid request field: service_type/resource_name does not accept commitments
	cloned = maps.Clone(request)
	cloned["service_type"] = "second"
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("commitments are not enabled for this resource\n"),
	}.Check(t, s.Handler)

	// invalid request field: AZ given, but resource does not accept AZ-aware commitments
	cloned = maps.Clone(request)
	cloned["resource_name"] = "things"
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("resource does not accept AZ-aware commitments, so the AZ must be set to \"any\"\n"),
	}.Check(t, s.Handler)

	// invalid request field: resource wants an AZ-aware commitment, but a malformed AZ or pseudo-AZ is given
	for _, az := range []string{"any", "unknown", "something-else", ""} {
		cloned = maps.Clone(request)
		cloned["availability_zone"] = az
		assert.HTTPRequest{
			Method:       http.MethodPost,
			Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
			Body:         assert.JSONObject{"commitment": cloned},
			ExpectStatus: http.StatusUnprocessableEntity,
			ExpectBody:   assert.StringData("no such availability zone\n"),
		}.Check(t, s.Handler)
	}

	// invalid request field: duration is not one of the configured values
	cloned = maps.Clone(request)
	cloned["duration"] = "3 hours"
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("unacceptable commitment duration for this resource, acceptable values: [\"1 hour\",\"2 hours\"]\n"),
	}.Check(t, s.Handler)

	// invalid request field: amount may not be negative (this is caught by the JSON parser)
	cloned = maps.Clone(request)
	cloned["amount"] = -42
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("request body is not valid JSON: json: cannot unmarshal number -42 into Go struct field CommitmentRequest.commitment.amount of type uint64\n"),
	}.Check(t, s.Handler)

	// invalid request field: amount may not be zero (this is caught by our logic)
	cloned = maps.Clone(request)
	cloned["amount"] = 0
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("amount of committed resource must be greater than zero\n"),
	}.Check(t, s.Handler)

	// invalid request field: confirm_by may not be in the past
	cloned = maps.Clone(request)
	cloned["confirm_by"] = s.Clock.Now().Add(-1 * time.Hour).Unix()
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("confirm_by must not be set in the past\n"),
	}.Check(t, s.Handler)
}

func TestDeleteCommitmentErrorCases(t *testing.T) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(testCommitmentsYAML),
		test.WithAPIHandler(NewV1API),
	)

	// we need a commitment in the DB to test deletion
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

	// no authentication
	s.TokenValidator.Enforcer.AllowUncommit = false
	s.Clock.StepBy(48 * time.Hour) // skip over the phase where fresh commitments can be deleted by their creators
	assert.HTTPRequest{
		Method:       http.MethodDelete,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1",
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowUncommit = true

	// unknown objects along the path
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

func Test_StartCommitmentTransfer(t *testing.T) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(testCommitmentsYAMLWithoutMinConfirmDate),
		test.WithAPIHandler(NewV1API),
	)

	var transferToken = test.GenerateDummyToken()

	// Test on confirmed commitment should succeed.
	// TransferAmount >= CommitmentAmount
	req1 := assert.JSONObject{
		"id":                1,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            10,
		"duration":          "1 hour",
		"transfer_status":   "",
		"transfer_token":    "",
	}

	resp1 := assert.JSONObject{
		"id":                1,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            10,
		"unit":              "B",
		"duration":          "1 hour",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      0,
		"expires_at":        3600,
		"transfer_status":   "unlisted",
		"transfer_token":    transferToken,
	}

	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req1},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/start-transfer",
		ExpectStatus: http.StatusAccepted,
		ExpectBody:   assert.JSONObject{"commitment": resp1},
		Body:         assert.JSONObject{"commitment": assert.JSONObject{"amount": 10, "transfer_status": "unlisted"}},
	}.Check(t, s.Handler)

	// TransferAmount < CommitmentAmount
	resp2 := assert.JSONObject{
		"id":                2,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            9,
		"unit":              "B",
		"duration":          "1 hour",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      0,
		"expires_at":        3600,
		"transfer_status":   "public",
		"transfer_token":    transferToken,
	}

	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/start-transfer",
		ExpectStatus: http.StatusAccepted,
		ExpectBody:   assert.JSONObject{"commitment": resp2},
		Body:         assert.JSONObject{"commitment": assert.JSONObject{"amount": 9, "transfer_status": "public"}},
	}.Check(t, s.Handler)

	// Negative Test, amount = 0.
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/start-transfer",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("delivered amount needs to be a positive value.\n"),
		Body:         assert.JSONObject{"commitment": assert.JSONObject{"amount": 0, "transfer_status": "public"}},
	}.Check(t, s.Handler)

	// Negative Test, delivered amount > commitment amount
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/start-transfer",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("delivered amount exceeds the commitment amount.\n"),
		Body:         assert.JSONObject{"commitment": assert.JSONObject{"amount": 11, "transfer_status": "public"}},
	}.Check(t, s.Handler)
}

func Test_GetCommitmentByToken(t *testing.T) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(testCommitmentsYAMLWithoutMinConfirmDate),
		test.WithAPIHandler(NewV1API),
	)

	var transferToken = test.GenerateDummyToken()
	// Prepare a commitment to test against in transfer mode.
	req1 := assert.JSONObject{
		"id":                1,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            10,
		"duration":          "1 hour",
	}
	req2 := assert.JSONObject{
		"id":                2,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            20,
		"duration":          "1 hour",
	}

	resp1 := assert.JSONObject{
		"id":                1,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            10,
		"unit":              "B",
		"duration":          "1 hour",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      0,
		"expires_at":        3600,
		"transfer_status":   "unlisted",
		"transfer_token":    transferToken,
	}
	resp2 := assert.JSONObject{
		"id":                2,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            20,
		"unit":              "B",
		"duration":          "1 hour",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      0,
		"expires_at":        3600,
		"transfer_status":   "unlisted",
		"transfer_token":    transferToken,
	}

	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req1},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/start-transfer",
		ExpectStatus: http.StatusAccepted,
		ExpectBody:   assert.JSONObject{"commitment": resp1},
		Body:         assert.JSONObject{"commitment": assert.JSONObject{"amount": 10, "transfer_status": "unlisted"}},
	}.Check(t, s.Handler)

	// Get commitment by token.
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/commitments/" + transferToken,
		ExpectBody:   assert.JSONObject{"commitment": resp1},
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)

	// Now check a token that does not exist.
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/commitments/" + "notExistingToken",
		ExpectStatus: http.StatusNotFound,
		ExpectBody:   assert.StringData("no matching commitment found.\n"),
	}.Check(t, s.Handler)

	// Token exists twice in the database.
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden/commitments/new",
		Body:         assert.JSONObject{"commitment": req2},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden/commitments/2/start-transfer",
		ExpectStatus: http.StatusAccepted,
		ExpectBody:   assert.JSONObject{"commitment": resp2},
		Body:         assert.JSONObject{"commitment": assert.JSONObject{"amount": 20, "transfer_status": "unlisted"}},
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/commitments/" + transferToken,
		ExpectStatus: http.StatusConflict,
		ExpectBody:   assert.StringData("detected multiple commitments with the same token.\n"),
	}.Check(t, s.Handler)
}

func Test_TransferCommitment(t *testing.T) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(testCommitmentsYAMLWithoutMinConfirmDate),
		test.WithAPIHandler(NewV1API),
	)

	var transferToken = test.GenerateDummyToken()
	req1 := assert.JSONObject{
		"id":                1,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            10,
		"duration":          "1 hour",
		"transfer_status":   "",
	}

	resp1 := assert.JSONObject{
		"id":                1,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            10,
		"unit":              "B",
		"duration":          "1 hour",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      0,
		"expires_at":        3600,
		"transfer_status":   "unlisted",
		"transfer_token":    transferToken,
	}

	resp2 := assert.JSONObject{
		"id":                1,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            10,
		"unit":              "B",
		"duration":          "1 hour",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      0,
		"expires_at":        3600,
	}

	// Split commitment
	resp3 := assert.JSONObject{
		"id":                2,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            9,
		"unit":              "B",
		"duration":          "1 hour",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      0,
		"expires_at":        3600,
		"transfer_status":   "unlisted",
		"transfer_token":    transferToken,
	}
	resp4 := assert.JSONObject{
		"id":                2,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            9,
		"unit":              "B",
		"duration":          "1 hour",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      0,
		"expires_at":        3600,
	}

	// Transfer Commitment to target AZ_RESOURCE_ID (SOURCE_ID=3 TARGET_ID=17)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req1},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	// Transfer full amount
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/start-transfer",
		ExpectStatus: http.StatusAccepted,
		ExpectBody:   assert.JSONObject{"commitment": resp1},
		Body:         assert.JSONObject{"commitment": assert.JSONObject{"amount": 10, "transfer_status": "unlisted"}},
	}.Check(t, s.Handler)

	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden/transfer-commitment/1",
		Header:       map[string]string{"Transfer-Token": transferToken},
		ExpectBody:   assert.JSONObject{"commitment": resp2},
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)

	// Split and transfer commitment.
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden/commitments/1/start-transfer",
		ExpectStatus: http.StatusAccepted,
		ExpectBody:   assert.JSONObject{"commitment": resp3},
		Body:         assert.JSONObject{"commitment": assert.JSONObject{"amount": 9, "transfer_status": "unlisted"}},
	}.Check(t, s.Handler)

	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/transfer-commitment/2",
		Header:       map[string]string{"Transfer-Token": transferToken},
		ExpectBody:   assert.JSONObject{"commitment": resp4},
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)

	var supersededCommitment db.ProjectCommitment
	err := s.DB.SelectOne(&supersededCommitment, `SELECT * FROM project_commitments where ID = 1`)
	if err != nil {
		t.Fatal(err)
	}
	if supersededCommitment.State != db.CommitmentStateSuperseded {
		t.Fatalf("commitment state should be %v. Received %v instead.", db.CommitmentStateSuperseded, supersededCommitment.State)
	}

	var splitCommitment db.ProjectCommitment
	err = s.DB.SelectOne(&splitCommitment, `SELECT * FROM project_commitments where ID = 2`)
	if err != nil {
		t.Fatal(err)
	}
	if splitCommitment.State != db.CommitmentStateActive {
		t.Fatalf("commitment state should be %v. Received %v instead.", db.CommitmentStateActive, splitCommitment.State)
	}

	// wrong token
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden/transfer-commitment/1",
		Header:       map[string]string{"Transfer-Token": "wrongToken"},
		ExpectStatus: http.StatusNotFound,
		ExpectBody:   assert.StringData("no matching commitment found\n"),
	}.Check(t, s.Handler)

	// No token provided
	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/transfer-commitment/1",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("no transfer token provided\n"),
	}.Check(t, s.Handler)
}

func Test_TransferCommitmentForbiddenByCapacityCheck(t *testing.T) {
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(testCommitmentsYAMLWithoutMinConfirmDate),
		test.WithAPIHandler(NewV1API),
	)

	// create commitments for resource "second/capacity" in AZ "az-one"
	// for all projects, so that all existing capacity is covered
	// (capacity = 30, and each project has usage = 1)
	req := assert.JSONObject{
		"commitment": assert.JSONObject{
			"service_type":      "second",
			"resource_name":     "capacity",
			"availability_zone": "az-one",
			"amount":            10,
			"duration":          "1 hour",
		},
	}
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         req,
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden/commitments/new",
		Body:         req,
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-france/projects/uuid-for-paris/commitments/new",
		Body:         req,
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	// test that this situation makes it impossible to move commitments between projects (without splitting them)
	//
	// The reason for that is that we need to enforce `sum_over_projects(max(committed, usage)) <= capacity`.
	// In other words, all existing commitments and usage must be covered by capacity. Since we already have
	// `sum_over_projects(committed) == capacity`, i.e. all capacity is committed somewhere, usage must stay
	// within these commitments in each project. Otherwise, the total amount of capacity allocated to usage
	// and/or commitments would exceed the available capacity.
	_, respBodyBytes := assert.HTTPRequest{
		Method: http.MethodPost,
		Path:   "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/start-transfer",
		Body: assert.JSONObject{
			"commitment": assert.JSONObject{
				"amount":          10,
				"transfer_status": "unlisted",
			},
		},
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)

	var resp struct {
		Commitment struct {
			TransferToken string `json:"transfer_token"`
		} `json:"commitment"`
	}
	err := json.Unmarshal(respBodyBytes, &resp)
	if err != nil {
		t.Fatal(err)
	}

	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden/transfer-commitment/1",
		Header:       map[string]string{"Transfer-Token": resp.Commitment.TransferToken},
		ExpectBody:   assert.StringData("not enough committable capacity on the receiving side\n"),
		ExpectStatus: http.StatusConflict,
	}.Check(t, s.Handler)
}
