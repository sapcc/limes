// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"
	"go.xyrillian.de/gg/jsonmatch"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/test"
)

func commonMergeCommitmentsSetup(t *testing.T, manager string) (s test.Setup, tr *easypg.Tracker, uuid1, uuid2, uuid3, uuid4 liquid.CommitmentUUID, initialCreatedAt, initialExpiresAt time.Time, expectedJSON map[string]any) {
	t.Helper()

	s, tr, uuid1, initialCreatedAt, initialExpiresAt, expectedJSON = commonExistingCommitmentSetup(t, manager)

	// create a second commitment (amount 5, same duration)
	req := map[string]any{
		"amount":            5,
		"duration":          "1 hour",
		"project_id":        "uuid-for-paris",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
	}
	expectedJSONNew := jsonmatch.Object(maps.Clone(expectedJSON))
	expectedJSONNew["uuid"] = jsonmatch.CaptureField(&uuid2)
	expectedJSONNew["amount"] = 5
	s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(req)).ExpectJSON(t, http.StatusCreated, expectedJSONNew)

	// create a third commitment (amount 6, same duration)
	req["amount"] = 6
	expectedJSONNew["uuid"] = jsonmatch.CaptureField(&uuid3)
	expectedJSONNew["amount"] = 6
	s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(req)).ExpectJSON(t, http.StatusCreated, expectedJSONNew)

	// create a fourth commitment (amount 3, 2 hour duration for different expiresAt)
	req["amount"] = 3
	req["duration"] = "2 hours"
	expectedJSONNew["uuid"] = jsonmatch.CaptureField(&uuid4)
	expectedJSONNew["amount"] = 3
	expectedJSONNew["duration"] = "2 hours"
	expectedJSONNew["expires_at"] = initialExpiresAt.Add(1 * time.Hour).Format(time.RFC3339)
	s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(req)).ExpectJSON(t, http.StatusCreated, expectedJSONNew)

	s.Auditor.IgnoreEventsUntilNow()
	tr.DBChanges().Ignore()
	return
}

// Helper function for a successful commitment merge.
func mergeCommitmentsAndExpectSuccess(t *testing.T, s test.Setup, liquidHandlesCommitments bool, request map[string]any, expected jsonmatch.Object, getAuditTarget func() cadf.Resource) {
	t.Helper()
	ctx := t.Context()
	mockLiquid := s.LiquidClients["first"]

	if liquidHandlesCommitments {
		mockLiquid.LastCommitmentChangeRequest = liquid.CommitmentChangeRequest{}
	}

	path := "/resources/v2/commitments/merge"
	s.Handler.RespondTo(ctx, "POST "+path, httptest.WithJSONBody(request)).
		ExpectJSON(t, http.StatusCreated, expected)
	target := getAuditTarget()
	s.Auditor.ExpectEvents(t, cadf.Event{
		Action:      "create",
		Outcome:     "success",
		Reason:      cadf.Reason{ReasonType: "HTTP", ReasonCode: "201"},
		RequestPath: path,
		Target:      target,
	})

	if liquidHandlesCommitments {
		actualCCR := must.Return(json.Marshal(mockLiquid.LastCommitmentChangeRequest))
		var expectedCCR jsonmatch.Object
		must.SucceedT(t, json.Unmarshal([]byte(target.Attachments[0].Content.(string)), &expectedCCR))
		for _, diff := range expectedCCR.DiffAgainst(actualCCR) {
			t.Error("in MockLiquid.LastCommitmentChangeRequest: " + diff.String())
		}
	} else {
		assert.Equal(t, mockLiquid.LastCommitmentChangeRequest, liquid.CommitmentChangeRequest{})
	}
}

func mergeCommitmentsAndExpectError(t *testing.T, s test.Setup, tr *easypg.Tracker, request map[string]any, expect func(r httptest.Response)) {
	t.Helper()
	ctx := t.Context()

	s.Handler.RespondTo(ctx, "POST /resources/v2/commitments/merge", httptest.WithJSONBody(request)).Expect(expect)
	tr.DBChanges().AssertEmpty()
	s.Auditor.ExpectEvents(t)
}

func TestCommitmentMergeHappyPaths(t *testing.T) {
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			s, tr, uuid1, uuid2, uuid3, uuid4, initialCreatedAt, initialExpiresAt, expectedJSON := commonMergeCommitmentsSetup(t, manager)
			expiresAt3 := initialCreatedAt.Add(2 * time.Hour)

			// merge uuid1 (amount=10, 1h) and uuid2 (amount=5, 1h) -> merged amount=15, 1h, expiresAt from uuid1/uuid2 (same)
			s.Clock.StepBy(1 * time.Minute)
			mergeTime := s.Clock.Now()
			var uuidMerged1 liquid.CommitmentUUID
			expectedJSON["uuid"] = jsonmatch.CaptureField(&uuidMerged1)
			expectedJSON["amount"] = 15
			expectedJSON["created_at"] = mergeTime.Format(time.RFC3339)
			expectedJSON["confirmed_at"] = mergeTime.Format(time.RFC3339)
			expectedJSON["updated_at"] = mergeTime.Format(time.RFC3339)

			auditTargetFunc := func() cadf.Resource {
				return cadf.Resource{
					TypeURI:     "service/resources/commitment",
					ID:          string(uuid1),
					DomainID:    "uuid-for-france",
					DomainName:  "france",
					ProjectID:   "uuid-for-paris",
					ProjectName: "paris",
					Attachments: []cadf.Attachment{must.Return(cadf.NewJSONAttachment("payload", liquid.CommitmentChangeRequest{
						AZ:          "az-one",
						InfoVersion: 1,
						ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
							"uuid-for-paris": {
								ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
									"capacity": {
										TotalConfirmedBefore: 24, TotalConfirmedAfter: 24, TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0, Commitments: []liquid.Commitment{{
											UUID:      uuid1,
											OldStatus: Some(liquid.CommitmentStatusConfirmed),
											NewStatus: Some(liquid.CommitmentStatusSuperseded),
											Amount:    10,
											ExpiresAt: initialExpiresAt,
										}, {
											UUID:      uuid2,
											OldStatus: Some(liquid.CommitmentStatusConfirmed),
											NewStatus: Some(liquid.CommitmentStatusSuperseded),
											Amount:    5,
											ExpiresAt: initialExpiresAt,
										}, {
											UUID:      uuidMerged1,
											NewStatus: Some(liquid.CommitmentStatusConfirmed),
											Amount:    15,
											ExpiresAt: initialExpiresAt,
										}},
									},
								},
							},
						},
					}))},
				}
			}
			mergeCommitmentsAndExpectSuccess(t, s, manager == "liquid",
				map[string]any{"commitment_uuids": []string{string(uuid1), string(uuid2)}},
				expectedJSON, auditTargetFunc)
			tr.DBChanges().AssertEqualf(`
				UPDATE project_commitments SET status = 'superseded', superseded_at = %[4]d, supersede_context_json = '{"reason": "merge", "related_ids": [5], "related_uuids": ["%[3]s"]}', updated_at = %[4]d WHERE id = 1 AND uuid = '%[1]s' AND transfer_token = NULL;
				UPDATE project_commitments SET status = 'superseded', superseded_at = %[4]d, supersede_context_json = '{"reason": "merge", "related_ids": [5], "related_uuids": ["%[3]s"]}', updated_at = %[4]d WHERE id = 2 AND uuid = '%[2]s' AND transfer_token = NULL;
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (5, '%[3]s', 3, 2, 'confirmed', 15, '1 hour', %[4]d, 'uuid-for-alice', 'alice@Default', %[4]d, %[5]d, '{"reason": "merge", "related_ids": [1, 2], "related_uuids": ["%[1]s", "%[2]s"]}', %[4]d);`,
				uuid1, uuid2, uuidMerged1, mergeTime.Unix(), initialExpiresAt.Unix())

			// merge uuidMerged1 (amount=15, 1h) and uuid3 (amount=6, 1h) and uuid4 (amount=3, 2h) -> merged amount=24, 2h (latest expiresAt from uuid3)
			s.Clock.StepBy(1 * time.Minute)
			mergeTime2 := s.Clock.Now()
			var uuidMerged2 liquid.CommitmentUUID
			expectedJSON["uuid"] = jsonmatch.CaptureField(&uuidMerged2)
			expectedJSON["amount"] = 24
			expectedJSON["duration"] = "2 hours"
			expectedJSON["created_at"] = mergeTime2.Format(time.RFC3339)
			expectedJSON["confirmed_at"] = mergeTime2.Format(time.RFC3339)
			expectedJSON["updated_at"] = mergeTime2.Format(time.RFC3339)
			expectedJSON["expires_at"] = initialExpiresAt.Add(1 * time.Hour)

			auditTargetFunc = func() cadf.Resource {
				return cadf.Resource{
					TypeURI:     "service/resources/commitment",
					ID:          string(uuidMerged1),
					DomainID:    "uuid-for-france",
					DomainName:  "france",
					ProjectID:   "uuid-for-paris",
					ProjectName: "paris",
					Attachments: []cadf.Attachment{must.Return(cadf.NewJSONAttachment("payload", liquid.CommitmentChangeRequest{
						AZ:          "az-one",
						InfoVersion: 1,
						ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
							"uuid-for-paris": {
								ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
									"capacity": {
										TotalConfirmedBefore: 24, TotalConfirmedAfter: 24, TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0, Commitments: []liquid.Commitment{{
											UUID:      uuidMerged1,
											OldStatus: Some(liquid.CommitmentStatusConfirmed),
											NewStatus: Some(liquid.CommitmentStatusSuperseded),
											Amount:    15,
											ExpiresAt: initialExpiresAt,
										}, {
											UUID:      uuid3,
											OldStatus: Some(liquid.CommitmentStatusConfirmed),
											NewStatus: Some(liquid.CommitmentStatusSuperseded),
											Amount:    6,
											ExpiresAt: initialExpiresAt,
										}, {
											UUID:      uuid4,
											OldStatus: Some(liquid.CommitmentStatusConfirmed),
											NewStatus: Some(liquid.CommitmentStatusSuperseded),
											Amount:    3,
											ExpiresAt: expiresAt3,
										}, {
											UUID:      uuidMerged2,
											NewStatus: Some(liquid.CommitmentStatusConfirmed),
											Amount:    24,
											ExpiresAt: expiresAt3,
										}},
									},
								},
							},
						},
					}))},
				}
			}
			mergeCommitmentsAndExpectSuccess(t, s, manager == "liquid",
				map[string]any{"commitment_uuids": []string{string(uuidMerged1), string(uuid3), string(uuid4)}},
				expectedJSON, auditTargetFunc)
			tr.DBChanges().AssertEqualf(`
				UPDATE project_commitments SET status = 'superseded', superseded_at = %[5]d, supersede_context_json = '{"reason": "merge", "related_ids": [6], "related_uuids": ["%[4]s"]}', updated_at = %[5]d WHERE id = 3 AND uuid = '%[2]s' AND transfer_token = NULL;
				UPDATE project_commitments SET status = 'superseded', superseded_at = %[5]d, supersede_context_json = '{"reason": "merge", "related_ids": [6], "related_uuids": ["%[4]s"]}', updated_at = %[5]d WHERE id = 4 AND uuid = '%[3]s' AND transfer_token = NULL;
				UPDATE project_commitments SET status = 'superseded', superseded_at = %[5]d, supersede_context_json = '{"reason": "merge", "related_ids": [6], "related_uuids": ["%[4]s"]}', updated_at = %[5]d WHERE id = 5 AND uuid = '%[1]s' AND transfer_token = NULL;
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (6, '%[4]s', 3, 2, 'confirmed', 24, '2 hours', %[5]d, 'uuid-for-alice', 'alice@Default', %[5]d, %[6]d, '{"reason": "merge", "related_ids": [5, 3, 4], "related_uuids": ["%[1]s", "%[2]s", "%[3]s"]}', %[5]d);`,
				uuidMerged1, uuid3, uuid4, uuidMerged2, mergeTime2.Unix(), expiresAt3.Unix())
		})
	}
}

func TestCommitmentMergeErrors(t *testing.T) {
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			s, tr, uuid1, uuid2, _, _, _, _, _ := commonMergeCommitmentsSetup(t, manager)

			// check permissions
			s.TokenValidator.Enforcer.AllowCommitmentUpdate = false
			mergeCommitmentsAndExpectError(t, s, tr, map[string]any{"commitment_uuids": []string{string(uuid1), string(uuid2)}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusForbidden, "Forbidden\n")
			})
			s.TokenValidator.Enforcer.AllowCommitmentUpdate = true

			// non-existing commitment
			mergeCommitmentsAndExpectError(t, s, tr, map[string]any{"commitment_uuids": []string{string(uuid1), "nonexistent-uuid"}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such commitment\n")
			})

			// 0 commitment UUIDs
			mergeCommitmentsAndExpectError(t, s, tr, map[string]any{"commitment_uuids": []string{}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment merge requires at least two commitments\n")
			})

			// 1 commitment UUID
			mergeCommitmentsAndExpectError(t, s, tr, map[string]any{"commitment_uuids": []string{string(uuid1)}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment merge requires at least two commitments\n")
			})

			// commitment in transfer cannot be merged
			s.Handler.RespondTo(s.Ctx, "PATCH /resources/v2/commitments/"+string(uuid1), httptest.WithJSONBody(map[string]any{"transfer_status": "public"})).
				ExpectStatus(t, http.StatusAccepted)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()
			mergeCommitmentsAndExpectError(t, s, tr, map[string]any{"commitment_uuids": []string{string(uuid1), string(uuid2)}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitments in transfer cannot be merged\n")
			})
			// revert transfer status
			s.Handler.RespondTo(s.Ctx, "PATCH /resources/v2/commitments/"+string(uuid1), httptest.WithJSONBody(map[string]any{"transfer_status": ""})).
				ExpectStatus(t, http.StatusAccepted)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()

			// deleted/inactive commitment
			s.Handler.RespondTo(s.Ctx, "DELETE /resources/v2/commitments/"+string(uuid2)).ExpectStatus(t, http.StatusNoContent)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()
			mergeCommitmentsAndExpectError(t, s, tr, map[string]any{"commitment_uuids": []string{string(uuid1), string(uuid2)}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such commitment\n")
			})
		})
	}
}
