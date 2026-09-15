// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"net/http"
	"testing"
	"time"

	. "go.xyrillian.de/gg/option"

	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/jsonmatch"

	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

func commonExistingCommitmentSetup(t *testing.T, manager string) (s test.Setup, tr *easypg.Tracker, uuid liquid.CommitmentUUID, createdAt, expiresAt time.Time, expectedJSON jsonmatch.Object) {
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
	createdAt = s.Clock.Now().UTC()
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
