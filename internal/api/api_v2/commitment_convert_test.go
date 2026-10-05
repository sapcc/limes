// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"encoding/json"
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

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/common_fixtures"
)

var convertCommitmentConfigJSON = string(must.Return(httptest.NewJQModifiableJSONString(`{
		"liquids": {
			"first": {
				"area": "first"
			}
		}
	}`, "convertCommitmentConfigJSON").
	ModifyWithVariable(".liquids.second = $ref", common_fixtures.LiquidSecondWithConversions).
	Modify(`.liquids.second.commitment_behavior_per_resource += [
		{
			"key": "capacity_medium",
			"value": {
				"durations_per_domain": [{"key": "germany", "value": ["2 hours"]}]
			}
		}
	]`).
	ModifyWithVariable(".discovery = $ref", common_fixtures.DiscoveryBerlinDresdenParis).
	ModifyWithVariable(".areas = $ref", common_fixtures.AreasFirstSecond).
	ModifyWithVariable(".availability_zones = $ref", common_fixtures.AZsOneTwo).
	MarshalJSON()))

func convertCommitmentSetup(t *testing.T, manager string) (s test.Setup, tr *easypg.Tracker, uuid liquid.CommitmentUUID, createdAt, expiresAt time.Time, expectedJSON jsonmatch.Object) {
	t.Helper()

	srvInfoFirst := test.DefaultLiquidServiceInfo("first")
	srvInfoSecond := ServiceInfoSecondForCommitmentConversion()
	// add the error-case resource capacity_medium (AZ-aware, piece unit)
	srvInfoSecond.Resources["capacity_medium"] = liquid.ResourceInfo{
		DisplayName: "Capacity Medium",
		Category:    Some(liquid.CategoryName("foo_category")),
		Unit:        liquid.UnitPiece,
		Topology:    liquid.AZAwareTopology,
		HasCapacity: true,
		HasQuota:    true,
	}

	if manager == "liquid" {
		for resName, resInfo := range srvInfoSecond.Resources {
			resInfo.HandlesCommitments = true
			srvInfoSecond.Resources[resName] = resInfo
		}
	}

	s = test.NewSetup(t,
		test.WithConfig(convertCommitmentConfigJSON),
		test.WithMockLiquidClient("first", srvInfoFirst),
		test.WithPersistedServiceInfo("first", srvInfoFirst),
		test.WithMockLiquidClient("second", srvInfoSecond),
		test.WithPersistedServiceInfo("second", srvInfoSecond),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)

	// step clock past min_confirm_date (1970-01-08T00:00:00Z)
	s.Clock.StepBy(8 * 24 * time.Hour)

	// set raw_capacity on the target AZ resources so capacity checks pass (limes manager)
	for _, path := range []string{
		"second/capacity/az-one", "second/capacity/az-two", "second/capacity/total",
		"second/capacity_80/az-one", "second/capacity_80/az-two", "second/capacity_80/total",
		"second/capacity_32/az-one", "second/capacity_32/az-two", "second/capacity_32/total",
		"second/capacity_medium/az-one", "second/capacity_medium/az-two", "second/capacity_medium/total",
	} {
		s.MustDBExec(`UPDATE az_resources SET raw_capacity = $1 WHERE path = $2`, 1024, path)
	}
	// update ServiceInfoCache
	must.ReturnT(t, s.Cluster.SIC.InvalidateService(s.Ctx, Some(db.ServiceType("second"))))

	// create the initial confirmed commitment: second/capacity/az-one, 320 GiB, 1 hour, berlin
	createdAt = s.Clock.Now().UTC()
	expiresAt = s.Clock.Now().Add(1 * time.Hour).UTC()
	expectedJSON = jsonmatch.Object{
		"uuid":              jsonmatch.CaptureField(&uuid),
		"amount":            320,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "second",
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
		"amount":            320,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
	})).ExpectJSON(t, http.StatusCreated, expectedJSON)
	s.Auditor.IgnoreEventsUntilNow()
	tr, tr0 := easypg.NewTracker(t, s.DB.DB)
	tr0.Ignore()
	return
}

// Helper function for a successful commitment conversion.
func convertCommitmentAndExpectSuccess(t *testing.T, s test.Setup, liquidHandlesCommitments bool, uuid liquid.CommitmentUUID, request map[string]any, expected jsonmatch.Object, getTarget func() cadf.Resource) {
	t.Helper()
	ctx := t.Context()
	mockLiquid := s.LiquidClients["second"]

	if liquidHandlesCommitments {
		mockLiquid.LastCommitmentChangeRequest = liquid.CommitmentChangeRequest{}
	}

	// the DB effect is not checked here; the caller will take care of that afterwards
	path := "/resources/v2/commitments/" + string(uuid) + "/convert"
	s.Handler.RespondTo(ctx, "POST "+path, httptest.WithJSONBody(request)).
		ExpectJSON(t, http.StatusAccepted, expected)
	target := getTarget()
	s.Auditor.ExpectEvents(t, cadf.Event{
		Action:      "update",
		Outcome:     "success",
		Reason:      cadf.Reason{ReasonType: "HTTP", ReasonCode: "202"},
		RequestPath: path,
		Target:      target,
	})

	if liquidHandlesCommitments {
		// check that the mock liquid saw the correct CommitmentChangeRequest
		// (the same as inside the audit event payload)
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

func convertCommitmentAndExpectError(t *testing.T, s test.Setup, tr *easypg.Tracker, uuid liquid.CommitmentUUID, request map[string]any, expect func(r httptest.Response)) {
	t.Helper()
	ctx := t.Context()

	methodAndPath := "POST /resources/v2/commitments/" + string(uuid) + "/convert"
	s.Handler.RespondTo(ctx, methodAndPath, httptest.WithJSONBody(request)).Expect(expect)
	tr.DBChanges().AssertEmpty()
	s.Auditor.ExpectEvents(t)
}

func TestCommitmentConvertHappyPaths(t *testing.T) {
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			s, tr, uuidCapacity, initialCreatedAt, initialExpiresAt, expectedJSON := convertCommitmentSetup(t, manager)

			// convert all 320 GiB of capacity → 4 capacity_80 (from=320, to=4, one_way)
			s.Clock.StepBy(1 * time.Minute)
			var uuidCapacity80 liquid.CommitmentUUID
			expectedJSON["uuid"] = jsonmatch.CaptureField(&uuidCapacity80)
			expectedJSON["amount"] = 4
			expectedJSON["resource_name"] = "capacity_80"
			expectedJSON["created_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			expectedJSON["updated_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			convertCommitmentAndExpectSuccess(t, s, manager == "liquid", uuidCapacity,
				map[string]any{
					"target_service_type":  "second",
					"target_resource_name": "capacity_80",
					"source_amount":        320,
					"target_amount":        4,
				}, expectedJSON, func() cadf.Resource {
					return cadf.Resource{
						TypeURI:     "service/resources/commitment",
						ID:          string(uuidCapacity),
						DomainID:    "uuid-for-germany",
						DomainName:  "germany",
						ProjectID:   "uuid-for-berlin",
						ProjectName: "berlin",
						Attachments: []cadf.Attachment{must.Return(cadf.NewJSONAttachment("payload", liquid.CommitmentChangeRequest{
							AZ:          "az-one",
							InfoVersion: 1,
							ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
								"uuid-for-berlin": {
									ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
										"capacity": {
											TotalConfirmedBefore: 320, TotalConfirmedAfter: 0,
											TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
											Commitments: []liquid.Commitment{{
												UUID:      uuidCapacity,
												OldStatus: Some(liquid.CommitmentStatusConfirmed),
												NewStatus: Some(liquid.CommitmentStatusSuperseded),
												Amount:    320,
												ExpiresAt: initialExpiresAt,
											}},
										},
										"capacity_80": {
											TotalConfirmedBefore: 0, TotalConfirmedAfter: 4,
											TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
											Commitments: []liquid.Commitment{{
												UUID:      uuidCapacity80,
												NewStatus: Some(liquid.CommitmentStatusConfirmed),
												Amount:    4,
												ExpiresAt: initialExpiresAt,
											}},
										},
									},
								},
							},
						}))},
					}
				})
			tr.DBChanges().AssertEqualf(`
				UPDATE project_commitments SET status = 'superseded', superseded_at = %[3]d, supersede_context_json = '{"reason": "convert", "related_ids": [2], "related_uuids": ["%[2]s"]}', updated_at = %[3]d WHERE id = 1 AND uuid = '%[1]s' AND transfer_token = NULL;
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (2, '%[2]s', 1, %[6]d, 'confirmed', 4, '1 hour', %[3]d, 'uuid-for-alice', 'alice@Default', %[4]d, %[5]d, '{"reason": "convert", "related_ids": [1], "related_uuids": ["%[1]s"]}', %[3]d);
			`, uuidCapacity, uuidCapacity80, s.Clock.Now().UTC().Unix(), initialCreatedAt.Unix(), initialExpiresAt.Unix(),
				s.GetAZResourceID("second", "capacity_80", "az-one"))

			// convert 3 of the 4 capacity_80 → capacity_32 (from=4, to=15, allow_rounding)
			// (2×15)/4 = 7.5 → 7 (rounded down), leftover 1 capacity_80
			s.Clock.StepBy(1 * time.Minute)
			var uuidCapacity32_1 liquid.CommitmentUUID
			expectedJSON["uuid"] = jsonmatch.CaptureField(&uuidCapacity32_1)
			expectedJSON["amount"] = 7
			expectedJSON["resource_name"] = "capacity_32"
			expectedJSON["created_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			expectedJSON["updated_at"] = s.Clock.Now().UTC().Format(time.RFC3339)

			// Read the leftover UUID from the DB after we do the conversion
			convertCommitmentAndExpectSuccess(t, s, manager == "liquid", uuidCapacity80,
				map[string]any{
					"target_service_type":  "second",
					"target_resource_name": "capacity_32",
					"source_amount":        3,
					"target_amount":        7,
				}, expectedJSON, func() cadf.Resource {
					// The leftover UUID is not in the response; read it from the DB.
					var uuidCapacity80Leftover liquid.CommitmentUUID
					must.SucceedT(t, s.DB.QueryRow(
						`SELECT uuid FROM project_commitments WHERE az_resource_id = $1 AND status != 'superseded' AND uuid != $2`,
						s.GetAZResourceID("second", "capacity_80", "az-one"), uuidCapacity80,
					).Scan(&uuidCapacity80Leftover))

					return cadf.Resource{
						TypeURI:     "service/resources/commitment",
						ID:          string(uuidCapacity80),
						DomainID:    "uuid-for-germany",
						DomainName:  "germany",
						ProjectID:   "uuid-for-berlin",
						ProjectName: "berlin",
						Attachments: []cadf.Attachment{must.Return(cadf.NewJSONAttachment("payload", liquid.CommitmentChangeRequest{
							AZ:          "az-one",
							InfoVersion: 1,
							ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
								"uuid-for-berlin": {
									ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
										"capacity_80": {
											TotalConfirmedBefore: 4, TotalConfirmedAfter: 1,
											TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
											Commitments: []liquid.Commitment{{
												UUID:      uuidCapacity80,
												OldStatus: Some(liquid.CommitmentStatusConfirmed),
												NewStatus: Some(liquid.CommitmentStatusSuperseded),
												Amount:    4,
												ExpiresAt: initialExpiresAt,
											}, {
												UUID:      uuidCapacity80Leftover,
												NewStatus: Some(liquid.CommitmentStatusConfirmed),
												Amount:    1,
												ExpiresAt: initialExpiresAt,
											}},
										},
										"capacity_32": {
											TotalConfirmedBefore: 0, TotalConfirmedAfter: 7,
											TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
											Commitments: []liquid.Commitment{{
												UUID:      uuidCapacity32_1,
												NewStatus: Some(liquid.CommitmentStatusConfirmed),
												Amount:    7,
												ExpiresAt: initialExpiresAt,
											}},
										},
									},
								},
							},
						}))},
					}
				})
			// Read the leftover UUID for DB assertion
			var uuid80Leftover liquid.CommitmentUUID
			must.SucceedT(t, s.DB.QueryRow(
				`SELECT uuid FROM project_commitments WHERE az_resource_id = $1 AND status != 'superseded' AND uuid != $2`,
				s.GetAZResourceID("second", "capacity_80", "az-one"), uuidCapacity80,
			).Scan(&uuid80Leftover))
			azResCapacity80ID := s.GetAZResourceID("second", "capacity_80", "az-one")
			azResCapacity32ID := s.GetAZResourceID("second", "capacity_32", "az-one")
			tr.DBChanges().AssertEqualf(`
				UPDATE project_commitments SET status = 'superseded', superseded_at = %[4]d, supersede_context_json = '{"reason": "convert", "related_ids": [3, 4], "related_uuids": ["%[2]s", "%[3]s"]}', updated_at = %[4]d WHERE id = 2 AND uuid = '%[1]s' AND transfer_token = NULL;
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (3, '%[2]s', 1, %[8]d, 'confirmed', 7, '1 hour', %[4]d, 'uuid-for-alice', 'alice@Default', %[5]d, %[6]d, '{"reason": "convert", "related_ids": [2], "related_uuids": ["%[1]s"]}', %[4]d);
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (4, '%[3]s', 1, %[7]d, 'confirmed', 1, '1 hour', %[4]d, 'uuid-for-alice', 'alice@Default', %[5]d, %[6]d, '{"reason": "split", "related_ids": [2], "related_uuids": ["%[1]s"]}', %[4]d);
			`, uuidCapacity80, uuidCapacity32_1, uuid80Leftover, s.Clock.Now().UTC().Unix(), initialCreatedAt.Unix(), initialExpiresAt.Unix(),
				azResCapacity80ID, azResCapacity32ID)

			// convert the remaining 1 capacity_80 → capacity_32 (rounding again)
			// (1×15)/4 = 3.75 → 3, no leftover
			s.Clock.StepBy(1 * time.Minute)
			var uuidCapacity32_2 liquid.CommitmentUUID
			expectedJSON["uuid"] = jsonmatch.CaptureField(&uuidCapacity32_2)
			expectedJSON["amount"] = 2
			expectedJSON["created_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			expectedJSON["updated_at"] = s.Clock.Now().UTC().Format(time.RFC3339)
			convertCommitmentAndExpectSuccess(t, s, manager == "liquid", uuid80Leftover,
				map[string]any{
					"target_service_type":  "second",
					"target_resource_name": "capacity_32",
					"source_amount":        1,
					"target_amount":        2,
				}, expectedJSON, func() cadf.Resource {
					return cadf.Resource{
						TypeURI:     "service/resources/commitment",
						ID:          string(uuid80Leftover),
						DomainID:    "uuid-for-germany",
						DomainName:  "germany",
						ProjectID:   "uuid-for-berlin",
						ProjectName: "berlin",
						Attachments: []cadf.Attachment{must.Return(cadf.NewJSONAttachment("payload", liquid.CommitmentChangeRequest{
							AZ:          "az-one",
							InfoVersion: 1,
							ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
								"uuid-for-berlin": {
									ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
										"capacity_80": {
											TotalConfirmedBefore: 1, TotalConfirmedAfter: 0,
											TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
											Commitments: []liquid.Commitment{{
												UUID:      uuid80Leftover,
												OldStatus: Some(liquid.CommitmentStatusConfirmed),
												NewStatus: Some(liquid.CommitmentStatusSuperseded),
												Amount:    1,
												ExpiresAt: initialExpiresAt,
											}},
										},
										"capacity_32": {
											TotalConfirmedBefore: 7, TotalConfirmedAfter: 9,
											TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0,
											Commitments: []liquid.Commitment{{
												UUID:      uuidCapacity32_2,
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
				})
			tr.DBChanges().AssertEqualf(`
				UPDATE project_commitments SET status = 'superseded', superseded_at = %[3]d, supersede_context_json = '{"reason": "convert", "related_ids": [5], "related_uuids": ["%[2]s"]}', updated_at = %[3]d WHERE id = 4 AND uuid = '%[1]s' AND transfer_token = NULL;
				INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (5, '%[2]s', 1, %[6]d, 'confirmed', 2, '1 hour', %[3]d, 'uuid-for-alice', 'alice@Default', %[4]d, %[5]d, '{"reason": "convert", "related_ids": [4], "related_uuids": ["%[1]s"]}', %[3]d);
			`, uuid80Leftover, uuidCapacity32_2, s.Clock.Now().UTC().Unix(), initialCreatedAt.Unix(), initialExpiresAt.Unix(),
				azResCapacity32ID)
		})
	}
}

func TestCommitmentConvertErrors(t *testing.T) {
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			s, tr, uuidCapacity, _, _, _ := convertCommitmentSetup(t, manager)

			// check permissions
			s.TokenValidator.Enforcer.AllowCommitmentUpdate = false
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity_80",
				"source_amount":        320,
				"target_amount":        3,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusForbidden, "Forbidden\n")
			})
			s.TokenValidator.Enforcer.AllowCommitmentUpdate = true

			// non-existing commitment
			convertCommitmentAndExpectError(t, s, tr, "does-not-exist", map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity_80",
				"source_amount":        320,
				"target_amount":        3,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such commitment\n")
			})

			// commitment in transfer must not be converted
			s.Handler.RespondTo(s.Ctx, "PATCH /resources/v2/commitments/"+string(uuidCapacity), httptest.WithJSONBody(map[string]any{"transfer_status": "public"})).
				ExpectStatus(t, http.StatusAccepted)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity_80",
				"source_amount":        320,
				"target_amount":        3,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusUnprocessableEntity, "commitment in transfer must not be converted\n")
			})
			// remove transfer status
			s.Handler.RespondTo(s.Ctx, "PATCH /resources/v2/commitments/"+string(uuidCapacity), httptest.WithJSONBody(map[string]any{"transfer_status": ""})).
				ExpectStatus(t, http.StatusAccepted)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()

			// target: no such service
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "nonexistent",
				"target_resource_name": "capacity_80",
				"source_amount":        320,
				"target_amount":        3,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "in target resource: no such service\n")
			})

			// target: no such resource
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "nonexistent",
				"source_amount":        320,
				"target_amount":        3,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "in target resource: no such resource\n")
			})

			// target: resource is forbidden
			berlinID := s.GetProjectID("berlin")
			capacity80ResID := s.GetResourceID("second", "capacity_80")
			s.MustDBExec(`UPDATE project_resources SET forbidden = $1 WHERE project_id = $2 AND resource_id = $3`, true, berlinID, capacity80ResID)
			tr.DBChanges().Ignore()
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity_80",
				"source_amount":        320,
				"target_amount":        3,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusUnprocessableEntity, "in target resource: resource is not enabled in this project\n")
			})
			s.MustDBExec(`UPDATE project_resources SET forbidden = $1 WHERE project_id = $2 AND resource_id = $3`, false, berlinID, capacity80ResID)
			tr.DBChanges().Ignore()

			// target: commitments are not enabled (first/capacity has no commitment behavior)
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "first",
				"target_resource_name": "capacity",
				"source_amount":        320,
				"target_amount":        3,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusUnprocessableEntity, "in target resource: commitments are not enabled for this resource\n")
			})

			// target: unacceptable commitment duration (1 hour commitment → capacity_medium with ["2 hours"] only)
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity_medium",
				"source_amount":        320,
				"target_amount":        1,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusUnprocessableEntity, "in target resource: unacceptable commitment duration for this resource; acceptable values: [\"2 hours\"]\n")
			})

			// target: no such conversion available (need a 2-hour commitment for this)
			var uuid2h liquid.CommitmentUUID
			var resp2hBody struct {
				UUID liquid.CommitmentUUID `json:"uuid"`
			}
			s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
				"amount":            320,
				"duration":          "2 hours",
				"project_id":        "uuid-for-berlin",
				"service_type":      "second",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"status":            "confirmed",
			})).CaptureJSON(&resp2hBody).ExpectStatus(t, http.StatusCreated)
			uuid2h = resp2hBody.UUID
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()
			convertCommitmentAndExpectError(t, s, tr, uuid2h, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity_medium",
				"source_amount":        320,
				"target_amount":        1,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusUnprocessableEntity, "no such conversion available\n")
			})

			// conversion to same resource
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity",
				"source_amount":        320,
				"target_amount":        320,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "commitment conversion is only allowed to a different resource\n")
			})

			// source_amount = 0
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity_80",
				"source_amount":        0,
				"target_amount":        0,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "amount must be greater than zero\n")
			})

			// source_amount > commitment amount
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity_80",
				"source_amount":        400,
				"target_amount":        5,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "amount cannot be higher than the commitment amount\n")
			})

			// no rounding allowed (capacity → capacity_80: from=320, no allow_rounding)
			// source 32 is not a multiple of 320
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity_80",
				"source_amount":        32,
				"target_amount":        0,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusBadRequest, "for this conversion, no rounding is allowed and there would be a remainder on the target side\n")
			})

			// target amount would be zero (capacity → capacity_32: from=32, allow_rounding)
			// source 16 → (16 * 1) / 32 = 0
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity_32",
				"source_amount":        16,
				"target_amount":        0,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusUnprocessableEntity, "target amount would be zero\n")
			})

			// target amount does not match the conversion rate
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity_80",
				"source_amount":        320,
				"target_amount":        5,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusUnprocessableEntity, "target amount does not match the conversion rate\n")
			})

			// liquid rejection (only for liquid manager)
			if manager == "liquid" {
				s.LiquidClients["second"].CommitmentChangeResponse.Set(liquid.CommitmentChangeResponse{
					RejectionReason: "some reason",
					RetryAt:         Some(s.Clock.Now().Add(3 * time.Hour).UTC()),
				})
				convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
					"target_service_type":  "second",
					"target_resource_name": "capacity_80",
					"source_amount":        320,
					"target_amount":        4,
				}, func(r httptest.Response) {
					r.ExpectText(t, http.StatusConflict, "some reason\n")
					r.ExpectHeader(t, "Retry-After", s.Clock.Now().Add(3*time.Hour).UTC().Format(time.RFC1123))
				})
				// rejection without Retry-After
				s.LiquidClients["second"].CommitmentChangeResponse.Set(liquid.CommitmentChangeResponse{
					RejectionReason: "no retry",
				})
				convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
					"target_service_type":  "second",
					"target_resource_name": "capacity_80",
					"source_amount":        320,
					"target_amount":        4,
				}, func(r httptest.Response) {
					r.ExpectText(t, http.StatusConflict, "no retry\n")
					r.ExpectHeader(t, "Retry-After", "")
				})
				s.LiquidClients["second"].CommitmentChangeResponse.Set(liquid.CommitmentChangeResponse{})
			}

			// limes rejection: not enough capacity (only for limes manager)
			if manager == "limes" {
				// temporarily lower capacity on the target
				bigAZResID := s.GetAZResourceID("second", "capacity_80", "az-one")
				s.MustDBExec(`UPDATE az_resources SET raw_capacity = $1 WHERE id = $2`, 0, bigAZResID)
				must.ReturnT(t, s.Cluster.SIC.InvalidateService(s.Ctx, Some(db.ServiceType("second"))))
				tr.DBChanges().Ignore()
				convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
					"target_service_type":  "second",
					"target_resource_name": "capacity_80",
					"source_amount":        320,
					"target_amount":        4,
				}, func(r httptest.Response) {
					r.ExpectText(t, http.StatusConflict, "not enough capacity!\n")
				})
				// restore capacity
				s.MustDBExec(`UPDATE az_resources SET raw_capacity = $1 WHERE id = $2`, 1024, bigAZResID)
				must.ReturnT(t, s.Cluster.SIC.InvalidateService(s.Ctx, Some(db.ServiceType("second"))))
				tr.DBChanges().Ignore()
			}

			// deleted commitment → no such commitment
			s.Handler.RespondTo(s.Ctx, "DELETE /resources/v2/commitments/"+string(uuidCapacity)).ExpectStatus(t, http.StatusNoContent)
			tr.DBChanges().Ignore()
			s.Auditor.IgnoreEventsUntilNow()
			convertCommitmentAndExpectError(t, s, tr, uuidCapacity, map[string]any{
				"target_service_type":  "second",
				"target_resource_name": "capacity_80",
				"source_amount":        320,
				"target_amount":        3,
			}, func(r httptest.Response) {
				r.ExpectText(t, http.StatusNotFound, "no such commitment\n")
			})
		})
	}
}
