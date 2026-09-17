// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/sapcc/go-bits/easypg"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"
	"go.xyrillian.de/gg/jsonmatch"

	"github.com/sapcc/limes/internal/test"
)

// Helper function for a successful commitment split.
func splitCommitmentAndExpectSuccess(t *testing.T, s test.Setup, liquidHandlesCommitments bool, uuid liquid.CommitmentUUID, request map[string]any, expected jsonmatch.Object, auditEventFunc func() cadf.Resource) {
	t.Helper()
	ctx := t.Context()
	mockLiquid := s.LiquidClients["first"]

	if liquidHandlesCommitments {
		mockLiquid.LastCommitmentChangeRequest = liquid.CommitmentChangeRequest{}
	}

	// we only expect the audit event and call to the liquid here
	// (the DB effect is not checked here; the caller will take care of that afterwards)
	path := "/resources/v2/commitments/" + string(uuid) + "/split"
	s.Handler.RespondTo(ctx, "POST "+path, httptest.WithJSONBody(request)).
		ExpectJSON(t, http.StatusCreated, expected)
	ae := auditEventFunc()
	s.Auditor.ExpectEvents(t, cadf.Event{
		Action:      "create",
		Outcome:     "success",
		Reason:      cadf.Reason{ReasonType: "HTTP", ReasonCode: "201"},
		RequestPath: path,
		Target:      ae,
	})

	if liquidHandlesCommitments {
		// check that the mock liquid saw the correct CommitmentChangeRequest
		// (the same as inside the audit event payload)
		actualCCR := must.Return(json.Marshal(mockLiquid.LastCommitmentChangeRequest))
		var expectedCCR jsonmatch.Object
		must.SucceedT(t, json.Unmarshal([]byte(ae.Attachments[0].Content.(string)), &expectedCCR))
		for _, diff := range expectedCCR.DiffAgainst(actualCCR) {
			t.Error("in MockLiquid.LastCommitmentChangeRequest: " + diff.String())
		}
	} else {
		assert.Equal(t, mockLiquid.LastCommitmentChangeRequest, liquid.CommitmentChangeRequest{})
	}
}

func splitCommitmentAndExpectError(t *testing.T, s test.Setup, tr *easypg.Tracker, uuid liquid.CommitmentUUID, request map[string]any, expect func(r httptest.Response)) {
	t.Helper()
	ctx := t.Context()

	methodAndPath := "POST /resources/v2/commitments/" + string(uuid) + "/split"
	s.Handler.RespondTo(ctx, methodAndPath, httptest.WithJSONBody(request)).Expect(expect)
	tr.DBChanges().AssertEmpty()
	s.Auditor.ExpectEvents(t)
}

func TestCommitmentSplitHappyPaths(t *testing.T) {
	// run this test twice, once with commitments managed by Limes, and once managed by the liquid
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			s, tr, uuidOriginal, initialCreatedAt, initialExpiresAt, _ := commonExistingCommitmentSetup(t, manager)

			// let's split in 3 parts
			s.Clock.StepBy(1 * time.Minute)
			newCreatedAt := s.Clock.Now()
			var uuidNew1, uuidNew2, uuidNew3 liquid.CommitmentUUID
			commonSplitResult := jsonmatch.Object{
				"duration":          "1 hour",
				"project_id":        "uuid-for-paris",
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"status":            "confirmed",
				"created_at":        newCreatedAt.Format(time.RFC3339),
				"creator_uuid":      "uuid-for-alice",
				"creator_name":      "alice@Default",
				"can_be_deleted":    true,
				"confirmed_at":      initialCreatedAt.Format(time.RFC3339),
				"expires_at":        initialExpiresAt.Format(time.RFC3339),
				"updated_at":        newCreatedAt.Format(time.RFC3339),
			}
			o1 := maps.Clone(commonSplitResult)
			o1["uuid"] = jsonmatch.CaptureField(&uuidNew1)
			o1["amount"] = 1
			o2 := maps.Clone(commonSplitResult)
			o2["uuid"] = jsonmatch.CaptureField(&uuidNew2)
			o2["amount"] = 3
			o3 := maps.Clone(commonSplitResult)
			o3["uuid"] = jsonmatch.CaptureField(&uuidNew3)
			o3["amount"] = 6
			expectedJSON := jsonmatch.Object{"commitments": jsonmatch.Array{o1, o2, o3}}
			expectedAuditEventFunc := func() cadf.Resource {
				return cadf.Resource{
					TypeURI:     "service/resources/commitment",
					ID:          string(uuidOriginal),
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
										TotalConfirmedBefore: 10, TotalConfirmedAfter: 10, TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0, Commitments: []liquid.Commitment{{
											UUID:      uuidOriginal,
											OldStatus: Some(liquid.CommitmentStatusConfirmed),
											NewStatus: Some(liquid.CommitmentStatusSuperseded),
											Amount:    10,
											ExpiresAt: initialExpiresAt,
										}, {
											UUID:      uuidNew1,
											NewStatus: Some(liquid.CommitmentStatusConfirmed),
											Amount:    1,
											ExpiresAt: initialExpiresAt,
										}, {
											UUID:      uuidNew2,
											NewStatus: Some(liquid.CommitmentStatusConfirmed),
											Amount:    3,
											ExpiresAt: initialExpiresAt,
										}, {
											UUID:      uuidNew3,
											NewStatus: Some(liquid.CommitmentStatusConfirmed),
											Amount:    6,
											ExpiresAt: initialExpiresAt,
										}},
									},
								},
							},
						},
					}))},
				}
			}
			splitCommitmentAndExpectSuccess(t, s, manager == "liquid", uuidOriginal, map[string]any{"amounts": []uint64{1, 3, 6}}, expectedJSON, expectedAuditEventFunc)
			tr.DBChanges().AssertEqualf(`
				UPDATE project_commitments SET status = 'superseded', superseded_at = %[5]d, supersede_context_json = '{"reason": "split", "related_ids": [2, 3, 4], "related_uuids": ["%[2]s", "%[3]s", "%[4]s"]}', updated_at = %[5]d WHERE id = 1 AND uuid = '%[1]s' AND transfer_token = NULL;
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (2, '%[2]s', 3, 2, 'confirmed', 1, '1 hour', %[5]d, 'uuid-for-alice', 'alice@Default', %[6]d, %[7]d, '{"reason": "split", "related_ids": [1], "related_uuids": ["%[1]s"]}', %[5]d);
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (3, '%[3]s', 3, 2, 'confirmed', 3, '1 hour', %[5]d, 'uuid-for-alice', 'alice@Default', %[6]d, %[7]d, '{"reason": "split", "related_ids": [1], "related_uuids": ["%[1]s"]}', %[5]d);
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (4, '%[4]s', 3, 2, 'confirmed', 6, '1 hour', %[5]d, 'uuid-for-alice', 'alice@Default', %[6]d, %[7]d, '{"reason": "split", "related_ids": [1], "related_uuids": ["%[1]s"]}', %[5]d);`,
				uuidOriginal, uuidNew1, uuidNew2, uuidNew3, newCreatedAt.Unix(), initialCreatedAt.Unix(), initialExpiresAt.Unix())

			// take the 3rd one and split it again
			s.Clock.StepBy(1 * time.Minute)
			newCreatedAt = s.Clock.Now()
			var uuidNew4, uuidNew5, uuidNew6 liquid.CommitmentUUID
			o1["uuid"] = jsonmatch.CaptureField(&uuidNew4)
			o1["amount"] = 2
			o1["created_at"] = newCreatedAt.Format(time.RFC3339)
			o1["updated_at"] = newCreatedAt.Format(time.RFC3339)
			o2["uuid"] = jsonmatch.CaptureField(&uuidNew5)
			o2["amount"] = 2
			o2["created_at"] = newCreatedAt.Format(time.RFC3339)
			o2["updated_at"] = newCreatedAt.Format(time.RFC3339)
			o3["uuid"] = jsonmatch.CaptureField(&uuidNew6)
			o3["amount"] = 2
			o3["created_at"] = newCreatedAt.Format(time.RFC3339)
			o3["updated_at"] = newCreatedAt.Format(time.RFC3339)
			expectedJSON = jsonmatch.Object{"commitments": jsonmatch.Array{o1, o2, o3}}
			expectedAuditEventFunc = func() cadf.Resource {
				return cadf.Resource{
					TypeURI:     "service/resources/commitment",
					ID:          string(uuidNew3),
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
										TotalConfirmedBefore: 10, TotalConfirmedAfter: 10, TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0, Commitments: []liquid.Commitment{{
											UUID:      uuidNew3,
											OldStatus: Some(liquid.CommitmentStatusConfirmed),
											NewStatus: Some(liquid.CommitmentStatusSuperseded),
											Amount:    6,
											ExpiresAt: initialExpiresAt,
										}, {
											UUID:      uuidNew4,
											NewStatus: Some(liquid.CommitmentStatusConfirmed),
											Amount:    2,
											ExpiresAt: initialExpiresAt,
										}, {
											UUID:      uuidNew5,
											NewStatus: Some(liquid.CommitmentStatusConfirmed),
											Amount:    2,
											ExpiresAt: initialExpiresAt,
										}, {
											UUID:      uuidNew6,
											NewStatus: Some(liquid.CommitmentStatusConfirmed),
											Amount:    2,
											ExpiresAt: initialExpiresAt,
										}},
									},
								},
							},
						},
					}))},
				}
			}
			splitCommitmentAndExpectSuccess(t, s, manager == "liquid", uuidNew3, map[string]any{"amounts": []uint64{2, 2, 2}}, expectedJSON, expectedAuditEventFunc)
		})
	}
}

func TestCommitmentSplitErrors(t *testing.T) {
	// run this test twice, once with commitments managed by Limes, and once managed by the liquid
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			s, tr, uuidOriginal, _, _, _ := commonExistingCommitmentSetup(t, manager)

			// check permissions
			s.TokenValidator.Enforcer.AllowCommitmentCreate = false
			splitCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{"amounts": []uint64{2, 2}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusForbidden, "Forbidden\n")
			})
			s.TokenValidator.Enforcer.AllowCommitmentCreate = true

			// non-existing commitment
			splitCommitmentAndExpectError(t, s, tr, "bla", map[string]any{"amounts": []uint64{2, 2}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such commitment\n")
			})

			// 0 resulting commitments
			splitCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{"amounts": []uint64{}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment has to be split in two or more commitments\n")
			})

			// 1 resulting commitment
			splitCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{"amounts": []uint64{5}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment has to be split in two or more commitments\n")
			})

			// mismatched amounts
			splitCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{"amounts": []uint64{1, 1}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "sum of split amounts must equal the original commitment amount\n")
			})

			// amount overflow
			splitCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{"amounts": []uint64{5 + (1 << 63), 5 + (1 << 63)}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "sum of amounts must not overflow uint64\n")
			})

			// commitment in transfer cannot be split
			s.Handler.RespondTo(s.Ctx, "PATCH /resources/v2/commitments/"+string(uuidOriginal), httptest.WithJSONBody(map[string]any{"transfer_status": "public"})).
				ExpectStatus(t, http.StatusAccepted)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()
			splitCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{"amounts": []uint64{5, 5}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment in transfer must not be split\n")
			})

			// inactive status
			s.Handler.RespondTo(s.Ctx, "DELETE /resources/v2/commitments/"+string(uuidOriginal)).ExpectStatus(t, http.StatusNoContent)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()
			splitCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{"amounts": []uint64{5, 5}}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such commitment\n")
			})
		})
	}
}
