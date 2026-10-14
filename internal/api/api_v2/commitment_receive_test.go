// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/sapcc/go-api-declarations/cadf"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"
	"go.xyrillian.de/gg/jsonmatch"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/audit"
	"github.com/sapcc/limes/internal/test"
)

func receiveCommitmentAndExpectSuccess(t *testing.T, s test.Setup, liquidHandlesCommitments bool, uuid liquid.CommitmentUUID, request map[string]any, expected jsonmatch.Object, getAuditTargets func() (cadf.Resource, cadf.Resource)) {
	t.Helper()
	ctx := t.Context()
	mockLiquid := s.LiquidClients["first"]

	if liquidHandlesCommitments {
		mockLiquid.LastCommitmentChangeRequest = liquid.CommitmentChangeRequest{}
	}

	// we only expect the audit event and call to the liquid here
	// (the DB effect is not checked here; the caller will take care of that afterwards)
	path := "/resources/v2/commitments/" + string(uuid) + "/receive"
	s.Handler.RespondTo(ctx, "POST "+path, httptest.WithJSONBody(request)).
		ExpectJSON(t, http.StatusCreated, expected)
	target0, target1 := getAuditTargets()

	// audit assertion: two events (one per project, sorted by project UUID)
	s.Auditor.ExpectEvents(t,
		cadf.Event{
			Action:      "create",
			Outcome:     "success",
			Reason:      cadf.Reason{ReasonType: "HTTP", ReasonCode: "201"},
			RequestPath: path,
			Target:      target0,
		},
		cadf.Event{
			Action:      "create",
			Outcome:     "success",
			Reason:      cadf.Reason{ReasonType: "HTTP", ReasonCode: "201"},
			RequestPath: path,
			Target:      target1,
		},
	)

	// CCR assertion: check that the mock liquid saw the correct CommitmentChangeRequest
	// (the same as inside the audit event payload)
	if liquidHandlesCommitments {
		actualCCR := must.Return(json.Marshal(mockLiquid.LastCommitmentChangeRequest))
		var expectedCCR jsonmatch.Object
		must.SucceedT(t, json.Unmarshal([]byte(target0.Attachments[0].Content.(string)), &expectedCCR))
		for _, diff := range expectedCCR.DiffAgainst(actualCCR) {
			t.Error("in MockLiquid.LastCommitmentChangeRequest: " + diff.String())
		}
	} else {
		assert.Equal(t, mockLiquid.LastCommitmentChangeRequest, liquid.CommitmentChangeRequest{})
	}
}

func receiveCommitmentAndExpectError(t *testing.T, s test.Setup, tr *easypg.Tracker, uuid liquid.CommitmentUUID, request map[string]any, expect func(r httptest.Response)) {
	t.Helper()
	ctx := t.Context()

	methodAndPath := "POST /resources/v2/commitments/" + string(uuid) + "/receive"
	s.Handler.RespondTo(ctx, methodAndPath, httptest.WithJSONBody(request)).Expect(expect)
	tr.DBChanges().AssertEmpty()
	s.Auditor.ExpectEvents(t)
}

func TestCommitmentReceiveHappyPaths(t *testing.T) {
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			s, tr, uuidOriginal, initialCreatedAt, initialExpiresAt, expectedJSON := commonExistingCommitmentSetup(t, manager)

			// make commitment public
			s.Clock.StepBy(1 * time.Minute)
			var transferToken string
			expectedJSON["transfer_token"] = jsonmatch.CaptureField(&transferToken)
			expectedJSON["transfer_status"] = "public"
			expectedJSON["transfer_started_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			expectedJSON["updated_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			patchCommitmentAndExpectSuccess(t, s, false, manager == "liquid", uuidOriginal, map[string]any{"transfer_status": "public"},
				expectedJSON, patchAuditTarget(uuidOriginal, initialExpiresAt, "", "public"))
			tr.DBChanges().Ignore()

			// full receive
			s.Clock.StepBy(1 * time.Minute)
			receiveTime1 := s.Clock.Now().UTC()
			s.TokenValidator.Enforcer.AllowCommitmentGet = false

			var uuidReceived liquid.CommitmentUUID
			receiveExpected := jsonmatch.Object{
				"uuid":              jsonmatch.CaptureField(&uuidReceived),
				"amount":            10,
				"duration":          "1 hour",
				"project_id":        "uuid-for-berlin",
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"status":            "confirmed",
				"created_at":        initialCreatedAt.Format(time.RFC3339),
				"creator_uuid":      "uuid-for-alice",
				"creator_name":      "alice@Default",
				"can_be_deleted":    true,
				"confirmed_at":      initialCreatedAt.Format(time.RFC3339),
				"expires_at":        initialExpiresAt.Format(time.RFC3339),
				"updated_at":        receiveTime1.Format(time.RFC3339),
			}

			receiveCommitmentAndExpectSuccess(t, s, manager == "liquid", uuidOriginal, map[string]any{"target_project_id": "uuid-for-berlin", "transfer_token": transferToken}, receiveExpected,
				func() (cadf.Resource, cadf.Resource) {
					receiveCCR := liquid.CommitmentChangeRequest{
						AZ:          "az-one",
						InfoVersion: 1,
						ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
							"uuid-for-paris": {
								ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
									"capacity": {
										TotalConfirmedBefore: 10, TotalConfirmedAfter: 0,
										TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
										Commitments: []liquid.Commitment{{
											UUID:      uuidOriginal,
											OldStatus: Some(liquid.CommitmentStatusConfirmed),
											NewStatus: Some(liquid.CommitmentStatusSuperseded),
											Amount:    10,
											ExpiresAt: initialExpiresAt,
										}},
									},
								},
							},
							"uuid-for-berlin": {
								ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
									"capacity": {
										TotalConfirmedBefore: 0, TotalConfirmedAfter: 10,
										TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
										Commitments: []liquid.Commitment{{
											UUID:      uuidReceived,
											NewStatus: Some(liquid.CommitmentStatusConfirmed),
											Amount:    10,
											ExpiresAt: initialExpiresAt,
										}},
									},
								},
							},
						},
					}
					ccrAttachment := must.Return(cadf.NewJSONAttachment("payload", receiveCCR))
					cacsAttachment := must.Return(cadf.NewJSONAttachment("context-payload", map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset{
						uuidOriginal: {
							OldTransferStatus: "public",
							NewTransferStatus: limesresources.CommitmentTransferStatusNone,
						},
					}))
					// sorted by project UUID: berlin < paris
					return cadf.Resource{
						TypeURI:     "service/resources/commitment",
						ID:          string(uuidReceived),
						DomainID:    "uuid-for-germany",
						DomainName:  "germany",
						ProjectID:   "uuid-for-berlin",
						ProjectName: "berlin",
						Attachments: []cadf.Attachment{ccrAttachment, cacsAttachment},
					}, cadf.Resource{
						TypeURI:     "service/resources/commitment",
						ID:          string(uuidOriginal),
						DomainID:    "uuid-for-france",
						DomainName:  "france",
						ProjectID:   "uuid-for-paris",
						ProjectName: "paris",
						Attachments: []cadf.Attachment{ccrAttachment, cacsAttachment},
					}
				},
			)
			tr.DBChanges().AssertEqualf(`
				DELETE FROM project_commitments WHERE id = 1 AND uuid = '%[1]s' AND transfer_token = '%[3]s';
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, superseded_at, creation_context_json, supersede_context_json, updated_at) VALUES (1, '%[1]s', 3, 2, 'superseded', 10, '1 hour', %[5]d, 'uuid-for-alice', 'alice@Default', %[5]d, %[6]d, %[4]d, '{"reason": "create"}', '{"reason": "receive", "related_ids": [2], "related_uuids": ["%[2]s"]}', %[4]d);
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (2, '%[2]s', 1, 2, 'confirmed', 10, '1 hour', %[5]d, 'uuid-for-alice', 'alice@Default', %[5]d, %[6]d, '{"reason": "receive", "related_ids": [1], "related_uuids": ["%[1]s"]}', %[4]d);
			`, uuidOriginal, uuidReceived, transferToken, receiveTime1.Unix(), initialCreatedAt.Unix(), initialExpiresAt.Unix())

			s.TokenValidator.Enforcer.AllowCommitmentGet = true

			// set unlisted
			s.Clock.StepBy(1 * time.Minute)
			receiveExpected["transfer_status"] = "unlisted"
			receiveExpected["transfer_token"] = jsonmatch.CaptureField(&transferToken)
			receiveExpected["transfer_started_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			receiveExpected["updated_at"] = s.Clock.Now().UTC().Format(time.RFC3339)

			patchCommitmentAndExpectSuccess(t, s, false, manager == "liquid", uuidReceived, map[string]any{"transfer_status": "unlisted"},
				receiveExpected, buildPatchAuditTarget(uuidReceived, initialExpiresAt, "", "unlisted",
					"uuid-for-germany", "germany", "uuid-for-berlin", "berlin", 10))
			tr.DBChanges().Ignore()

			// receive only a part
			s.Clock.StepBy(25 * time.Hour)
			s.TokenValidator.Enforcer.AllowCommitmentDeleteAdmin = false

			var uuidReceived2 liquid.CommitmentUUID
			receiveExpected["uuid"] = jsonmatch.CaptureField(&uuidReceived2)
			receiveExpected["amount"] = 6
			receiveExpected["updated_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			delete(receiveExpected, "transfer_started_at")
			delete(receiveExpected, "transfer_status")
			delete(receiveExpected, "transfer_token")
			delete(receiveExpected, "can_be_deleted")
			receiveExpected["project_id"] = "uuid-for-dresden"
			receiveCommitmentAndExpectSuccess(t, s, manager == "liquid", uuidReceived, map[string]any{"target_project_id": "uuid-for-dresden", "amount": 6}, receiveExpected, func() (cadf.Resource, cadf.Resource) {
				// The leftover UUID is not in the response; read it from the DB.
				var uuidLeftover liquid.CommitmentUUID
				must.SucceedT(t, s.DB.QueryRow(
					`SELECT uuid FROM project_commitments WHERE project_id = 1 AND status != 'superseded' AND uuid != $1`,
					uuidReceived,
				).Scan(&uuidLeftover))
				receiveCCR := liquid.CommitmentChangeRequest{
					AZ:          "az-one",
					InfoVersion: 1,
					ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
						"uuid-for-berlin": {
							ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
								"capacity": {
									TotalConfirmedBefore: 10, TotalConfirmedAfter: 4,
									TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
									Commitments: []liquid.Commitment{{
										UUID:      uuidReceived,
										OldStatus: Some(liquid.CommitmentStatusConfirmed),
										NewStatus: Some(liquid.CommitmentStatusSuperseded),
										Amount:    10,
										ExpiresAt: initialExpiresAt,
									}, {
										UUID:      uuidLeftover,
										NewStatus: Some(liquid.CommitmentStatusConfirmed),
										Amount:    4,
										ExpiresAt: initialExpiresAt,
									}},
								},
							},
						},
						"uuid-for-dresden": {
							ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
								"capacity": {
									TotalConfirmedBefore: 0, TotalConfirmedAfter: 6,
									TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
									Commitments: []liquid.Commitment{{
										UUID:      uuidReceived2,
										NewStatus: Some(liquid.CommitmentStatusConfirmed),
										Amount:    6,
										ExpiresAt: initialExpiresAt,
									}},
								},
							},
						},
					},
				}
				ccrAttachment := must.Return(cadf.NewJSONAttachment("payload", receiveCCR))
				cacsAttachment := must.Return(cadf.NewJSONAttachment("context-payload", map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset{
					uuidReceived: {
						OldTransferStatus: "unlisted",
						NewTransferStatus: limesresources.CommitmentTransferStatusNone,
					},
					uuidLeftover: {
						OldTransferStatus: limesresources.CommitmentTransferStatusNone,
						NewTransferStatus: "unlisted",
					},
				}))
				// sorted by project UUID: berlin < dresden
				return cadf.Resource{
					TypeURI:     "service/resources/commitment",
					ID:          string(uuidReceived),
					DomainID:    "uuid-for-germany",
					DomainName:  "germany",
					ProjectID:   "uuid-for-berlin",
					ProjectName: "berlin",
					Attachments: []cadf.Attachment{ccrAttachment, cacsAttachment},
				}, cadf.Resource{
					TypeURI:     "service/resources/commitment",
					ID:          string(uuidReceived2),
					DomainID:    "uuid-for-germany",
					DomainName:  "germany",
					ProjectID:   "uuid-for-dresden",
					ProjectName: "dresden",
					Attachments: []cadf.Attachment{ccrAttachment, cacsAttachment},
				}
			},
			)
			// Read the leftover UUID and its new transfer token from the DB for the DB-changes assertion
			var uuidLeftover liquid.CommitmentUUID
			var leftoverTransferToken string
			must.SucceedT(t, s.DB.QueryRow(
				`SELECT uuid, transfer_token FROM project_commitments WHERE project_id = 1 AND status != 'superseded' AND uuid != $1`,
				uuidReceived,
			).Scan(&uuidLeftover, &leftoverTransferToken))
			tr.DBChanges().AssertEqualf(`
				DELETE FROM project_commitments WHERE id = 2 AND uuid = '%[1]s' AND transfer_token = '%[2]s';
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, superseded_at, creation_context_json, supersede_context_json, updated_at) VALUES (2, '%[1]s', 1, 2, 'superseded', 10, '1 hour', %[8]d, 'uuid-for-alice', 'alice@Default', %[8]d, %[9]d, %[6]d, '{"reason": "receive", "related_ids": [1], "related_uuids": ["%[10]s"]}', '{"reason": "receive", "related_ids": [3, 4], "related_uuids": ["%[3]s", "%[4]s"]}', %[6]d);
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (3, '%[3]s', 2, 2, 'confirmed', 6, '1 hour', %[8]d, 'uuid-for-alice', 'alice@Default', %[8]d, %[9]d, '{"reason": "receive", "related_ids": [2], "related_uuids": ["%[1]s"]}', %[6]d);
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, transfer_status, transfer_token, creation_context_json, transfer_started_at, updated_at) VALUES (4, '%[4]s', 1, 2, 'confirmed', 4, '1 hour', %[6]d, 'uuid-for-alice', 'alice@Default', %[8]d, %[9]d, 'unlisted', '%[5]s', '{"reason": "split", "related_ids": [2], "related_uuids": ["%[1]s"]}', %[6]d, %[6]d);
			`, uuidReceived, transferToken, uuidReceived2, uuidLeftover, leftoverTransferToken, s.Clock.Now().UTC().Unix(), receiveTime1.Unix(), initialCreatedAt.Unix(), initialExpiresAt.Unix(), uuidOriginal)
			s.TokenValidator.Enforcer.AllowCommitmentDeleteAdmin = true
		})
	}
}

func TestCommitmentReceiveErrors(t *testing.T) {
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			s, tr, uuidOriginal, _, _, expectedJSON := commonExistingCommitmentSetup(t, manager)
			baseRequest := map[string]any{
				"target_project_id": "uuid-for-berlin",
			}

			// commitment not in transfer
			receiveCommitmentAndExpectError(t, s, tr, uuidOriginal, baseRequest, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment to receive is not in transfer\n")
			})

			// set commitment in transfer (so subsequent tests can test other errors)
			s.Clock.StepBy(1 * time.Minute)
			var transferToken string
			expectedJSON["transfer_token"] = jsonmatch.CaptureField(&transferToken)
			expectedJSON["transfer_status"] = "public"
			expectedJSON["transfer_started_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			expectedJSON["updated_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			s.Handler.RespondTo(s.Ctx, "PATCH /resources/v2/commitments/"+string(uuidOriginal), httptest.WithJSONBody(map[string]any{"transfer_status": "public"})).
				ExpectJSON(t, http.StatusAccepted, expectedJSON)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()

			// non-existing commitment UUID
			receiveCommitmentAndExpectError(t, s, tr, "does-not-exist", baseRequest, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such commitment\n")
			})

			// no permission to create in target project → 403
			s.TokenValidator.Enforcer.AllowCommitmentCreate = false
			receiveCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{
				"target_project_id": "uuid-for-berlin",
				"transfer_token":    transferToken,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusForbidden, "Forbidden\n")
			})
			s.TokenValidator.Enforcer.AllowCommitmentCreate = true

			// no commitment_get and no commitment_get_public
			s.TokenValidator.Enforcer.AllowCommitmentGet = false
			s.TokenValidator.Enforcer.AllowCommitmentGetPublic = false
			receiveCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{
				"target_project_id": "uuid-for-berlin",
				"transfer_token":    transferToken,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusForbidden, "Forbidden\n")
			})

			// commitment_get_public is true but wrong transfer token
			s.TokenValidator.Enforcer.AllowCommitmentGetPublic = true
			receiveCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{
				"target_project_id": "uuid-for-berlin",
				"transfer_token":    "wrong-token",
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusUnauthorized, "transfer_token does not match to commitment to receive\n")
			})
			s.TokenValidator.Enforcer.AllowCommitmentGet = true

			// source == target
			receiveCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{
				"target_project_id": "uuid-for-paris",
				"transfer_token":    transferToken,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "source and target project are equal\n")
			})

			// amount too high
			receiveCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{
				"target_project_id": "uuid-for-berlin",
				"amount":            11,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "cannot receive more amount than commitment in transfer has\n")
			})

			// amount == 0
			receiveCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{
				"target_project_id": "uuid-for-berlin",
				"amount":            0,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "cannot receive amount of 0\n")
			})

			// unknown target project
			receiveCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{
				"target_project_id": "uuid-for-nonexistent",
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such project (UUID = uuid-for-nonexistent)\n")
			})

			// resource is forbidden in target project
			berlinID := s.GetProjectID("berlin")
			firstCapacityID := s.GetResourceID("first", "capacity")
			s.MustDBExec(`UPDATE project_resources SET forbidden = $1 WHERE project_id = $2 AND resource_id = $3`, true, berlinID, firstCapacityID)
			tr.DBChanges().Ignore()
			receiveCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{
				"target_project_id": "uuid-for-berlin",
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusUnprocessableEntity, "in target project: resource is not enabled in this project\n")
			})
			s.MustDBExec(`UPDATE project_resources SET forbidden = $1 WHERE project_id = $2 AND resource_id = $3`, false, berlinID, firstCapacityID)
			tr.DBChanges().Ignore()

			// liquid rejection (only for liquid manager)
			if manager == "liquid" {
				mockLiquid := s.LiquidClients["first"]

				// rejection without Retry-After
				mockLiquid.CommitmentChangeResponse.Set(liquid.CommitmentChangeResponse{
					RejectionReason: "not enough capacity for transfer",
				})
				receiveCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{
					"target_project_id": "uuid-for-berlin",
				}, func(r httptest.Response) {
					r.ExpectHeader(t, "Retry-After", "").
						ExpectText(t, http.StatusConflict, "not enough capacity for transfer\n")
				})

				// rejection with Retry-After
				retryAt := s.Clock.Now().Add(1 * time.Hour)
				mockLiquid.CommitmentChangeResponse.Set(liquid.CommitmentChangeResponse{
					RejectionReason: "try again later",
					RetryAt:         Some(retryAt),
				})
				receiveCommitmentAndExpectError(t, s, tr, uuidOriginal, map[string]any{
					"target_project_id": "uuid-for-berlin",
				}, func(r httptest.Response) {
					r.ExpectHeader(t, "Retry-After", retryAt.Format(time.RFC1123)).
						ExpectText(t, http.StatusConflict, "try again later\n")
				})

				// reset mock liquid
				mockLiquid.CommitmentChangeResponse.Set(liquid.CommitmentChangeResponse{})
			}

			// deleted commitment
			s.Handler.RespondTo(s.Ctx, "DELETE /resources/v2/commitments/"+string(uuidOriginal)).ExpectStatus(t, http.StatusNoContent)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()
			receiveCommitmentAndExpectError(t, s, tr, uuidOriginal, baseRequest, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such commitment\n")
			})
		})
	}
}

// patchAuditTarget builds the expected cadf.Resource for a PATCH that only changes
// transfer_status on a commitment in the paris project.
func patchAuditTarget(uuid liquid.CommitmentUUID, expiresAt time.Time, oldTransferStatus, newTransferStatus string) cadf.Resource {
	return buildPatchAuditTarget(uuid, expiresAt, oldTransferStatus, newTransferStatus,
		"uuid-for-france", "france", "uuid-for-paris", "paris", 10)
}

func buildPatchAuditTarget(
	uuid liquid.CommitmentUUID,
	expiresAt time.Time,
	oldTransferStatus, newTransferStatus string,
	domainID, domainName, projectID, projectName string,
	totalConfirmed uint64,
) cadf.Resource {

	return cadf.Resource{
		TypeURI:     "service/resources/commitment",
		ID:          string(uuid),
		DomainID:    domainID,
		DomainName:  domainName,
		ProjectID:   projectID,
		ProjectName: projectName,
		Attachments: []cadf.Attachment{must.Return(cadf.NewJSONAttachment("payload", liquid.CommitmentChangeRequest{
			AZ:          "az-one",
			InfoVersion: 1,
			ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
				liquid.ProjectUUID(projectID): {
					ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
						"capacity": {
							TotalConfirmedBefore: totalConfirmed, TotalConfirmedAfter: totalConfirmed,
							TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
							Commitments: []liquid.Commitment{{
								UUID:      uuid,
								OldStatus: Some(liquid.CommitmentStatusConfirmed),
								NewStatus: Some(liquid.CommitmentStatusConfirmed),
								Amount:    totalConfirmed,
								ExpiresAt: expiresAt,
							}},
						},
					},
				},
			},
		})), must.Return(cadf.NewJSONAttachment("context-payload", map[liquid.CommitmentUUID]audit.CommitmentAttributeChangeset{
			uuid: {
				OldTransferStatus: limesresources.CommitmentTransferStatus(oldTransferStatus),
				NewTransferStatus: limesresources.CommitmentTransferStatus(newTransferStatus),
			},
		}))},
	}
}
