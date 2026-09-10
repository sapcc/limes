// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"encoding/json"
	"fmt"
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
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

// Helper function for a successful commitment patch.
func patchCommitmentAndExpectSuccess(t *testing.T, s test.Setup, noop, liquidHandlesCommitments bool, uuid string, request map[string]any, expected jsonmatch.Object, auditEvent cadf.Resource) {
	t.Helper()
	ctx := t.Context()
	mockLiquid := s.LiquidClients["first"]

	if liquidHandlesCommitments {
		mockLiquid.LastCommitmentChangeRequest = liquid.CommitmentChangeRequest{}
	}

	// we only expect the audit event and call to the liquid here
	// (the DB effect is not checked here; the caller will take care of that afterwards)
	path := "/resources/v2/commitments/" + uuid
	s.Handler.RespondTo(ctx, "PATCH "+path, httptest.WithJSONBody(request)).
		ExpectJSON(t, http.StatusAccepted, expected)
	if noop {
		s.Auditor.ExpectEvents(t)
	} else {
		s.Auditor.ExpectEvents(t, cadf.Event{
			Action:      "update",
			Outcome:     "success",
			Reason:      cadf.Reason{ReasonType: "HTTP", ReasonCode: "202"},
			RequestPath: path,
			Target:      auditEvent,
		})
	}

	if liquidHandlesCommitments && !noop {
		// check that the mock liquid saw the correct CommitmentChangeRequest
		// (the same as inside the audit event payload)
		actualCCR := must.Return(json.Marshal(mockLiquid.LastCommitmentChangeRequest))
		var expectedCCR jsonmatch.Object
		must.SucceedT(t, json.Unmarshal([]byte(auditEvent.Attachments[0].Content.(string)), &expectedCCR))
		for _, diff := range expectedCCR.DiffAgainst(actualCCR) {
			t.Error("in MockLiquid.LastCommitmentChangeRequest: " + diff.String())
		}
	} else {
		assert.Equal(t, mockLiquid.LastCommitmentChangeRequest, liquid.CommitmentChangeRequest{})
	}
}

func patchCommitmentAndExpectError(t *testing.T, s test.Setup, tr *easypg.Tracker, uuid string, request map[string]any, expect func(r httptest.Response)) {
	t.Helper()
	ctx := t.Context()

	methodAndPath := "PATCH /resources/v2/commitments/" + uuid
	s.Handler.RespondTo(ctx, methodAndPath, httptest.WithJSONBody(request)).Expect(expect)
	tr.DBChanges().AssertEmpty()
	s.Auditor.ExpectEvents(t)
}

func commonPatchTestSetup(t *testing.T, manager string) (s test.Setup, tr *easypg.Tracker, uuid string, expiresAt time.Time, expectedJSON jsonmatch.Object) {
	t.Helper()

	srvInfoFirst := test.DefaultLiquidServiceInfo("First")
	if manager == "liquid" {
		for resName, resInfo := range srvInfoFirst.Resources {
			resInfo.HandlesCommitments = true
			srvInfoFirst.Resources[resName] = resInfo
		}
	}

	s = test.NewSetup(t,
		test.WithConfig(string(must.ReturnT(httptest.NewJQModifiableJSONString(commitmentCreateConfigJSON, "TestCommitmentPatch config").
			Modify("del(.liquids.second)").
			Modify("del(.areas.second)").
			MarshalJSON())(t))),
		test.WithMockLiquidClient("first", srvInfoFirst),
		test.WithPersistedServiceInfo("first", srvInfoFirst),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)

	// we need capacity to confirm commitments
	firstCapacityAZOneID := s.GetAZResourceID("first", "capacity", "az-one")
	s.MustDBExec("UPDATE az_resources SET raw_capacity = $1 WHERE id = $2", 100, firstCapacityAZOneID)
	// update ServiceInfoCache (used by az_allocation_stats file)
	must.ReturnT(t, s.Cluster.SIC.InvalidateService(s.Ctx, Some(db.ServiceType("first"))))

	// setup: place one commitment into the DB
	createdAt := s.Clock.Now().UTC()
	expiresAt = s.Clock.Now().Add(1 * time.Hour).UTC()
	expectedJSON = jsonmatch.Object{
		"uuid":              jsonmatch.CaptureField(&uuid),
		"amount":            10,
		"duration":          "1 hour",
		"project_id":        "uuid-for-paris",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
		"created_at":        createdAt.Format(time.RFC3339),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      createdAt.Format(time.RFC3339),
		"expires_at":        expiresAt.Format(time.RFC3339),
		"updated_at":        createdAt.Format(time.RFC3339),
	}
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
	tr, tr0 := easypg.NewTracker(t, s.DB.DB)
	tr0.Ignore()
	return
}

func TestCommitmentPatchHappyPaths(t *testing.T) {
	// run this test twice, once with commitments managed by Limes, and once managed by the liquid
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			s, tr, uuid, expiresAt, expectedJSON := commonPatchTestSetup(t, manager)

			// adjust expectations and update transfer status
			s.Clock.StepBy(time.Minute)
			var transferToken string
			expectedJSON["transfer_token"] = jsonmatch.CaptureField(&transferToken)
			expectedJSON["transfer_status"] = "public"
			expectedJSON["updated_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			expectedAuditEvent := cadf.Resource{
				TypeURI:     "service/resources/commitment",
				ID:          uuid,
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
										UUID:      liquid.CommitmentUUID(uuid),
										OldStatus: Some(liquid.CommitmentStatusConfirmed),
										NewStatus: Some(liquid.CommitmentStatusConfirmed),
										Amount:    10,
										ExpiresAt: expiresAt,
									}},
								},
							},
						},
					},
				})), must.Return(cadf.NewJSONAttachment("context-payload", map[string]audit.CommitmentAttributeChangeset{
					uuid: {
						OldTransferStatus: "",
						NewTransferStatus: "public",
					},
				}))},
			}
			patchCommitmentAndExpectSuccess(t, s, false, manager == "liquid", uuid, map[string]any{"transfer_status": "public"}, expectedJSON, expectedAuditEvent)
			tr.DBChanges().AssertEqualf(`
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, transfer_status, transfer_token, creation_context_json, transfer_started_at, updated_at) VALUES (1, '%[1]s', 3, 2, 'confirmed', 10, '1 hour', 0, 'uuid-for-alice', 'alice@Default', 0, 3600, 'public', '%[3]s', '{"reason": "create"}', %[2]d, %[2]d);
				DELETE FROM project_commitments WHERE id = 1 AND uuid = '%[1]s' AND transfer_token = NULL;
			`, uuid, s.Clock.Now().UTC().Unix(), transferToken)

			// patch with the same input does nothing, no updates to timestamps
			s.Clock.StepBy(time.Minute)
			patchCommitmentAndExpectSuccess(t, s, true, manager == "liquid", uuid, map[string]any{"transfer_status": "public"}, expectedJSON, cadf.Resource{})
			tr.DBChanges().AssertEmpty()

			// now we du a bunch of status updates to check that all transitions are possible:
			oldStatus := limesresources.CommitmentTransferStatus("public")
			for _, newStatus := range []limesresources.CommitmentTransferStatus{"unlisted", "public", "", "public", "unlisted", "", "unlisted", ""} {
				t.Run(fmt.Sprintf("iteration=%q->%q", oldStatus, newStatus), func(t *testing.T) {
					s.Clock.StepBy(time.Minute)
					oldTransferToken := transferToken
					if newStatus != "" {
						expectedJSON["transfer_status"] = newStatus
						expectedJSON["transfer_token"] = jsonmatch.CaptureField(&transferToken)
					} else {
						delete(expectedJSON, "transfer_status")
						delete(expectedJSON, "transfer_token")
						transferToken = ""
					}
					expectedJSON["updated_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
					expectedAuditEvent.Attachments[1] = must.Return(cadf.NewJSONAttachment("context-payload", map[string]audit.CommitmentAttributeChangeset{
						uuid: {
							OldTransferStatus: oldStatus,
							NewTransferStatus: newStatus,
						},
					}))
					patchCommitmentAndExpectSuccess(t, s, false, manager == "liquid", uuid, map[string]any{"transfer_status": newStatus}, expectedJSON, expectedAuditEvent)

					// build the DELETE and INSERT statements, then sort them by their easypg key
					oldTokenSQL := fmt.Sprintf("'%s'", oldTransferToken)
					if oldTokenSQL == "''" {
						oldTokenSQL = "NULL"
					}
					newTokenSQL := fmt.Sprintf("'%s'", transferToken)
					if newTokenSQL == "''" {
						newTokenSQL = "NULL"
					}
					assert.Equal(t, oldTransferToken != transferToken, true)

					deleteStmt := fmt.Sprintf("DELETE FROM project_commitments WHERE id = 1 AND uuid = '%s' AND transfer_token = %s;\n", uuid, oldTokenSQL)
					var insertStmt string
					if newStatus == "" {
						insertStmt = fmt.Sprintf(`INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (1, '%[1]s', 3, 2, 'confirmed', 10, '1 hour', 0, 'uuid-for-alice', 'alice@Default', 0, 3600, '{"reason": "create"}', %[2]d);
						`, uuid, s.Clock.Now().UTC().Unix())
					} else {
						insertStmt = fmt.Sprintf(`INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, transfer_status, transfer_token, creation_context_json, transfer_started_at, updated_at) VALUES (1, '%[1]s', 3, 2, 'confirmed', 10, '1 hour', 0, 'uuid-for-alice', 'alice@Default', 0, 3600, '%[3]s', '%[4]s', '{"reason": "create"}', %[2]d, %[2]d);
						`, uuid, s.Clock.Now().UTC().Unix(), newStatus, transferToken)
					}

					// easypg sorts by key string: "id = 1 AND uuid = '...' AND transfer_token = <val>"
					if oldTokenSQL < newTokenSQL {
						tr.DBChanges().AssertEqual(deleteStmt + insertStmt)
					} else {
						tr.DBChanges().AssertEqual(insertStmt + deleteStmt)
					}

					// patch with the same input does nothing
					s.Clock.StepBy(time.Minute)
					patchCommitmentAndExpectSuccess(t, s, true, manager == "liquid", uuid, map[string]any{"transfer_status": newStatus}, expectedJSON, cadf.Resource{})
					tr.DBChanges().AssertEmpty()

					oldStatus = newStatus
				})
			}

			// check extension of duration
			s.Clock.StepBy(time.Minute)
			expectedJSON["duration"] = "2 hours"
			expectedJSON["expires_at"] = expiresAt.Add(1 * time.Hour).Format(time.RFC3339)
			expectedJSON["updated_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			expectedAuditEvent.Attachments = []cadf.Attachment{must.Return(cadf.NewJSONAttachment("payload", liquid.CommitmentChangeRequest{
				AZ:          "az-one",
				InfoVersion: 1,
				ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
					"uuid-for-paris": {
						ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
							"capacity": {
								TotalConfirmedBefore: 10, TotalConfirmedAfter: 10, TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0, Commitments: []liquid.Commitment{{
									UUID:         liquid.CommitmentUUID(uuid),
									OldStatus:    Some(liquid.CommitmentStatusConfirmed),
									NewStatus:    Some(liquid.CommitmentStatusConfirmed),
									Amount:       10,
									ExpiresAt:    expiresAt.Add(1 * time.Hour).UTC(),
									OldExpiresAt: Some(expiresAt),
								}},
							},
						},
					},
				},
			}))}
			patchCommitmentAndExpectSuccess(t, s, false, manager == "liquid", uuid, map[string]any{"duration": "2 hours"}, expectedJSON, expectedAuditEvent)
		})
	}
}

func TestCommitmentPatchErrors(t *testing.T) {
	// run this test twice, once with commitments managed by Limes, and once managed by the liquid
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			s, tr, uuidOne, expiresAt, expectedJSON := commonPatchTestSetup(t, manager)

			// check permissions
			s.TokenValidator.Enforcer.AllowCommitmentPatch = false
			patchCommitmentAndExpectError(t, s, tr, uuidOne, map[string]any{}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusForbidden, "Forbidden\n")
			})
			s.TokenValidator.Enforcer.AllowCommitmentPatch = true

			// non-existing commitment
			patchCommitmentAndExpectError(t, s, tr, "bla", map[string]any{}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such commitment\n")
			})

			// no modifications
			patchCommitmentAndExpectError(t, s, tr, uuidOne, map[string]any{}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "one commitment modification has to be set\n")
			})

			// too many modifications
			patchCommitmentAndExpectError(t, s, tr, uuidOne, map[string]any{
				"duration":        "2 hours",
				"transfer_status": "public",
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "only one commitment modification may be set\n")
			})

			// non-existing status
			patchCommitmentAndExpectError(t, s, tr, uuidOne, map[string]any{"transfer_status": "bla"}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "no such commitment transfer status\n")
			})

			// non-existing duration
			patchCommitmentAndExpectError(t, s, tr, uuidOne, map[string]any{"duration": "1000 days"}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "unacceptable commitment duration for this resource; acceptable values: [\"1 hour\",\"2 hours\"]\n")
			})

			// duration shortening
			var uuidTwo string
			expectedJSON["uuid"] = jsonmatch.CaptureField(&uuidTwo)
			expectedJSON["duration"] = "2 hours"
			expectedJSON["expires_at"] = expiresAt.Add(1 * time.Hour).Format(time.RFC3339)
			s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
				"amount":            10,
				"duration":          "2 hours",
				"project_id":        "uuid-for-paris",
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"status":            "confirmed",
			})).ExpectJSON(t, http.StatusCreated, expectedJSON)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()
			patchCommitmentAndExpectError(t, s, tr, uuidTwo, map[string]any{"duration": "1 hour"}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment duration must not be shortened\n")
			})

			// inactive status
			s.Handler.RespondTo(s.Ctx, "DELETE /resources/v2/commitments/"+uuidTwo).ExpectStatus(t, http.StatusNoContent)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()
			patchCommitmentAndExpectError(t, s, tr, uuidTwo, map[string]any{}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such commitment\n")
			})

			if manager == "liquid" {
				// check rejection from backend causes proper answer
				s.LiquidClients["first"].CommitmentChangeResponse.Set(liquid.CommitmentChangeResponse{
					RejectionReason: "some reason",
					RetryAt:         Some(s.Clock.Now().Add(3 * time.Hour).UTC()),
				})
				patchCommitmentAndExpectError(t, s, tr, uuidOne, map[string]any{"duration": "2 hours"}, func(r httptest.Response) {
					r.ExpectText(t, http.StatusConflict, "some reason\n")
					r.ExpectHeader(t, "Retry-After", s.Clock.Now().Add(3*time.Hour).UTC().Format(time.RFC1123))
				})
			}
		})
	}
}
