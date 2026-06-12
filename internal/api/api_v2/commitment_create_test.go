// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/jsonmatch"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

const commitmentCreateConfigJSON = `{
	"availability_zones": ["az-one", "az-two"],
	"discovery": {
		"method": "static",
		"static_config": {
			"domains": [
				{"name": "germany", "id": "uuid-for-germany"},
				{"name": "france", "id": "uuid-for-france"}
			],
			"projects": {
				"uuid-for-germany": [
					{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"},
					{"name": "dresden", "id": "uuid-for-dresden", "parent_id": "uuid-for-berlin"}
				],
				"uuid-for-france": [
					{"name": "paris", "id": "uuid-for-paris", "parent_id": "uuid-for-france"}
				]
			}
		}
	},
	"areas": { "first": { "display_name": "First" }, "second": { "display_name": "Second" }},
	"liquids": {
		"first": {
			"area": "first",
			"commitment_behavior_per_resource": [
				{
					"key": "capacity",
					"value": {
						"durations_per_domain": [{"key": ".*", "value": ["1 hour", "2 hours"]}]
					}
				},
				{
					"key": "things",
					"value": {
						"durations_per_domain": [{"key": ".*", "value": ["1 hour", "2 hours"]}],
						"min_confirm_date": "1970-01-08T00:00:00Z" // one week after start of mock.Clock
					}
				}
			]
		},
		"second": {
			"area": "second"
		}
	},
	"mail_notifications": {
		// Mail templates are configured solely to prove that API use does not generate mail notifications.
		"templates": {
			"confirmed_commitments":   { "subject": "Confirmed!",   "body": "Hello" },
			"expiring_commitments":    { "subject": "Expiring!",    "body": "Hello" },
			"transferred_commitments": { "subject": "Transferred!", "body": "Hello" }
		}
	}
}`

// Helper function for a successful commitment creation. This always tests the dry-run first, and then does the actual creation.
func createCommitmentAndExpectSuccess(t *testing.T, s test.Setup, tr *easypg.Tracker, ccrAcceptedByLiquid bool, request map[string]any, expected jsonmatch.Object, getTarget func() cadf.Resource) {
	t.Helper()
	ctx := t.Context()
	mockLiquid := s.LiquidClients[db.ServiceType(request["service_type"].(string))]

	if ccrAcceptedByLiquid {
		// explicitly clear LastCommitmentChangeRequest, so we can detect if the dry-run incorrectly writes into it
		mockLiquid.LastCommitmentChangeRequest = liquid.CommitmentChangeRequest{}
	}

	// check that the dry-run request succeeds, without causing any side effects in the DB or audit trail
	dryrunRequest := maps.Clone(request)
	dryrunRequest["dry_run"] = true
	dryrunExpected := maps.Clone(expected)
	dryrunExpected["uuid"] = jsonmatch.Irrelevant()
	s.Handler.RespondTo(ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(dryrunRequest)).
		ExpectJSON(t, http.StatusCreated, dryrunExpected)

	s.Auditor.ExpectEvents(t, nil...)
	tr.DBChanges().AssertEmpty()
	if ccrAcceptedByLiquid {
		// if there was a CCR, then it must have been a dry-run
		if !reflect.DeepEqual(mockLiquid.LastCommitmentChangeRequest, liquid.CommitmentChangeRequest{}) {
			assert.Equal(t, mockLiquid.LastCommitmentChangeRequest.DryRun, true)
		}
	}

	// after creating the commitment, we expect to see an audit event
	// (the DB effect is not checked here; the caller will take care of that afterwards)
	s.Handler.RespondTo(ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(request)).
		ExpectJSON(t, http.StatusCreated, expected)
	target := getTarget() // this is a callback because we need to wait for ExpectJSON() to capture the UUID in expected["uuid"] first
	s.Auditor.ExpectEvents(t, cadf.Event{
		Action:      "create",
		Outcome:     "success",
		Reason:      cadf.Reason{ReasonType: "HTTP", ReasonCode: "201"},
		RequestPath: "/resources/v2/commitments/new",
		Target:      target,
	})

	if ccrAcceptedByLiquid {
		// check that the mock liquid saw the correct CommitmentChangeRequest
		// (the same as inside the audit event payload)
		actualCCR := must.Return(json.Marshal(mockLiquid.LastCommitmentChangeRequest))
		var expectedCCR jsonmatch.Object
		must.SucceedT(t, json.Unmarshal([]byte(target.Attachments[0].Content.(string)), &expectedCCR))
		for _, diff := range expectedCCR.DiffAgainst(actualCCR) {
			t.Error("in MockLiquid.LastCommitmentChangeRequest: " + diff.String())
		}
	}
}

// Helper function for a failed commitment creation. This tests that the dry-run and the actual run fail in the same way.
func createCommitmentAndExpectError(t *testing.T, s test.Setup, tr *easypg.Tracker, request map[string]any, expect func(r httptest.Response)) {
	t.Helper()
	ctx := t.Context()

	dryrunRequest := maps.Clone(request)
	dryrunRequest["dry_run"] = true

	s.Handler.RespondTo(ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(dryrunRequest)).Expect(expect)
	s.Handler.RespondTo(ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(request)).Expect(expect)
	tr.DBChanges().AssertEmpty() // since both requests failed
}

func TestCommitmentCreateBasic(t *testing.T) {
	// run this test twice, once with commitments managed by Limes, and once managed by the liquid
	for _, manager := range []string{"limes", "liquid"} {
		t.Run("managedby="+manager, func(t *testing.T) {
			srvInfoFirst := test.DefaultLiquidServiceInfo("First")
			srvInfoSecond := test.DefaultLiquidServiceInfo("Second")
			if manager == "liquid" {
				for resName, resInfo := range srvInfoFirst.Resources {
					resInfo.HandlesCommitments = true
					srvInfoFirst.Resources[resName] = resInfo
				}
				for resName, resInfo := range srvInfoSecond.Resources {
					resInfo.HandlesCommitments = true
					srvInfoSecond.Resources[resName] = resInfo
				}
			}

			s := test.NewSetup(t,
				test.WithConfig(commitmentCreateConfigJSON),
				test.WithMockLiquidClient("first", srvInfoFirst),
				test.WithPersistedServiceInfo("first", srvInfoFirst),
				test.WithMockLiquidClient("second", srvInfoSecond),
				test.WithPersistedServiceInfo("second", srvInfoSecond),
				test.WithInitialDiscovery,
				test.WithEmptyResourceRecordsAsNeeded,
			)
			s.Clock.StepBy(time.Hour) // to detect later that services.next_scrape_at was moved to NOW() after adding a confirmed commitment

			tr, tr0 := easypg.NewTracker(t, s.DB.Db)
			tr0.Ignore()

			// it is always possible to create "pending" commitments, regardless of capacity
			var uuid1 string
			createCommitmentAndExpectSuccess(t, s, tr, manager == "liquid", map[string]any{
				"amount":            1,
				"duration":          "1 hour",
				"project_id":        "uuid-for-berlin",
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"status":            "pending",
			}, jsonmatch.Object{
				"uuid":              jsonmatch.CaptureField(&uuid1),
				"amount":            1,
				"duration":          "1 hour",
				"project_id":        "uuid-for-berlin",
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"status":            "pending",
				"created_at":        s.Clock.Now().Unix(),
				"creator_uuid":      "uuid-for-alice",
				"creator_name":      "alice@Default",
				"can_be_deleted":    true,
				"confirm_by":        s.Clock.Now().Unix(),
				"expires_at":        s.Clock.Now().Add(1 * time.Hour).Unix(),
				"updated_at":        s.Clock.Now().Unix(),
			}, func() cadf.Resource {
				return cadf.Resource{
					TypeURI:     "service/resources/commitment",
					ID:          uuid1,
					DomainID:    "uuid-for-germany",
					DomainName:  "germany",
					ProjectID:   "uuid-for-berlin",
					ProjectName: "berlin",
					Attachments: []cadf.Attachment{must.Return(cadf.NewJSONAttachment("payload", map[string]any{
						"az":          "az-one",
						"dryRun":      false,
						"infoVersion": 1,
						"byProject": map[string]map[string]any{
							"uuid-for-berlin": {
								"byResource": map[string]map[string]any{
									"capacity": {
										"totalConfirmedBefore":  0,
										"totalConfirmedAfter":   0,
										"totalGuaranteedBefore": 0,
										"totalGuaranteedAfter":  0,
										"commitments": []map[string]any{{
											"amount":    1,
											"confirmBy": s.Clock.Now().UTC().Format(time.RFC3339),
											"expiresAt": s.Clock.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
											"newStatus": "pending",
											"oldStatus": nil,
											"uuid":      uuid1,
										}},
									},
								},
							},
						},
					}))},
				}
			})
			tr.DBChanges().AssertEqualf(`
			INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirm_by, expires_at, creation_context_json, updated_at) VALUES (2, '%[1]s', 1, 2, 'pending', 1, '1 hour', %[2]d, 'uuid-for-alice', 'alice@Default', %[2]d, %[3]d, '{"reason": "create"}', %[2]d);
		`,
				uuid1,
				s.Clock.Now().Unix(),
				s.Clock.Now().Add(1*time.Hour).Unix(),
			)

			// same for "planned" commitments; these are also allowed for resources with a min_confirm_date in the future
			const oneDay time.Duration = 24 * time.Hour
			var uuid2 string
			createCommitmentAndExpectSuccess(t, s, tr, manager == "liquid", map[string]any{
				"amount":            2,
				"duration":          "1 hour",
				"project_id":        "uuid-for-berlin",
				"service_type":      "first",
				"resource_name":     "things",
				"availability_zone": "any",
				"status":            "planned",
				"confirm_by":        s.Clock.Now().Add(10 * oneDay).Unix(),
			}, jsonmatch.Object{
				"uuid":              jsonmatch.CaptureField(&uuid2),
				"amount":            2,
				"duration":          "1 hour",
				"project_id":        "uuid-for-berlin",
				"service_type":      "first",
				"resource_name":     "things",
				"availability_zone": "any",
				"status":            "planned",
				"created_at":        s.Clock.Now().Unix(),
				"creator_uuid":      "uuid-for-alice",
				"creator_name":      "alice@Default",
				"can_be_deleted":    true,
				"confirm_by":        s.Clock.Now().Add(10 * oneDay).Unix(),
				"expires_at":        s.Clock.Now().Add(10*oneDay + 1*time.Hour).Unix(),
				"updated_at":        s.Clock.Now().Unix(),
			}, func() cadf.Resource {
				return cadf.Resource{
					TypeURI:     "service/resources/commitment",
					ID:          uuid2,
					DomainID:    "uuid-for-germany",
					DomainName:  "germany",
					ProjectID:   "uuid-for-berlin",
					ProjectName: "berlin",
					Attachments: []cadf.Attachment{must.Return(cadf.NewJSONAttachment("payload", map[string]any{
						"az":          "any",
						"dryRun":      false,
						"infoVersion": 1,
						"byProject": map[string]map[string]any{
							"uuid-for-berlin": {
								"byResource": map[string]map[string]any{
									"things": {
										"totalConfirmedBefore":  0,
										"totalConfirmedAfter":   0,
										"totalGuaranteedBefore": 0,
										"totalGuaranteedAfter":  0,
										"commitments": []map[string]any{{
											"amount":    2,
											"confirmBy": s.Clock.Now().Add(10 * oneDay).UTC().Format(time.RFC3339),
											"expiresAt": s.Clock.Now().Add(10*oneDay + 1*time.Hour).UTC().Format(time.RFC3339),
											"newStatus": "planned",
											"oldStatus": nil,
											"uuid":      uuid2,
										}},
									},
								},
							},
						},
					}))},
				}
			})
			tr.DBChanges().AssertEqualf(`
			INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirm_by, expires_at, creation_context_json, updated_at) VALUES (4, '%[1]s', 1, 6, 'planned', 2, '1 hour', %[2]d, 'uuid-for-alice', 'alice@Default', %[3]d, %[4]d, '{"reason": "create"}', %[2]d);
		`,
				uuid2,
				s.Clock.Now().Unix(),
				s.Clock.Now().Add(10*oneDay).Unix(),
				s.Clock.Now().Add(10*oneDay+1*time.Hour).Unix(),
			)

			// "confirmed" commitments require capacity to be present
			firstCapacityAZTwoID := s.GetAZResourceID("first", "capacity", "az-two")
			firstCapacityTotalID := s.GetAZResourceID("first", "capacity", "total")
			s.MustDBExec("UPDATE az_resources SET raw_capacity = $1 WHERE id IN ($2, $3)", 100, firstCapacityAZTwoID, firstCapacityTotalID)
			tr.DBChanges().Ignore()

			var uuid3 string
			createCommitmentAndExpectSuccess(t, s, tr, manager == "liquid", map[string]any{
				"amount":            3,
				"duration":          "1 hour",
				"project_id":        "uuid-for-berlin",
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-two",
				"status":            "confirmed",
			}, jsonmatch.Object{
				"uuid":              jsonmatch.CaptureField(&uuid3),
				"amount":            3,
				"duration":          "1 hour",
				"project_id":        "uuid-for-berlin",
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-two",
				"status":            "confirmed",
				"created_at":        s.Clock.Now().Unix(),
				"creator_uuid":      "uuid-for-alice",
				"creator_name":      "alice@Default",
				"can_be_deleted":    true,
				"confirmed_at":      s.Clock.Now().Unix(),
				"expires_at":        s.Clock.Now().Add(1 * time.Hour).Unix(),
				"updated_at":        s.Clock.Now().Unix(),
			}, func() cadf.Resource {
				return cadf.Resource{
					TypeURI:     "service/resources/commitment",
					ID:          uuid3,
					DomainID:    "uuid-for-germany",
					DomainName:  "germany",
					ProjectID:   "uuid-for-berlin",
					ProjectName: "berlin",
					Attachments: []cadf.Attachment{must.Return(cadf.NewJSONAttachment("payload", map[string]any{
						"az":          "az-two",
						"dryRun":      false,
						"infoVersion": 1,
						"byProject": map[string]map[string]any{
							"uuid-for-berlin": {
								"byResource": map[string]map[string]any{
									"capacity": {
										"totalConfirmedBefore":  0,
										"totalConfirmedAfter":   3,
										"totalGuaranteedBefore": 0,
										"totalGuaranteedAfter":  0,
										"commitments": []map[string]any{{
											"amount":    3,
											"expiresAt": s.Clock.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
											"newStatus": "confirmed",
											"oldStatus": nil,
											"uuid":      uuid3,
										}},
									},
								},
							},
						},
					}))},
				}
			})
			tr.DBChanges().AssertEqualf(`
			INSERT INTO project_commitments (id, uuid, project_id, az_resource_id, status, amount, duration, created_at, creator_uuid, creator_name, confirmed_at, expires_at, creation_context_json, updated_at) VALUES (6, '%[1]s', 1, 3, 'confirmed', 3, '1 hour', %[2]d, 'uuid-for-alice', 'alice@Default', %[2]d, %[3]d, '{"reason": "create"}', %[2]d);
			UPDATE services SET next_scrape_at = %[2]d WHERE id = 1 AND type = 'first' AND liquid_version = 1;
		`,
				uuid3,
				s.Clock.Now().Unix(),
				s.Clock.Now().Add(1*time.Hour).Unix(),
			)
		})
	}
}

func TestCommitmentCreateValidationErrors(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(commitmentCreateConfigJSON),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)

	tr, tr0 := easypg.NewTracker(t, s.DB.Db)
	tr0.Ignore()

	s.TokenValidator.Enforcer.AllowCommitmentCreate = false
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "pending",
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusForbidden, "Forbidden\n")
	})
	s.TokenValidator.Enforcer.AllowCommitmentCreate = true

	// no such project/service/resource/AZ
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-chemnitz",
		"service_type":      "nonexistent",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "pending",
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusNotFound, "no such project (UUID = uuid-for-chemnitz)\n")
	})
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "nonexistent",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "pending",
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusNotFound, "no such service\n")
	})
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "nonexistent",
		"availability_zone": "az-one",
		"status":            "pending",
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusNotFound, "no such resource\n")
	})
	for _, az := range []string{"az-three", "unknown", ""} {
		createCommitmentAndExpectError(t, s, tr, map[string]any{
			"amount":            1,
			"duration":          "1 hour",
			"project_id":        "uuid-for-berlin",
			"service_type":      "first",
			"resource_name":     "capacity",
			"availability_zone": az,
			"status":            "pending",
		}, func(r httptest.Response) {
			r.ExpectText(t, http.StatusNotFound, "no such availability zone\n")
		})
	}

	// invalid AZ choice for resource
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "things", // FlatTopology!
		"availability_zone": "az-one",
		"status":            "pending",
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusUnprocessableEntity, "resource does not accept AZ-aware commitments, so the AZ must be set to \"any\"\n")
	})
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "capacity", // AZAwareTopology!
		"availability_zone": "any",
		"status":            "pending",
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusUnprocessableEntity, "resource is AZ-aware, so the AZ may not be set to \"any\"\n")
	})

	// resource is forbidden
	berlinID := s.GetProjectID("berlin")
	firstCapacityID := s.GetResourceID("first", "capacity")
	s.MustDBExec(`UPDATE project_resources SET forbidden = $1 WHERE project_id = $2 AND resource_id = $3`, true, berlinID, firstCapacityID)
	tr.DBChanges().Ignore()
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "pending",
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusUnprocessableEntity, "resource is not enabled in this project\n")
	})
	s.MustDBExec(`UPDATE project_resources SET forbidden = $1 WHERE project_id = $2 AND resource_id = $3`, false, berlinID, firstCapacityID)
	tr.DBChanges().Ignore()

	// resource does not allow commitments
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "second",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "pending",
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusUnprocessableEntity, "commitments are not enabled for this resource\n")
	})

	// invalid choice of amount
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            -42,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusBadRequest, "request body is not valid JSON: json: cannot unmarshal number -42 into Go struct field CommitmentRequest.amount of type uint64\n")
	})
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            0,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusUnprocessableEntity, "amount of committed resource must be greater than zero\n")
	})

	// invalid choice of duration
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "3 hours",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "pending",
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusUnprocessableEntity, "unacceptable commitment duration for this resource; acceptable values: [\"1 hour\",\"2 hours\"]\n")
	})

	// invalid choice of status
	for _, status := range []string{"guaranteed", "active", "superseded", "expired", "deleted", "unknown"} {
		createCommitmentAndExpectError(t, s, tr, map[string]any{
			"amount":            1,
			"duration":          "1 hour",
			"project_id":        "uuid-for-berlin",
			"service_type":      "first",
			"resource_name":     "capacity",
			"availability_zone": "az-one",
			"status":            status,
		}, func(r httptest.Response) {
			r.ExpectText(t, http.StatusUnprocessableEntity, "initial commitment status value is invalid\n")
		})
	}

	// invalid presence/absence of confirm_by for chosen state
	for _, status := range []string{"planned"} {
		createCommitmentAndExpectError(t, s, tr, map[string]any{
			"amount":            1,
			"duration":          "1 hour",
			"project_id":        "uuid-for-berlin",
			"service_type":      "first",
			"resource_name":     "capacity",
			"availability_zone": "az-one",
			"status":            status,
		}, func(r httptest.Response) {
			r.ExpectText(t, http.StatusUnprocessableEntity, "confirm_by must be set for the requested initial commitment status\n")
		})
	}
	for _, status := range []string{"pending", "confirmed"} {
		createCommitmentAndExpectError(t, s, tr, map[string]any{
			"amount":            1,
			"duration":          "1 hour",
			"project_id":        "uuid-for-berlin",
			"service_type":      "first",
			"resource_name":     "capacity",
			"availability_zone": "az-one",
			"status":            status,
			"confirm_by":        s.Clock.Now().Add(1 * time.Hour).Unix(),
		}, func(r httptest.Response) {
			r.ExpectText(t, http.StatusUnprocessableEntity, "confirm_by may not be set for the requested initial commitment status\n")
		})
	}

	// invalid choice of confirm_by
	s.Clock.StepBy(2 * time.Hour)
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "planned",
		"confirm_by":        s.Clock.Now().Add(-1 * time.Hour).Unix(),
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusUnprocessableEntity, "confirm_by may not be set in the past\n")
	})

	// invalid choice of confirm_by: "things" has min_confirm_date = "1970-01-08T00:00:00Z",
	// which is one week after the current time on the mock clock
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "things",
		"availability_zone": "any",
		"status":            "pending", // i.e. confirm_by defaults to NOW()
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusUnprocessableEntity, "this commitment needs a `confirm_by` timestamp at or after 1970-01-08T00:00:00Z\n")
	})
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "things",
		"availability_zone": "any",
		"status":            "planned",
		"confirm_by":        s.Clock.Now().Add(1 * time.Hour).Unix(),
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusUnprocessableEntity, "this commitment needs a `confirm_by` timestamp at or after 1970-01-08T00:00:00Z\n")
	})

	// invalid choice of notify_on_confirm
	createCommitmentAndExpectError(t, s, tr, map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
		"notify_on_confirm": true,
	}, func(r httptest.Response) {
		r.ExpectText(t, http.StatusUnprocessableEntity, "notify_on_confirm may not be set for commitments with immediate confirmation\n")
	})
}

func TestCommitmentCreateRejectedByLiquid(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo("First")
	for resName, resInfo := range srvInfoFirst.Resources {
		resInfo.HandlesCommitments = true
		srvInfoFirst.Resources[resName] = resInfo
	}

	ctx := t.Context()
	s := test.NewSetup(t,
		test.WithConfig(commitmentCreateConfigJSON),
		test.WithMockLiquidClient("first", srvInfoFirst),
		test.WithPersistedServiceInfo("first", srvInfoFirst),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)

	// test rejection by liquid without RetryAt
	mockLiquid := s.LiquidClients["first"]
	mockLiquid.CommitmentChangeResponse.Set(liquid.CommitmentChangeResponse{
		RejectionReason: "datacenter is not accepting new commitments at this time (reason: on fire)",
	})
	s.Handler.RespondTo(ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-two",
		"status":            "confirmed",
	})).
		ExpectHeader(t, "Retry-After", ""). // not set
		ExpectText(t, http.StatusConflict, "datacenter is not accepting new commitments at this time (reason: on fire)\n")

	// test rejection by liquid with RetryAt
	mockLiquid.CommitmentChangeResponse.Set(liquid.CommitmentChangeResponse{
		RejectionReason: "could not purchase new capacity, Sammy bought it all",
		RetryAt:         Some(s.Clock.Now().Add(1 * time.Hour)),
	})
	s.Handler.RespondTo(ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
		"amount":            1,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
	})).
		ExpectHeader(t, "Retry-After", s.Clock.Now().Add(1*time.Hour).Format(time.RFC1123)).
		ExpectText(t, http.StatusConflict, "could not purchase new capacity, Sammy bought it all\n")
}

func TestCommitmentCreateRejectedByLimes(t *testing.T) {
	ctx := t.Context()
	s := test.NewSetup(t,
		test.WithConfig(commitmentCreateConfigJSON),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)
	// set up some capacity in first/capacity, and allocate some of it with commitments and usage
	firstCapacityID := s.GetResourceID("first", "capacity")
	for az, capacity := range map[liquid.AvailabilityZone]uint64{
		"az-one": 20,
		"az-two": 30,
		"total":  50,
	} {
		s.MustDBExec(`UPDATE az_resources SET raw_capacity = $1 WHERE az = $2 AND resource_id = $3`,
			capacity, az, firstCapacityID)
	}
	dresdenID := s.GetProjectID("dresden")
	firstCapacityOneID := s.GetAZResourceID("first", "capacity", "az-one")
	s.MustDBExec(`UPDATE project_az_resources SET usage = $1 WHERE project_id = $2 AND az_resource_id = $3`,
		10, dresdenID, firstCapacityOneID)
	s.Handler.RespondTo(ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
		"amount":            5,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
	})).ExpectStatus(t, http.StatusCreated)

	// test rejection by Limes: on 20 capacity, we have 10 used by dresden
	// and 5 committed by berlin, so this additional commitment does not fit
	s.Handler.RespondTo(ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
		"amount":            15,
		"duration":          "1 hour",
		"project_id":        "uuid-for-berlin",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
	})).ExpectText(t, http.StatusConflict, "not enough capacity!\n")
}
