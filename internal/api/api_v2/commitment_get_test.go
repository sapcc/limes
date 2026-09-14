// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/jsonmatch"

	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

func TestCommitmentGetSingle(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(commitmentCreateConfigJSON),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)

	// setup: ensure capacity is available for confirmed commitments
	firstCapacityAZOneID := s.GetAZResourceID("first", "capacity", "az-one")
	firstCapacityTotalID := s.GetAZResourceID("first", "capacity", "total")
	s.MustDBExec("UPDATE az_resources SET raw_capacity = $1 WHERE id IN ($2, $3)", 100, firstCapacityAZOneID, firstCapacityTotalID)
	// update ServiceInfoCache (used by az_allocation_stats file)
	must.ReturnT(t, s.Cluster.SIC.InvalidateService(s.Ctx, Some(db.ServiceType("first"))))

	// setup: create one commitment via the POST API
	s.UpdateMockUserIdentity(map[string]string{
		"project_id":          "uuid-for-paris",
		"project_name":        "paris",
		"project_domain_name": "france",
		"project_domain_id":   "uuid-for-france",
	})

	var uuidOne string
	createdAt := s.Clock.Now().UTC().Format(time.RFC3339)
	expiresAt := s.Clock.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
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
		"created_at":        createdAt,
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      createdAt,
		"expires_at":        expiresAt,
		"updated_at":        createdAt,
	})
	s.Clock.StepBy(time.Hour)

	// success: get existing commitment
	fixturePath := "./fixtures/commitment-get-single.json"
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments/"+uuidOne).
		ExpectJSON(t, http.StatusOK,
			httptest.NewJQModifiableJSONFixture(fixturePath, "success").
				Modify(fmt.Sprintf(`.uuid = %q`, uuidOne)))

	// error: get non-existing commitment
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments/00000000-0000-0000-0000-000000000099").
		ExpectText(t, http.StatusNotFound, "no such commitment\n")

	// error: permission denied
	s.TokenValidator.Enforcer.AllowCommitmentGet = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments/"+uuidOne).
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowCommitmentGet = true

	// error: permission denied as regular user for obsolete commitment
	s.TokenValidator.Enforcer.ForbidWithObsolete = true
	s.Handler.RespondTo(s.Ctx, "DELETE /resources/v2/commitments/"+uuidOne).ExpectStatus(t, http.StatusNoContent)
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments/"+uuidOne).
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.ForbidWithObsolete = false

	// success: elevated user can see obsolete commitment (with updated timestamp)
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments/"+uuidOne).
		ExpectJSON(t, http.StatusOK,
			httptest.NewJQModifiableJSONFixture(fixturePath, "success").
				Modify(fmt.Sprintf(`.uuid = %q`, uuidOne)).
				Modify(`.status = "deleted"`).
				Modify(`.updated_at = "1970-01-01T01:00:00Z"`))
}

func TestCommitmentGetMultiple(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(commitmentCreateConfigJSON),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)

	// setup: ensure capacity is available for confirmed commitments
	firstCapacityAZOneID := s.GetAZResourceID("first", "capacity", "az-one")
	firstCapacityTotalID := s.GetAZResourceID("first", "capacity", "total")
	s.MustDBExec("UPDATE az_resources SET raw_capacity = $1 WHERE id IN ($2, $3)", 1000, firstCapacityAZOneID, firstCapacityTotalID)
	// update ServiceInfoCache (used by az_allocation_stats file)
	must.ReturnT(t, s.Cluster.SIC.InvalidateService(s.Ctx, Some(db.ServiceType("first"))))

	// helper to create a confirmed commitment via POST API
	createConfirmedCommitment := func(projectUUID, projectName, domainUUID, domainName string, amount int, uuidTarget *string) {
		t.Helper()
		s.UpdateMockUserIdentity(map[string]string{
			"project_id":          projectUUID,
			"project_name":        projectName,
			"project_domain_name": domainName,
			"project_domain_id":   domainUUID,
		})
		s.Handler.RespondTo(s.Ctx, "POST /resources/v2/commitments/new", httptest.WithJSONBody(map[string]any{
			"amount":            amount,
			"duration":          "1 hour",
			"project_id":        projectUUID,
			"service_type":      "first",
			"resource_name":     "capacity",
			"availability_zone": "az-one",
			"status":            "confirmed",
		})).ExpectJSON(t, http.StatusCreated, jsonmatch.Object{
			"uuid":              jsonmatch.CaptureField(uuidTarget),
			"amount":            amount,
			"duration":          "1 hour",
			"project_id":        projectUUID,
			"service_type":      "first",
			"resource_name":     "capacity",
			"availability_zone": "az-one",
			"status":            "confirmed",
			"created_at":        s.Clock.Now().UTC().Format(time.RFC3339),
			"creator_uuid":      "uuid-for-alice",
			"creator_name":      "alice@Default",
			"can_be_deleted":    true,
			"confirmed_at":      s.Clock.Now().UTC().Format(time.RFC3339),
			"expires_at":        s.Clock.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
			"updated_at":        s.Clock.Now().UTC().Format(time.RFC3339),
		})
	}

	// commitment 1: paris, amount 10
	var uuid1 string
	createConfirmedCommitment("uuid-for-paris", "paris", "uuid-for-france", "france", 10, &uuid1)

	// commitment 2: berlin, amount 20
	var uuid2 string
	createConfirmedCommitment("uuid-for-berlin", "berlin", "uuid-for-germany", "germany", 20, &uuid2)

	// commitment 3: dresden, amount 5 (will be made public via DB)
	s.Clock.StepBy(time.Hour)
	var uuid3 string
	createConfirmedCommitment("uuid-for-dresden", "dresden", "uuid-for-germany", "germany", 5, &uuid3)

	// make commitment 3 public via PATCH API
	var transferToken3 string
	s.Handler.RespondTo(s.Ctx, "PATCH /resources/v2/commitments/"+uuid3, httptest.WithJSONBody(map[string]any{
		"transfer_status": "public",
	})).ExpectJSON(t, http.StatusAccepted, jsonmatch.Object{
		"uuid":              uuid3,
		"amount":            5,
		"duration":          "1 hour",
		"project_id":        "uuid-for-dresden",
		"service_type":      "first",
		"resource_name":     "capacity",
		"availability_zone": "az-one",
		"status":            "confirmed",
		"transfer_status":   "public",
		"transfer_token":    jsonmatch.CaptureField(&transferToken3),
		"created_at":        s.Clock.Now().UTC().Format(time.RFC3339),
		"creator_uuid":      "uuid-for-alice",
		"creator_name":      "alice@Default",
		"can_be_deleted":    true,
		"confirmed_at":      s.Clock.Now().UTC().Format(time.RFC3339),
		"expires_at":        s.Clock.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339),
		"updated_at":        s.Clock.Now().UTC().Format(time.RFC3339),
	})

	// commitment 4: dresden, amount 5 (will be deleted)
	var uuid4 string
	createConfirmedCommitment("uuid-for-dresden", "dresden", "uuid-for-germany", "germany", 5, &uuid4)

	// mark commitment 4 as deleted
	s.Handler.RespondTo(s.Ctx, "DELETE /resources/v2/commitments/"+uuid4).ExpectStatus(t, http.StatusNoContent)

	s.Clock.StepBy(time.Hour)

	// To make the token project-scoped to "paris" in domain "france"
	s.UpdateMockUserIdentity(map[string]string{
		"project_id":          "uuid-for-paris",
		"project_name":        "paris",
		"project_domain_name": "france",
		"project_domain_id":   "uuid-for-france",
	})

	// helper: build jq modification to inject captured UUIDs and transfer token into fixture
	injectUUIDs := func(f httptest.JQModifiableContent) httptest.JQModifiableContent {
		return f.
			Modify(fmt.Sprintf(`.commitments[0].uuid = %q`, uuid1)).
			Modify(fmt.Sprintf(`.commitments[1].uuid = %q`, uuid2)).
			Modify(fmt.Sprintf(`.commitments[2].uuid = %q`, uuid3)).
			Modify(fmt.Sprintf(`.commitments[2].transfer_token = %q`, transferToken3)).
			Modify(fmt.Sprintf(`.commitments[3].uuid = %q`, uuid4))
	}

	// error cases
	// error: no main filter set (admin)
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments").
		ExpectText(t, http.StatusBadRequest, "one of \"category\" or \"resource\" must be set\n")

	// error: no main filter set (non-admin)
	s.TokenValidator.Enforcer.AllowCommitmentGetUnscoped = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments").
		ExpectText(t, http.StatusBadRequest, "one of \"public\", \"project_uuid\", \"domain_uuid\" must be set\n")
	s.TokenValidator.Enforcer.AllowCommitmentGetUnscoped = true

	// error: multiple main filters set
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?public=true&project_uuid=uuid-for-paris").
		ExpectText(t, http.StatusBadRequest, "only one of \"public\", \"project_uuid\", \"domain_uuid\" may be set\n")

	// error: category without service
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?project_uuid=uuid-for-paris&category=foo").
		ExpectText(t, http.StatusBadRequest, "\"category\" or \"resource\" require \"service\" to be set\n")

	// error: resource without service
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?project_uuid=uuid-for-paris&resource=capacity").
		ExpectText(t, http.StatusBadRequest, "\"category\" or \"resource\" require \"service\" to be set\n")

	// error: with=obsolete without permission
	s.TokenValidator.Enforcer.ForbidWithObsolete = true
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?project_uuid=uuid-for-paris&with=obsolete").
		ExpectText(t, http.StatusForbidden, "\"with=obsolete\" requires special permissions\n")
	s.TokenValidator.Enforcer.ForbidWithObsolete = false

	// error: no permission at all
	s.TokenValidator.Enforcer.AllowCommitmentGet = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?project_uuid=uuid-for-paris").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowCommitmentGet = true

	// error: no permission for public
	s.TokenValidator.Enforcer.AllowCommitmentGetPublic = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?public=true").
		ExpectText(t, http.StatusForbidden, "Forbidden\n")
	s.TokenValidator.Enforcer.AllowCommitmentGetPublic = true

	// --- Successful queries ---

	// success: filter by project_uuid
	fixturePath := "./fixtures/commitment-get-multiple.json"
	deletedModification := `del(.commitments.[] | select(.status == "deleted"))`
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?project_uuid=uuid-for-paris").
		ExpectJSON(t, http.StatusOK,
			injectUUIDs(httptest.NewJQModifiableJSONFixture(fixturePath, "single project")).
				Modify(deletedModification).
				Modify(`del(.commitments.[] | select(.project_id != "uuid-for-paris"))`))

	// success: filter by domain_uuid (switch to domain-scoped token)
	s.UpdateMockUserIdentity(map[string]string{
		"project_id":          "",
		"project_name":        "",
		"project_domain_name": "",
		"project_domain_id":   "",
		"domain_id":           "uuid-for-france",
		"domain_name":         "france",
	})
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?domain_uuid=uuid-for-germany").
		ExpectJSON(t, http.StatusOK,
			injectUUIDs(httptest.NewJQModifiableJSONFixture(fixturePath, "single domain")).
				Modify(deletedModification).
				Modify(`del(.commitments.[] | select(.project_id == "uuid-for-paris"))`))

	// success: filter by public (this token cannot see the project_uuid)
	s.TokenValidator.Enforcer.AllowCommitmentGet = false
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?public=true").
		ExpectJSON(t, http.StatusOK,
			injectUUIDs(httptest.NewJQModifiableJSONFixture(fixturePath, "public")).
				Modify(deletedModification).
				Modify(fmt.Sprintf(`del(.commitments.[] | select(.uuid != %q))`, uuid3)).
				Modify(`del(.commitments.[].project_id)`))

	// success: filter by public (this token can see the project_uuid)
	s.TokenValidator.Enforcer.AllowCommitmentGet = true
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?public=true").
		ExpectJSON(t, http.StatusOK,
			injectUUIDs(httptest.NewJQModifiableJSONFixture(fixturePath, "public")).
				Modify(deletedModification).
				Modify(fmt.Sprintf(`del(.commitments.[] | select(.uuid != %q))`, uuid3)))

	// success: with service filter (admin scenario)
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?service=first").
		ExpectJSON(t, http.StatusOK,
			injectUUIDs(httptest.NewJQModifiableJSONFixture(fixturePath, "service filter")).
				Modify(deletedModification))

	// success: obsolete (and service filter, because it's always required)
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?with=obsolete&service=first").
		ExpectJSON(t, http.StatusOK,
			injectUUIDs(httptest.NewJQModifiableJSONFixture(fixturePath, "obsolete")))

	// success: updated after
	s.Handler.RespondTo(s.Ctx, "GET /resources/v2/commitments?service=first&updated_after="+s.Clock.Now().Add(-1*time.Hour).Format(time.RFC3339)).
		ExpectJSON(t, http.StatusOK,
			injectUUIDs(httptest.NewJQModifiableJSONFixture(fixturePath, "updated_after")).
				Modify(fmt.Sprintf(`del(.commitments.[] | select(.uuid != %q))`, uuid3)))
}
