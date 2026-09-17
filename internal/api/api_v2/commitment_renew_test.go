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

func renewCommitmentAndExpectSuccess(t *testing.T, s test.Setup, liquidHandlesCommitments bool, uuid liquid.CommitmentUUID, request map[string]any, expected jsonmatch.Object, getAuditTarget func() cadf.Resource) {
	t.Helper()
	ctx := t.Context()
	mockLiquid := s.LiquidClients["first"]

	if liquidHandlesCommitments {
		mockLiquid.LastCommitmentChangeRequest = liquid.CommitmentChangeRequest{}
	}

	path := "/resources/v2/commitments/" + string(uuid) + "/renew"
	s.Handler.RespondTo(ctx, "POST "+path, httptest.WithJSONBody(request)).
		ExpectJSON(t, http.StatusAccepted, expected)
	target := getAuditTarget()
	s.Auditor.ExpectEvents(t, cadf.Event{
		Action:      "create",
		Outcome:     "success",
		Reason:      cadf.Reason{ReasonType: "HTTP", ReasonCode: "202"},
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

func renewCommitmentAndExpectError(t *testing.T, s test.Setup, tr *easypg.Tracker, uuid liquid.CommitmentUUID, request map[string]any, expect func(r httptest.Response)) {
	t.Helper()
	ctx := t.Context()

	methodAndPath := "POST /resources/v2/commitments/" + string(uuid) + "/renew"
	s.Handler.RespondTo(ctx, methodAndPath, httptest.WithJSONBody(request)).Expect(expect)
	tr.DBChanges().AssertEmpty()
	s.Auditor.ExpectEvents(t)
}

func TestCommitmentRenewHappyPath(t *testing.T) {
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			s, tr, uuidOriginal, _, initialExpiresAt, expectedJSON := commonExistingCommitmentSetup(t, manager)

			s.Clock.StepBy(1 * time.Minute)
			renewedAt := s.Clock.Now()

			// renew #1 with the same duration ("1 hour"), notify_on_confirm=false
			var uuidRenewed1 liquid.CommitmentUUID
			renewedExpectedJSON1 := maps.Clone(expectedJSON)
			renewedExpectedJSON1["uuid"] = jsonmatch.CaptureField(&uuidRenewed1)
			renewedExpectedJSON1["status"] = "planned"
			renewedExpectedJSON1["created_at"] = renewedAt.Format(time.RFC3339)
			renewedExpectedJSON1["updated_at"] = renewedAt.Format(time.RFC3339)
			renewedExpectedJSON1["expires_at"] = initialExpiresAt.Add(1 * time.Hour).Format(time.RFC3339)
			renewedExpectedJSON1["confirm_by"] = initialExpiresAt.Format(time.RFC3339)
			delete(renewedExpectedJSON1, "confirmed_at")
			renewCommitmentAndExpectSuccess(t, s, manager == "liquid", uuidOriginal,
				map[string]any{"duration": "1 hour"},
				renewedExpectedJSON1,
				func() cadf.Resource {
					return cadf.Resource{
						TypeURI:     "service/resources/commitment",
						ID:          string(uuidRenewed1),
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
											TotalConfirmedBefore: 10, TotalConfirmedAfter: 10, TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
											Commitments: []liquid.Commitment{{
												UUID:      uuidRenewed1,
												NewStatus: Some(liquid.CommitmentStatusPlanned),
												Amount:    10,
												ConfirmBy: Some(initialExpiresAt),
												ExpiresAt: initialExpiresAt.Add(1 * time.Hour),
											}},
										},
									},
								},
							},
						}))},
					}
				})
			tr.DBChanges().AssertEqualf(`
				UPDATE project_commitments SET renew_context_json = '{"reason": "renew", "related_ids": [2], "related_uuids": ["%[2]s"]}', updated_at = %[3]d WHERE id = 1 AND uuid = '%[1]s' AND transfer_token = NULL;
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirm_by, expires_at, creation_context_json, updated_at) VALUES (2, '%[2]s', 3, 2, 'planned', 10, '1 hour', %[3]d, 'uuid-for-alice', 'alice@Default', %[4]d, %[5]d, '{"reason": "renew", "related_ids": [1], "related_uuids": ["%[1]s"]}', %[3]d);`,
				uuidOriginal, uuidRenewed1, renewedAt.Unix(), initialExpiresAt.Unix(), initialExpiresAt.Add(1*time.Hour).Unix())

			// create a second confirmed commitment so we can test renew #2
			s.Clock.StepBy(1 * time.Minute)
			createdAt2 := s.Clock.Now()
			expiresAt2 := createdAt2.Add(1 * time.Hour)
			var uuidOriginal2 liquid.CommitmentUUID
			expectedJSON["uuid"] = jsonmatch.CaptureField(&uuidOriginal2)
			expectedJSON["created_at"] = createdAt2.Format(time.RFC3339)
			expectedJSON["confirmed_at"] = createdAt2.Format(time.RFC3339)
			expectedJSON["expires_at"] = expiresAt2.Format(time.RFC3339)
			expectedJSON["updated_at"] = createdAt2.Format(time.RFC3339)
			s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
				"amount":            10,
				"duration":          "1 hour",
				"project_id":        "uuid-for-paris",
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"status":            "confirmed",
			})).ExpectJSON(t, http.StatusCreated, expectedJSON)
			s.Auditor.IgnoreEventsUntilNow()
			tr.DBChanges().Ignore()

			// renew #2 with a longer duration ("2 hours") and notify_on_confirm=true
			s.Clock.StepBy(1 * time.Minute)
			renewedAt2 := s.Clock.Now()
			var uuidRenewed2 liquid.CommitmentUUID
			renewedExpectedJSON2 := maps.Clone(expectedJSON)
			renewedExpectedJSON2["uuid"] = jsonmatch.CaptureField(&uuidRenewed2)
			renewedExpectedJSON2["status"] = "planned"
			renewedExpectedJSON2["duration"] = "2 hours"
			renewedExpectedJSON2["notify_on_confirm"] = true
			renewedExpectedJSON2["created_at"] = renewedAt2.Format(time.RFC3339)
			renewedExpectedJSON2["updated_at"] = renewedAt2.Format(time.RFC3339)
			renewedExpectedJSON2["expires_at"] = expiresAt2.Add(2 * time.Hour).Format(time.RFC3339)
			renewedExpectedJSON2["confirm_by"] = expiresAt2.Format(time.RFC3339)
			delete(renewedExpectedJSON2, "confirmed_at")
			renewCommitmentAndExpectSuccess(t, s, manager == "liquid", uuidOriginal2,
				map[string]any{"duration": "2 hours", "notify_on_confirm": true},
				renewedExpectedJSON2,
				func() cadf.Resource {
					return cadf.Resource{
						TypeURI:     "service/resources/commitment",
						ID:          string(uuidRenewed2),
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
											TotalConfirmedBefore: 20, TotalConfirmedAfter: 20, TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
											Commitments: []liquid.Commitment{{
												UUID:      uuidRenewed2,
												NewStatus: Some(liquid.CommitmentStatusPlanned),
												Amount:    10,
												ConfirmBy: Some(expiresAt2),
												ExpiresAt: expiresAt2.Add(2 * time.Hour),
											}},
										},
									},
								},
							},
						}))},
					}
				})
			// the second original commitment (id=3 because uuidOriginal2 is the 3rd commitment) gets renew_context_json set;
			// the renewed one is inserted (id=4)
			tr.DBChanges().AssertEqualf(`
				UPDATE project_commitments SET renew_context_json = '{"reason": "renew", "related_ids": [4], "related_uuids": ["%[2]s"]}', updated_at = %[3]d WHERE id = 3 AND uuid = '%[1]s' AND transfer_token = NULL;
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirm_by, expires_at, notify_on_confirm, creation_context_json, updated_at) VALUES (4, '%[2]s', 3, 2, 'planned', 10, '2 hours', %[3]d, 'uuid-for-alice', 'alice@Default', %[4]d, %[5]d, TRUE, '{"reason": "renew", "related_ids": [3], "related_uuids": ["%[1]s"]}', %[3]d);`,
				uuidOriginal2, uuidRenewed2, renewedAt2.Unix(), expiresAt2.Unix(), expiresAt2.Add(2*time.Hour).Unix())
		})
	}
}

func TestCommitmentRenewErrors(t *testing.T) {
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			s, tr, uuidOriginal, _, initialExpiresAt, expectedJSON := commonExistingCommitmentSetup(t, manager)

			// check permissions
			s.TokenValidator.Enforcer.AllowCommitmentCreate = false
			renewCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{"duration": "1 hour"}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusForbidden, "Forbidden\n")
			})
			s.TokenValidator.Enforcer.AllowCommitmentCreate = true

			// non-existing commitment
			renewCommitmentAndExpectError(t, s, tr, "bla", map[string]any{"duration": "1 hour"}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such commitment\n")
			})

			// invalid duration (not in behavior.Durations)
			renewCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{"duration": "3 hours"}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "unacceptable commitment duration for this resource; acceptable values: [\"1 hour\",\"2 hours\",\"1 year\"]\n")
			})

			// not confirmed: create a planned commitment and try to renew it
			s.Clock.StepBy(1 * time.Minute)
			plannedAt := s.Clock.Now()
			var uuidPlanned liquid.CommitmentUUID
			expectedJSON["uuid"] = jsonmatch.CaptureField(&uuidPlanned)
			expectedJSON["status"] = "planned"
			expectedJSON["created_at"] = plannedAt.Format(time.RFC3339)
			expectedJSON["updated_at"] = plannedAt.Format(time.RFC3339)
			expectedJSON["confirm_by"] = plannedAt.Add(1 * time.Hour).Format(time.RFC3339)
			expectedJSON["expires_at"] = plannedAt.Add(1 * time.Hour).Add(1 * time.Hour).Format(time.RFC3339)
			delete(expectedJSON, "confirmed_at")
			s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
				"amount":            10,
				"duration":          "1 hour",
				"project_id":        "uuid-for-paris",
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"status":            "planned",
				"confirm_by":        plannedAt.Add(1 * time.Hour).Format(time.RFC3339),
			})).ExpectJSON(t, http.StatusCreated, expectedJSON)
			s.Auditor.IgnoreEventsUntilNow()
			tr.DBChanges().Ignore()
			renewCommitmentAndExpectError(t, s, tr, uuidPlanned, map[string]any{"duration": "1 hour"}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment renewal is only allowed for confirmed commitments\n")
			})

			// in transfer: set uuidOriginal to public transfer, then try to renew
			s.Handler.RespondTo(s.Ctx, "PATCH /resources/v2/commitments/"+string(uuidOriginal), httptest.WithJSONBody(map[string]any{"transfer_status": "public"})).
				ExpectStatus(t, http.StatusAccepted)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()
			renewCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{"duration": "1 hour"}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment renewal is only allowed for commitments which are not in transfer\n")
			})
			// revert transfer status
			s.Handler.RespondTo(s.Ctx, "PATCH /resources/v2/commitments/"+string(uuidOriginal), httptest.WithJSONBody(map[string]any{"transfer_status": ""})).
				ExpectStatus(t, http.StatusAccepted)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()

			// already expired: step clock past initialExpiresAt
			s.Clock.StepBy(initialExpiresAt.Sub(s.Clock.Now()) + 1*time.Second)
			renewCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{"duration": "1 hour"}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment renewal is not allowed for expired commitments\n")
			})

			// too early: create a "1 year" confirmed commitment (expires ~1 year from now, well outside the 90-day window)
			nowForYearCommitment := s.Clock.Now()
			expiresAtYear := nowForYearCommitment.Add(365 * 24 * time.Hour)
			var uuidYear liquid.CommitmentUUID
			expectedJSON["uuid"] = jsonmatch.CaptureField(&uuidYear)
			expectedJSON["status"] = "confirmed"
			expectedJSON["duration"] = "1 year"
			expectedJSON["created_at"] = nowForYearCommitment.Format(time.RFC3339)
			expectedJSON["confirmed_at"] = nowForYearCommitment.Format(time.RFC3339)
			expectedJSON["updated_at"] = nowForYearCommitment.Format(time.RFC3339)
			expectedJSON["expires_at"] = expiresAtYear.Format(time.RFC3339)
			delete(expectedJSON, "confirm_by")
			s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
				"amount":            10,
				"duration":          "1 year",
				"project_id":        "uuid-for-paris",
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"status":            "confirmed",
			})).ExpectJSON(t, http.StatusCreated, expectedJSON)
			s.Auditor.IgnoreEventsUntilNow()
			tr.DBChanges().Ignore()
			renewCommitmentAndExpectError(t, s, tr, uuidYear, map[string]any{"duration": "1 year"}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment renewal is only possible in a certain timespan before expiry\n")
			})

			// already renewed: renew uuidYear once (step into the renewal window first), then try again
			s.Clock.StepBy(expiresAtYear.Sub(s.Clock.Now()) - 1*time.Hour)
			s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/"+string(uuidYear)+"/renew", httptest.WithJSONBody(map[string]any{"duration": "1 year"})).
				ExpectStatus(t, http.StatusAccepted)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()

			renewCommitmentAndExpectError(t, s, tr, uuidYear, map[string]any{"duration": "1 year"}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment was already renewed\n")
			})

			// deleted commitment
			s.Handler.RespondTo(s.Ctx, "DELETE /resources/v2/commitments/"+string(uuidOriginal)).ExpectStatus(t, http.StatusNoContent)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()
			renewCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{"duration": "1 hour"}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such commitment\n")
			})
		})
	}
}
