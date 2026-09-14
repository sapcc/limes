// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sapcc/go-api-declarations/cadf"
	"github.com/sapcc/go-bits/httptest"
	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"
	"go.xyrillian.de/gg/jsonmatch"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

// Helper function for a successful commitment deletion.
func deleteCommitmentAndExpectSuccess(t *testing.T, s test.Setup, tr *easypg.Tracker, liquidHandlesCommitments bool, uuid string, dbID int, ccr liquid.CommitmentChangeRequest) {
	t.Helper()
	path := "/resources/v2/commitments/" + uuid
	s.Handler.RespondTo(s.Ctx, "DELETE "+path).ExpectStatus(t, http.StatusNoContent)

	// if CCR is empty, this was an already deleted commitment and we don't need to check for any updates
	if ccr.InfoVersion == 0 {
		return
	}

	// assertions
	tr.DBChanges().AssertEqualf("UPDATE project_commitments SET status = 'deleted', deleted_at = %[1]d, updated_at = %[1]d WHERE id = %[2]d AND uuid = '%[3]s' AND transfer_token = NULL;", s.Clock.Now().Unix(), dbID, uuid)
	s.Auditor.ExpectEvents(t, cadf.Event{
		Action:      "delete",
		Outcome:     "success",
		Reason:      cadf.Reason{ReasonType: "HTTP", ReasonCode: "204"},
		RequestPath: path,
		Target: cadf.Resource{
			TypeURI:     "service/resources/commitment",
			ID:          uuid,
			DomainID:    "uuid-for-france",
			DomainName:  "france",
			ProjectID:   "uuid-for-paris",
			ProjectName: "paris",
			Attachments: []cadf.Attachment{must.Return(cadf.NewJSONAttachment("payload", ccr))},
		}})
	if liquidHandlesCommitments {
		assert.Equal(t, s.LiquidClients["first"].LastCommitmentChangeRequest, ccr)
		s.LiquidClients["first"].LastCommitmentChangeRequest = liquid.CommitmentChangeRequest{}
	} else {
		assert.Equal(t, s.LiquidClients["first"].LastCommitmentChangeRequest, liquid.CommitmentChangeRequest{})
	}
}

// Helper function for a failed commitment deletion.
func deleteCommitmentAndExpectError(t *testing.T, s test.Setup, tr *easypg.Tracker, uuid liquid.CommitmentUUID, ccr liquid.CommitmentChangeRequest, expect func(r httptest.Response)) {
	t.Helper()
	path := "/resources/v2/commitments/" + string(uuid)
	s.Handler.RespondTo(s.Ctx, "DELETE "+path).Expect(expect)

	// assertions
	tr.DBChanges().AssertEmpty()
	s.Auditor.ExpectEvents(t, nil...)
	assert.Equal(t, s.LiquidClients["first"].LastCommitmentChangeRequest, ccr)
}

func generateDeleteCCR(uuid liquid.CommitmentUUID, expiresAt time.Time) liquid.CommitmentChangeRequest {
	return liquid.CommitmentChangeRequest{
		AZ:          "az-one",
		InfoVersion: 1,
		ByProject: map[liquid.ProjectUUID]liquid.ProjectCommitmentChangeset{
			"uuid-for-paris": {
				ByResource: map[liquid.ResourceName]liquid.ResourceCommitmentChangeset{
					"capacity": {
						TotalConfirmedBefore: 10, TotalConfirmedAfter: 0, TotalGuaranteedBefore: 0, TotalGuaranteedAfter: 0, Commitments: []liquid.Commitment{{
							UUID:      uuid,
							OldStatus: Some(liquid.CommitmentStatusConfirmed),
							NewStatus: None[liquid.CommitmentStatus](),
							Amount:    10,
							ExpiresAt: expiresAt,
						}},
					},
				},
			},
		},
	}
}

func TestCommitmentDeleteBasic(t *testing.T) {
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

			// setup: ensure capacity is available for confirmed commitments
			firstCapacityAZOneID := s.GetAZResourceID("first", "capacity", "az-one")
			s.MustDBExec("UPDATE az_resources SET raw_capacity = $1 WHERE id = $2", 100, firstCapacityAZOneID)
			must.ReturnT(t, s.Cluster.SIC.InvalidateService(s.Ctx, Some(db.ServiceType("first"))))

			// setup: create one commitment via the POST API
			var uuid string
			expiresAt := s.Clock.Now().Add(1 * time.Hour).UTC()
			s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
				"amount":            10,
				"duration":          "1 hour",
				"project_id":        "uuid-for-paris",
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"status":            "confirmed",
			})).ExpectJSON(t, http.StatusCreated, jsonmatch.Object{
				"uuid":              jsonmatch.CaptureField(&uuid),
				"amount":            10,
				"duration":          "1 hour",
				"project_id":        "uuid-for-paris",
				"service_type":      "first",
				"resource_name":     "capacity",
				"availability_zone": "az-one",
				"status":            "confirmed",
				"created_at":        s.Clock.Now().UTC().Format(time.RFC3339),
				"creator_uuid":      "uuid-for-alice",
				"creator_name":      "alice@Default",
				"can_be_deleted":    true,
				"confirmed_at":      s.Clock.Now().UTC().Format(time.RFC3339),
				"expires_at":        expiresAt.Format(time.RFC3339),
				"updated_at":        s.Clock.Now().UTC().Format(time.RFC3339),
			})
			s.Auditor.IgnoreEventsUntilNow()
			s.Clock.StepBy(time.Hour)
			tr, tr0 := easypg.NewTracker(t, s.DB.DB)
			tr0.Ignore()

			deleteCommitmentAndExpectSuccess(t, s, tr, manager == "liquid", uuid, 1, generateDeleteCCR(liquid.CommitmentUUID(uuid), expiresAt))

			// the commitment is gone, subsequent calls return 204
			deleteCommitmentAndExpectSuccess(t, s, tr, true, uuid, 1, liquid.CommitmentChangeRequest{})
		})
	}
}

func TestCommitmentDeleteErrors(t *testing.T) {
	srvInfoFirst := test.DefaultLiquidServiceInfo("First")
	srvInfoSecond := test.DefaultLiquidServiceInfo("Second")
	for resName, resInfo := range srvInfoFirst.Resources {
		resInfo.HandlesCommitments = true
		srvInfoFirst.Resources[resName] = resInfo
	}
	for resName, resInfo := range srvInfoSecond.Resources {
		resInfo.HandlesCommitments = true
		srvInfoSecond.Resources[resName] = resInfo
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

	// setup: ensure capacity is available for confirmed commitments
	firstCapacityAZOneID := s.GetAZResourceID("first", "capacity", "az-one")
	s.MustDBExec("UPDATE az_resources SET raw_capacity = $1 WHERE id = $2", 1000, firstCapacityAZOneID)
	must.ReturnT(t, s.Cluster.SIC.InvalidateService(s.Ctx, Some(db.ServiceType("first"))))

	tr, _ := easypg.NewTracker(t, s.DB.DB)

	// non-existing commitment - as we have all permissions, we get 204
	deleteCommitmentAndExpectSuccess(t, s, tr, true, "00000000-0000-0000-0000-000000000099", 0, liquid.CommitmentChangeRequest{})

	// create a commitment via POST API, then advance 24 hours (cannot be deleted by non-admins)
	var uuidOne string
	expiresAt := s.Clock.Now().Add(1 * time.Hour).UTC()
	s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
		"amount":            10,
		"duration":          "1 hour",
		"project_id":        "uuid-for-paris",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
	})).ExpectJSON(t, http.StatusCreated, jsonmatch.Object{
		"uuid":              jsonmatch.CaptureField(&uuidOne),
		"amount":            10,
		"duration":          "1 hour",
		"project_id":        "uuid-for-paris",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
		"created_at":        s.Clock.Now().UTC().Format(time.RFC3339),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      s.Clock.Now().UTC().Format(time.RFC3339),
		"expires_at":        expiresAt.Format(time.RFC3339),
		"updated_at":        s.Clock.Now().UTC().Format(time.RFC3339),
	})
	s.Auditor.IgnoreEventsUntilNow()
	tr.DBChanges().Ignore()
	s.LiquidClients["first"].LastCommitmentChangeRequest = liquid.CommitmentChangeRequest{}
	s.Clock.StepBy(24 * time.Hour)

	// wrong token scope
	s.TokenValidator.Enforcer.AllowProject = false
	deleteCommitmentAndExpectError(t, s, tr, liquid.CommitmentUUID(uuidOne), liquid.CommitmentChangeRequest{}, func(r httptest.Response) {
		r.ExpectBody(t, http.StatusForbidden, []byte("Forbidden\n"))
	})
	s.TokenValidator.Enforcer.AllowProject = true

	// no admin privileges
	s.TokenValidator.Enforcer.AllowCommitmentDeleteAdmin = false
	deleteCommitmentAndExpectError(t, s, tr, liquid.CommitmentUUID(uuidOne), liquid.CommitmentChangeRequest{}, func(r httptest.Response) {
		r.ExpectBody(t, http.StatusForbidden, []byte("commitment cannot be deleted\n"))
	})

	// delete with admin privileges succeeds
	s.TokenValidator.Enforcer.AllowCommitmentDeleteAdmin = true
	deleteCommitmentAndExpectSuccess(t, s, tr, true, uuidOne, 1, generateDeleteCCR(liquid.CommitmentUUID(uuidOne), expiresAt))

	// create another commitment via POST API
	var uuidTwo string
	expiresAt2 := s.Clock.Now().Add(1 * time.Hour).UTC()
	s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
		"amount":            10,
		"duration":          "1 hour",
		"project_id":        "uuid-for-paris",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
	})).ExpectJSON(t, http.StatusCreated, jsonmatch.Object{
		"uuid":              jsonmatch.CaptureField(&uuidTwo),
		"amount":            10,
		"duration":          "1 hour",
		"project_id":        "uuid-for-paris",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
		"created_at":        s.Clock.Now().UTC().Format(time.RFC3339),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      s.Clock.Now().UTC().Format(time.RFC3339),
		"expires_at":        expiresAt2.Format(time.RFC3339),
		"updated_at":        s.Clock.Now().UTC().Format(time.RFC3339),
	})
	s.Auditor.IgnoreEventsUntilNow()
	tr.DBChanges().Ignore()
	s.LiquidClients["first"].LastCommitmentChangeRequest = liquid.CommitmentChangeRequest{}

	// simulate unresponsive liquid
	s.LiquidClients["first"].CommitmentChangeResponse.SetError(errors.New("simulated liquid error"))
	deleteCommitmentAndExpectError(t, s, tr, liquid.CommitmentUUID(uuidTwo), generateDeleteCCR(liquid.CommitmentUUID(uuidTwo), expiresAt2), func(r httptest.Response) {
		r.ExpectStatus(t, http.StatusInternalServerError) // the body has an error uuid which we cannot capture as of now
	})
}
