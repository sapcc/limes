// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

const day = 24 * time.Hour

const testCommitmentsYAML = `
	availability_zones: [ az-one, az-two ]
	discovery:
		method: static
		static_config:
			domains:
				- { name: germany, id: uuid-for-germany }
				- { name: france,id: uuid-for-france }
			projects:
				uuid-for-germany:
					- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
					- { name: dresden, id: uuid-for-dresden, parent_id: uuid-for-berlin }
				uuid-for-france:
					- { name: paris, id: uuid-for-paris, parent_id: uuid-for-france}
	liquids:
		first:
			area: first
			liquid_service_type: %[1]s
			commitment_behavior_per_resource:
				- key: '.*'
					value:
						durations_per_domain: [{ key: '.*', value: ["1 hour", "2 hours"] }]
						min_confirm_date: '1970-01-08T00:00:00Z' # one week after start of mock.Clock
		second:
			area: second
			liquid_service_type: %[2]s
			commitment_behavior_per_resource: []
`
const testCommitmentsYAMLWithoutMinConfirmDate = `
	availability_zones: [ az-one, az-two ]
	discovery:
		method: static
		static_config:
			domains:
				- { name: germany, id: uuid-for-germany }
				- { name: france,id: uuid-for-france }
			projects:
				uuid-for-germany:
					- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
					- { name: dresden, id: uuid-for-dresden, parent_id: uuid-for-berlin }
				uuid-for-france:
					- { name: paris, id: uuid-for-paris, parent_id: uuid-for-france}
	liquids:
		first:
			area: first
			liquid_service_type: %[1]s
			commitment_behavior_per_resource: []
		second:
			area: second
			liquid_service_type: %[2]s
			commitment_behavior_per_resource:
				- key: '.*'
					value:
						durations_per_domain: [{ key: '.*', value: ["1 hour", "2 hours", "3 hours"] }]
`

const testConvertCommitmentsYAML = `
	availability_zones: [ az-one, az-two ]
	discovery:
		method: static
		static_config:
			domains:
				- { name: germany, id: uuid-for-germany }
				- { name: france,id: uuid-for-france }
			projects:
				uuid-for-germany:
					- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
					- { name: dresden, id: uuid-for-dresden, parent_id: uuid-for-berlin }
				uuid-for-france:
					- { name: paris, id: uuid-for-paris, parent_id: uuid-for-france}
	liquids:
		first:
			area: first
			liquid_service_type: %[1]s
			commitment_behavior_per_resource:
				- key: capacity
					value:
						durations_per_domain: &durations [{ key: '.*', value: ["1 hour", "2 hours"] }]
						conversion_rule: { identifier: flavor1, weight: 48 }
				- key: '.*'
					value: { durations_per_domain: *durations }
		second:
			area: second
			liquid_service_type: %[2]s
			commitment_behavior_per_resource:
				- key: capacity
					value:
						durations_per_domain: *durations
						conversion_rule: { identifier: flavor1, weight: 32 }
				- key: '.*'
					value: { durations_per_domain: *durations }
		third:
			area: third
			liquid_service_type: %[3]s
			commitment_behavior_per_resource:
				- key: capacity_c32
					value:
						durations_per_domain: *durations
						conversion_rule: { identifier: flavor1, weight: 32 }
				- key: capacity_c48
					value:
						durations_per_domain: *durations
						conversion_rule: { identifier: flavor1, weight: 48 }
				- key: capacity_c96
					value:
						durations_per_domain: *durations
						conversion_rule: { identifier: flavor1, weight: 96 }
				- key: capacity_c120
					value:
						durations_per_domain: *durations
						conversion_rule: { identifier: flavor1, weight: 120 }
				- key: capacity2_c144
					value:
						durations_per_domain: *durations
						conversion_rule: { identifier: flavor2, weight: 144 }
				- key: '.*'
					value: { durations_per_domain: *durations }
`

func TestCommitmentLifecycleWithDelayedConfirmation(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testCommitmentsYAML, liquidServiceTypeFirst, liquidServiceTypeSecond)),
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
		"notify_on_confirm": true,
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
		"notify_on_confirm": true,
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
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testCommitmentsYAML, liquidServiceTypeFirst, liquidServiceTypeSecond)),
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

	// try to create a commitment with a mail notification flag (only possible to set for planned commitments)
	notificationReq := assert.JSONObject{
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"amount":            1,
		"duration":          "1 hour",
		"notify_on_confirm": true,
	}

	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": notificationReq},
		ExpectStatus: http.StatusConflict,
	}.Check(t, s.Handler)
}

func TestGetCommitmentsErrorCases(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testCommitmentsYAML, liquidServiceTypeFirst, liquidServiceTypeSecond)),
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
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testCommitmentsYAML, liquidServiceTypeFirst, liquidServiceTypeSecond)),
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
		ExpectBody:   assert.StringData("no such service and/or resource: unknown/capacity\n"),
	}.Check(t, s.Handler)

	// invalid request field: resource_name does not exist
	cloned = maps.Clone(request)
	cloned["resource_name"] = "unknown"
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": cloned},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("no such service and/or resource: first/unknown\n"),
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

	// invalid request field: service_type/resource_name accepts commitments, but is forbidden in this project
	_, err := s.DB.Exec(`UPDATE project_resources SET forbidden = TRUE`)
	if err != nil {
		t.Fatal(err)
	}
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": request},
		ExpectStatus: http.StatusUnprocessableEntity,
		ExpectBody:   assert.StringData("resource first/capacity is not enabled in this project\n"),
	}.Check(t, s.Handler)
	_, err = s.DB.Exec(`UPDATE project_resources SET forbidden = FALSE`)
	if err != nil {
		t.Fatal(err)
	}

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
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testCommitmentsYAML, liquidServiceTypeFirst, liquidServiceTypeSecond)),
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
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testCommitmentsYAMLWithoutMinConfirmDate, liquidServiceTypeFirst, liquidServiceTypeSecond)),
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

	assert.HTTPRequest{
		Method:       http.MethodDelete,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1",
		ExpectStatus: http.StatusNoContent,
	}.Check(t, s.Handler)

	// TransferAmount < CommitmentAmount
	resp2 := assert.JSONObject{
		"id":                3,
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
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req1},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	assert.HTTPRequest{
		Method:       "POST",
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/2/start-transfer",
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
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/2/start-transfer",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("delivered amount exceeds the commitment amount.\n"),
		Body:         assert.JSONObject{"commitment": assert.JSONObject{"amount": 11, "transfer_status": "public"}},
	}.Check(t, s.Handler)
}

func Test_GetCommitmentByToken(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testCommitmentsYAMLWithoutMinConfirmDate, liquidServiceTypeFirst, liquidServiceTypeSecond)),
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
}

func Test_TransferCommitment(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testCommitmentsYAMLWithoutMinConfirmDate, liquidServiceTypeFirst, liquidServiceTypeSecond)),
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
	assert.DeepEqual(t, "commitment state", supersededCommitment.State, db.CommitmentStateSuperseded)

	var splitCommitment db.ProjectCommitment
	err = s.DB.SelectOne(&splitCommitment, `SELECT * FROM project_commitments where ID = 2`)
	if err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, "commitment state", splitCommitment.State, db.CommitmentStateActive)

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
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testCommitmentsYAMLWithoutMinConfirmDate, liquidServiceTypeFirst, liquidServiceTypeSecond)),
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

func Test_GetCommitmentConversion(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	srvInfoThird := test.DefaultLiquidServiceInfo()
	srvInfoThird.Resources = map[liquid.ResourceName]liquid.ResourceInfo{
		"capacity_c32":   {Unit: limes.UnitBytes, HasQuota: true, Topology: liquid.FlatTopology},
		"capacity_c48":   {Unit: limes.UnitBytes, HasQuota: true, Topology: liquid.FlatTopology},
		"capacity_c96":   {Unit: limes.UnitBytes, HasQuota: true, Topology: liquid.FlatTopology},
		"capacity_c120":  {Unit: limes.UnitNone, HasQuota: true, Topology: liquid.FlatTopology},
		"capacity2_c144": {Unit: limes.UnitNone, HasQuota: true, Topology: liquid.FlatTopology},
	}
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	_, liquidServiceTypeThird := test.NewMockLiquidClient(srvInfoThird)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testConvertCommitmentsYAML, liquidServiceTypeFirst, liquidServiceTypeSecond, liquidServiceTypeThird)),
		test.WithAPIHandler(NewV1API),
	)

	// capacity_c120 uses a different Unit than the source and is therefore ignored.
	resp1 := []assert.JSONObject{
		{
			"from":            1,
			"to":              1,
			"target_service":  "first",
			"target_resource": "capacity",
		},
		{
			"from":            2,
			"to":              3,
			"target_service":  "second",
			"target_resource": "capacity",
		},
		{
			"from":            2,
			"to":              3,
			"target_service":  "third",
			"target_resource": "capacity_c32",
		}, {
			"from":            2,
			"to":              1,
			"target_service":  "third",
			"target_resource": "capacity_c96",
		}}

	resp2 := []assert.JSONObject{}

	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/commitment-conversion/third/capacity_c48",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"conversions": resp1},
	}.Check(t, s.Handler)

	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/commitment-conversion/third/capacity2_c144",
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.JSONObject{"conversions": resp2},
	}.Check(t, s.Handler)
}

func Test_ConvertCommitments(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	srvInfoThird := test.DefaultLiquidServiceInfo()
	srvInfoThird.Resources = map[liquid.ResourceName]liquid.ResourceInfo{
		"capacity_c32":   {Unit: limes.UnitBytes, HasQuota: true, Topology: liquid.FlatTopology},
		"capacity_c48":   {Unit: limes.UnitBytes, HasQuota: true, Topology: liquid.FlatTopology},
		"capacity_c96":   {Unit: limes.UnitBytes, HasQuota: true, Topology: liquid.FlatTopology},
		"capacity_c120":  {Unit: limes.UnitNone, HasQuota: true, Topology: liquid.FlatTopology},
		"capacity2_c144": {Unit: limes.UnitNone, HasQuota: true, Topology: liquid.FlatTopology},
	}
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	_, liquidServiceTypeThird := test.NewMockLiquidClient(srvInfoThird)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testConvertCommitmentsYAML, liquidServiceTypeFirst, liquidServiceTypeSecond, liquidServiceTypeThird)),
		test.WithAPIHandler(NewV1API),
	)

	req := func(targetService, targetResource string, sourceAmount, TargetAmount uint64) assert.JSONObject {
		return assert.JSONObject{
			"commitment": assert.JSONObject{
				"target_service":  targetService,
				"target_resource": targetResource,
				"source_amount":   sourceAmount,
				"target_amount":   TargetAmount,
			},
		}
	}

	resp := func(id, amount uint64, targetService, targetResource string) assert.JSONObject {
		return assert.JSONObject{
			"id":                id,
			"service_type":      targetService,
			"resource_name":     targetResource,
			"availability_zone": "az-one",
			"amount":            amount,
			"unit":              "B",
			"duration":          "1 hour",
			"created_at":        s.Clock.Now().Unix(),
			"creator_uuid":      "uuid-for-alice",
			"creator_name":      "alice@Default",
			"can_be_deleted":    true,
			"confirmed_at":      s.Clock.Now().Unix(),
			"expires_at":        s.Clock.Now().Add(1 * time.Hour).Unix(),
		}
	}
	respWithConfirmBy := func(id, amount uint64, targetService, targetResource string) assert.JSONObject {
		return assert.JSONObject{
			"id":                id,
			"service_type":      targetService,
			"resource_name":     targetResource,
			"availability_zone": "az-one",
			"amount":            amount,
			"unit":              "B",
			"duration":          "1 hour",
			"created_at":        s.Clock.Now().Unix(),
			"creator_uuid":      "uuid-for-alice",
			"creator_name":      "alice@Default",
			"can_be_deleted":    true,
			"confirm_by":        s.Clock.Now().Add(14 * day).Unix(),
			"expires_at":        s.Clock.Now().Add(14*day + 1*time.Hour).Unix(),
		}
	}

	// conversion rate is (second: 3 to first: 2)
	assert.HTTPRequest{
		Method: http.MethodPost,
		Path:   "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body: assert.JSONObject{
			"commitment": assert.JSONObject{
				"service_type":      "second",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"amount":            21,
				"duration":          "1 hour",
			},
		},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	// Converted commitment does not fit into the capacity (amount: 12, capacity: 10)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/convert",
		Body:         req("first", "capacity", 21, 14),
		ExpectBody:   assert.StringData("not enough capacity to confirm the commitment\n"),
		ExpectStatus: http.StatusUnprocessableEntity,
	}.Check(t, s.Handler)

	// Conversion with remainder should be rejected.
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/convert",
		Body:         req("first", "capacity", 10, 6),
		ExpectBody:   assert.StringData("amount: 10 does not fit into conversion rate of: 3\n"),
		ExpectStatus: http.StatusConflict,
	}.Check(t, s.Handler)

	var originalCommitment db.ProjectCommitment

	// Conversion without remainder
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/convert",
		Body:         req("first", "capacity", 3, 2),
		ExpectBody:   assert.JSONObject{"commitment": resp(3, 2, "first", "capacity")},
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)
	err := s.DB.SelectOne(&originalCommitment, `SELECT * FROM project_commitments where ID = 1`)
	if err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, "commitment state", originalCommitment.State, db.CommitmentStateSuperseded)
	err = s.DB.SelectOne(&originalCommitment, `SELECT * FROM project_commitments where ID = 2`)
	if err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, "commitment amount", originalCommitment.Amount, 18)

	// Reject conversion attempt to a different project.
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-dresden/commitments/2/convert",
		Body:         req("first", "capacity", 6, 4),
		ExpectBody:   assert.StringData("no such commitment\n"),
		ExpectStatus: http.StatusNotFound,
	}.Check(t, s.Handler)

	// Reject conversion at the same project to the same resource.
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/2/convert",
		Body:         req("second", "capacity", 6, 6),
		ExpectBody:   assert.StringData("conversion attempt to the same resource.\n"),
		ExpectStatus: http.StatusConflict,
	}.Check(t, s.Handler)

	// Mismatching target amount
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/2/convert",
		Body:         req("first", "capacity", 6, 3),
		ExpectBody:   assert.StringData("conversion mismatch. provided: 3, calculated: 4\n"),
		ExpectStatus: http.StatusConflict,
	}.Check(t, s.Handler)

	// Reject an amount that doesn't fit into the conversion rate (remainder > 0).
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/2/convert",
		Body:         req("first", "capacity", 1, 3),
		ExpectBody:   assert.StringData("amount: 1 does not fit into conversion rate of: 3\n"),
		ExpectStatus: http.StatusConflict,
	}.Check(t, s.Handler)

	// Check conversion to a different identifier which should be rejected
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/2/convert",
		Body:         req("third", "capacity2_c144", 1, 3),
		ExpectBody:   assert.StringData("commitment is not convertible into resource third/capacity2_c144\n"),
		ExpectStatus: http.StatusUnprocessableEntity,
	}.Check(t, s.Handler)

	// test commitment conversion with confirmBy field (unconfirmed commitment)
	assert.HTTPRequest{
		Method: http.MethodPost,
		Path:   "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body: assert.JSONObject{
			"commitment": assert.JSONObject{
				"service_type":      "second",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"amount":            3,
				"duration":          "1 hour",
				"confirm_by":        s.Clock.Now().Add(14 * day).Unix(),
			},
		},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/4/convert",
		Body:         req("first", "capacity", 3, 2),
		ExpectBody:   assert.JSONObject{"commitment": respWithConfirmBy(5, 2, "first", "capacity")},
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)
}

func Test_UpdateCommitmentDuration(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testCommitmentsYAMLWithoutMinConfirmDate, liquidServiceTypeFirst, liquidServiceTypeSecond)),
		test.WithAPIHandler(NewV1API),
	)

	// Positive: confirmed commitment
	assert.HTTPRequest{
		Method: http.MethodPost,
		Path:   "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body: assert.JSONObject{
			"commitment": assert.JSONObject{
				"service_type":      "second",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"amount":            10,
				"duration":          "2 hours",
			},
		},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	// Fast forward by 1 hour. Creation_time = 0; Now = 1; (Expire = Creation_time + 2 hours)
	s.Clock.StepBy(1 * time.Hour)

	assert.HTTPRequest{
		Method: http.MethodPost,
		Path:   "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/update-duration",
		Body:   assert.JSONObject{"duration": "3 hours"},
		ExpectBody: assert.JSONObject{"commitment": assert.JSONObject{
			"id":                1,
			"service_type":      "second",
			"resource_name":     "capacity",
			"availability_zone": "az-one",
			"amount":            10,
			"unit":              "B",
			"duration":          "3 hours",
			"created_at":        s.Clock.Now().Add(-1 * time.Hour).Unix(),
			"creator_uuid":      "uuid-for-alice",
			"creator_name":      "alice@Default",
			"can_be_deleted":    true,
			"confirmed_at":      s.Clock.Now().Add(-1 * time.Hour).Unix(),
			"expires_at":        s.Clock.Now().Add(2 * time.Hour).Unix(),
		}},
		ExpectStatus: http.StatusOK,
	}.Check(t, s.Handler)

	// Positive: Pending commitment
	assert.HTTPRequest{
		Method: http.MethodPost,
		Path:   "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body: assert.JSONObject{
			"commitment": assert.JSONObject{
				"service_type":      "second",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"amount":            10,
				"confirm_by":        s.Clock.Now().Add(1 * day).Unix(),
				"duration":          "1 hours",
			},
		},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	assert.HTTPRequest{
		Method: http.MethodPost,
		Path:   "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/2/update-duration",
		Body:   assert.JSONObject{"duration": "3 hours"},
		ExpectBody: assert.JSONObject{"commitment": assert.JSONObject{
			"id":                2,
			"service_type":      "second",
			"resource_name":     "capacity",
			"availability_zone": "az-one",
			"amount":            10,
			"unit":              "B",
			"duration":          "3 hours",
			"created_at":        s.Clock.Now().Unix(),
			"creator_uuid":      "uuid-for-alice",
			"creator_name":      "alice@Default",
			"can_be_deleted":    true,
			"confirm_by":        s.Clock.Now().Add(1 * day).Unix(),
			"expires_at":        s.Clock.Now().Add(3*time.Hour + 1*day).Unix(),
		}},
		ExpectStatus: http.StatusOK,
	}.Check(t, s.Handler)

	// Negative: Provided duration is invalid
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/update-duration",
		Body:         assert.JSONObject{"duration": "99 hours"},
		ExpectBody:   assert.StringData("provided duration: 99 hours does not match the config [1 hour 2 hours 3 hours]\n"),
		ExpectStatus: http.StatusUnprocessableEntity,
	}.Check(t, s.Handler)

	// Negative: Provided duration < Commitment Duration
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/update-duration",
		Body:         assert.JSONObject{"duration": "1 hour"},
		ExpectBody:   assert.StringData("duration change from 3 hours to 1 hour forbidden\n"),
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)

	// Negative: Expired commitment.
	s.Clock.StepBy(-1 * time.Hour)
	assert.HTTPRequest{
		Method: http.MethodPost,
		Path:   "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body: assert.JSONObject{
			"commitment": assert.JSONObject{
				"service_type":      "second",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"amount":            10,
				"duration":          "1 hours",
			},
		},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	s.Clock.StepBy(1 * time.Hour)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/3/update-duration",
		Body:         assert.JSONObject{"duration": "2 hours"},
		ExpectBody:   assert.StringData("unable to process expired commitment\n"),
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)

	// Negative: Superseded commitment
	s.Clock.StepBy(-1 * time.Hour)
	_, err := s.DB.Exec("UPDATE project_commitments SET state='superseded' where ID = 3")
	if err != nil {
		t.Fatal(err)
	}
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/3/update-duration",
		Body:         assert.JSONObject{"duration": "2 hours"},
		ExpectBody:   assert.StringData("unable to operate on commitment with a state of superseded\n"),
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
}

func Test_MergeCommitments(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testCommitmentsYAMLWithoutMinConfirmDate, liquidServiceTypeFirst, liquidServiceTypeSecond)),
		test.WithAPIHandler(NewV1API),
	)

	srvInfoSecond.Resources["other"] = liquid.ResourceInfo{
		Unit:     limes.UnitBytes,
		Topology: liquid.AZAwareTopology,
	}

	// Create two confirmed commitments on the same resource
	req1 := assert.JSONObject{
		"id":                1,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"amount":            10,
		"duration":          "1 hour",
	}
	req2 := assert.JSONObject{
		"id":                2,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"amount":            5,
		"duration":          "2 hours",
	}
	// Create confirmed commitment in different AZ
	req3 := assert.JSONObject{
		"id":                3,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            1,
		"duration":          "2 hours",
	}
	// Create confirmed commitment on different resource
	req4 := assert.JSONObject{
		"id":                4,
		"service_type":      "second",
		"resource_name":     "other",
		"availability_zone": "az-one",
		"amount":            1,
		"duration":          "2 hours",
	}
	resp3 := assert.JSONObject{
		"id":                3,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            1,
		"unit":              "B",
		"duration":          "2 hours",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      0,
		"expires_at":        s.Clock.Now().Add(2 * time.Hour).Unix(),
	}
	resp4 := assert.JSONObject{
		"id":                4,
		"service_type":      "second",
		"resource_name":     "other",
		"availability_zone": "az-one",
		"amount":            1,
		"unit":              "B",
		"duration":          "2 hours",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      0,
		"expires_at":        s.Clock.Now().Add(2 * time.Hour).Unix(),
	}
	// Merged commitment
	resp5 := assert.JSONObject{
		"id":                5,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"amount":            15,
		"unit":              "B",
		"duration":          "2 hours",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      0,
		"expires_at":        s.Clock.Now().Add(2 * time.Hour).Unix(),
	}
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req1},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req2},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req3},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req4},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	// No authentication
	// Missing edit permissions
	s.TokenValidator.Enforcer.AllowEdit = false
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/merge",
		Body:         assert.JSONObject{"commitment_ids": []int{1, 2}},
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowEdit = true

	// Unknown domain, project and commitment
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/unknown/projects/uuid-for-berlin/commitments/merge",
		Body:         assert.JSONObject{"commitment_ids": []int{1, 2}},
		ExpectStatus: http.StatusNotFound,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/unknown/commitments/merge",
		Body:         assert.JSONObject{"commitment_ids": []int{1, 2}},
		ExpectStatus: http.StatusNotFound,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/merge",
		Body:         assert.JSONObject{"commitment_ids": []int{1, 2000}},
		ExpectStatus: http.StatusNotFound,
	}.Check(t, s.Handler)

	// Check that there are at least 2 commits to merge
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/merge",
		Body:         assert.JSONObject{"commitment_ids": []int{1}},
		ExpectStatus: http.StatusBadRequest,
	}.Check(t, s.Handler)

	// Do not merge commitments in different AZs
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/merge",
		Body:         assert.JSONObject{"commitment_ids": []int{1, 3}},
		ExpectStatus: http.StatusConflict,
	}.Check(t, s.Handler)
	// Do not merge commitments on different resources
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/merge",
		Body:         assert.JSONObject{"commitment_ids": []int{1, 4}},
		ExpectStatus: http.StatusConflict,
	}.Check(t, s.Handler)

	// Do not merge commitments with states other than "active"
	unmergableStates := []db.CommitmentState{db.CommitmentStatePlanned, db.CommitmentStatePending, db.CommitmentStateSuperseded, db.CommitmentStateExpired}
	for _, state := range unmergableStates {
		_, err := s.DB.Exec("UPDATE project_commitments SET state=$1 where ID = 2", state)
		if err != nil {
			t.Fatal(err)
		}
		assert.HTTPRequest{
			Method:       http.MethodPost,
			Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/merge",
			Body:         assert.JSONObject{"commitment_ids": []int{1, 2}},
			ExpectStatus: http.StatusConflict,
		}.Check(t, s.Handler)
	}
	_, err := s.DB.Exec("UPDATE project_commitments SET state=$1 where ID = 2", db.CommitmentStateActive)
	if err != nil {
		t.Fatal(err)
	}

	// Happy path
	// New merged commitment should be returned with latest expiration date of all commitments
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/merge",
		Body:         assert.JSONObject{"commitment_ids": []int{1, 2}},
		ExpectBody:   assert.JSONObject{"commitment": resp5},
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)
	// Check that commitments not involved in the merge remained the same
	// Check that new merged commitment is present and that superseded commitments are not reported anymore
	assert.HTTPRequest{
		Method:       http.MethodGet,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments",
		ExpectBody:   assert.JSONObject{"commitments": []assert.JSONObject{resp3, resp4, resp5}},
		ExpectStatus: http.StatusOK,
	}.Check(t, s.Handler)
	// Validate that commitments that were merged are now superseded and have the correct context
	var supersededCommitment db.ProjectCommitment
	err = s.DB.SelectOne(&supersededCommitment, `SELECT * FROM project_commitments where ID = 1`)
	if err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, "commitment state", supersededCommitment.State, db.CommitmentStateSuperseded)
	expectedContext := db.CommitmentWorkflowContext{Reason: db.CommitmentReasonMerge, RelatedCommitmentIDs: []db.ProjectCommitmentID{5}}
	var supersedeContext db.CommitmentWorkflowContext
	err = json.Unmarshal(supersededCommitment.SupersedeContextJSON.UnwrapOr(nil), &supersedeContext)
	if err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, "commitment supersede context", supersedeContext, expectedContext)
	err = s.DB.SelectOne(&supersededCommitment, `SELECT * FROM project_commitments where ID = 2`)
	if err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, "commitment state", supersededCommitment.State, db.CommitmentStateSuperseded)
	err = json.Unmarshal(supersededCommitment.SupersedeContextJSON.UnwrapOr(nil), &supersedeContext)
	if err != nil {
		t.Fatal(err)
	}
	assert.DeepEqual(t, "commitment supersede context", supersedeContext, expectedContext)
}

func Test_RenewCommitments(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo()
	srvInfoSecond := test.DefaultLiquidServiceInfo()
	_, liquidServiceTypeFirst := test.NewMockLiquidClient(srvInfoFirst)
	_, liquidServiceTypeSecond := test.NewMockLiquidClient(srvInfoSecond)
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-commitments.sql"),
		test.WithConfig(fmt.Sprintf(testCommitmentsYAMLWithoutMinConfirmDate, liquidServiceTypeFirst, liquidServiceTypeSecond)),
		test.WithAPIHandler(NewV1API),
	)

	req1 := assert.JSONObject{
		"id":                1,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"amount":            2,
		"duration":          "1 hour",
	}
	req2 := assert.JSONObject{
		"id":                2,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            1,
		"duration":          "2 hours",
	}

	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req1},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req2},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	resp1 := assert.JSONObject{
		"id":                3,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"amount":            2,
		"unit":              "B",
		"duration":          "1 hour",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirm_by":        s.Clock.Now().Add(1 * time.Hour).Unix(),
		"expires_at":        s.Clock.Now().Add(2 * time.Hour).Unix(),
	}
	resp2 := assert.JSONObject{
		"id":                4,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"amount":            1,
		"unit":              "B",
		"duration":          "2 hours",
		"created_at":        s.Clock.Now().Unix(),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirm_by":        s.Clock.Now().Add(2 * time.Hour).Unix(),
		"expires_at":        s.Clock.Now().Add(4 * time.Hour).Unix(),
	}

	// Renew applicable commitments successfully
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/renew",
		ExpectBody:   assert.JSONObject{"commitment": resp1},
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/2/renew",
		ExpectBody:   assert.JSONObject{"commitment": resp2},
		ExpectStatus: http.StatusAccepted,
	}.Check(t, s.Handler)

	// Ensure that already renewed commitments can't be renewed again
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/1/renew",
		ExpectStatus: http.StatusConflict,
	}.Check(t, s.Handler)

	// Do not allow to renew already expired commitments (that are not tagged as ones yet)
	req3 := assert.JSONObject{
		"id":                5,
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"amount":            1,
		"duration":          "1 hour",
	}
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/new",
		Body:         assert.JSONObject{"commitment": req3},
		ExpectStatus: http.StatusCreated,
	}.Check(t, s.Handler)

	s.Clock.StepBy(2 * time.Hour)

	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/5/renew",
		ExpectStatus: http.StatusConflict,
	}.Check(t, s.Handler)

	s.Clock.StepBy(-2 * time.Hour)
	// Do not allow to renew explicit expired commitments
	_, err := s.DB.Exec("UPDATE project_commitments SET state = $1 WHERE id = 5", db.CommitmentStateExpired)
	if err != nil {
		t.Fatal(err)
	}
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/5/renew",
		ExpectStatus: http.StatusConflict,
	}.Check(t, s.Handler)

	// Reject requests that try to renew commitments too early (more than 3 month before expiring date)
	_, err = s.DB.Exec("UPDATE project_commitments SET duration = $1, expires_at = $2, state = $3 WHERE id = 5",
		"4 months", s.Clock.Now().Add(4*30*24*time.Hour), db.CommitmentStateActive,
	)
	if err != nil {
		t.Fatal(err)
	}
	assert.HTTPRequest{
		Method:       http.MethodPost,
		Path:         "/v1/domains/uuid-for-germany/projects/uuid-for-berlin/commitments/5/renew",
		ExpectStatus: http.StatusConflict,
	}.Check(t, s.Handler)
}
